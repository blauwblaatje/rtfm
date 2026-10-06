// Package replay rebuilds a game summary from a CRG event log (see README.md).
package replay

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Result of a replay.
type Output struct {
	Summary  *Summary
	Warnings []string // inconsistencies that don't stop the replay
}

// Replay applies events in order and returns the resulting summary.
// Events listed in an effective Undone are skipped. Checkpoints are not
// checked here; see Verify.
func Replay(events []*Event) (*Output, error) {
	r, err := NewReplayer(events)
	if err != nil {
		return nil, err
	}
	if r.st.s == nil {
		return nil, fmt.Errorf("log has no GameCreated event")
	}
	s := r.Summary()
	r.st.checkOfficialScore()
	return &Output{Summary: s, Warnings: r.st.warnings}, nil
}

// A Replayer keeps a replayed game and applies further events to it one at a
// time, for the live engine. It is not safe for concurrent use.
type Replayer struct {
	st      *state
	lastSeq int
}

// NewReplayer replays events (skipping those an Undone reverts) and returns
// the replayer, ready for more.
func NewReplayer(events []*Event) (*Replayer, error) {
	skip, err := UndoneSeqs(events)
	if err != nil {
		return nil, err
	}
	r := &Replayer{st: newState()}
	for _, e := range events {
		if skip[e.Seq] {
			if e.Type != "Checkpoint" {
				r.lastSeq = e.Seq
			}
			continue
		}
		if err := r.Apply(e); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Apply applies one more event. An Undone is only recorded here; replaying
// what it reverts needs NewReplayer over the whole log. After an error the
// game may be partly changed, so the caller should start again from the log.
func (r *Replayer) Apply(e *Event) error {
	if e.Type != "Checkpoint" {
		r.lastSeq = e.Seq
	}
	if r.st.s == nil && e.Type != "GameCreated" {
		return fmt.Errorf("seq %d (%s): the first event must be GameCreated", e.Seq, e.Type)
	}
	if err := r.st.apply(e); err != nil {
		return fmt.Errorf("seq %d (%s): %w", e.Seq, e.Type, err)
	}
	return nil
}

// Summary returns the game as it stands. It is the replayer's own copy:
// later calls to Apply change it.
func (r *Replayer) Summary() *Summary {
	if r.st.s == nil {
		return nil
	}
	r.st.s.Log = &LogInfo{LastSeq: r.lastSeq}
	return r.st.s
}

// Warnings returns the inconsistencies found so far.
func (r *Replayer) Warnings() []string { return r.st.warnings }

// UndoneSeqs returns the seq numbers to skip. It walks the log backwards so
// that undoing an Undone restores the events that one had reverted.
func UndoneSeqs(events []*Event) (map[int]bool, error) {
	skip := map[int]bool{}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Type != "Undone" || skip[e.Seq] {
			continue
		}
		for _, r := range e.Reverts {
			if r >= e.Seq {
				return nil, fmt.Errorf("seq %d (Undone): can only revert earlier events, not seq %d", e.Seq, r)
			}
			skip[r] = true
		}
	}
	return skip, nil
}

type state struct {
	s           *Summary
	periods     map[string]*Period
	jams        map[string]*Jam
	jamPeriod   map[string]*Period
	trips       map[string]*Trip
	tripTJ      map[string]*TeamJam
	timeouts    map[string]*Timeout
	toPeriod    map[string]*Period
	skaters     map[string]*Skater
	skaterTeam  map[string]string
	staff       map[string]*Staff
	staffTeam   map[string]string
	officials   map[string]*Official
	penalties   map[string]*Penalty
	boxTrips    map[string]*BoxTrip
	adjustments map[string]*ScoreAdjustment
	upcoming    *Jam // announced by JamUpcoming, not yet started
	warnings    []string
}

func newState() *state {
	return &state{
		periods: map[string]*Period{}, jams: map[string]*Jam{}, jamPeriod: map[string]*Period{},
		trips: map[string]*Trip{}, tripTJ: map[string]*TeamJam{},
		timeouts: map[string]*Timeout{}, toPeriod: map[string]*Period{},
		skaters: map[string]*Skater{}, skaterTeam: map[string]string{},
		staff: map[string]*Staff{}, staffTeam: map[string]string{},
		officials: map[string]*Official{}, penalties: map[string]*Penalty{},
		boxTrips: map[string]*BoxTrip{}, adjustments: map[string]*ScoreAdjustment{},
	}
}

func (st *state) warnf(format string, args ...any) {
	st.warnings = append(st.warnings, fmt.Sprintf(format, args...))
}

func (st *state) apply(e *Event) error {
	s := st.s
	switch e.Type {

	// --- setup ---------------------------------------------------------
	case "GameCreated":
		if s != nil {
			return fmt.Errorf("game already created")
		}
		rs := *e.Ruleset
		rs.Overrides = map[string]any{}
		setOverrides(rs.Overrides, e.Ruleset.Overrides)
		st.s = &Summary{
			Schema:  "crg-game/1",
			ID:      e.Game,
			Name:    deref(e.Name),
			State:   "prepared",
			Ruleset: rs,
			Teams: []*Team{
				{Team: "1", Skaters: []*Skater{}},
				{Team: "2", Skaters: []*Skater{}},
			},
			Periods:  []*Period{},
			Timezone: deref(e.Timezone),
			GameType: deref(e.GameType),
			CueMode:  deref(e.CueMode),
		}
		return st.updateInfo(e.Info)
	case "GameInfoUpdated":
		setStr(&st.s.Timezone, e.Timezone)
		setStr(&st.s.GameType, e.GameType)
		setStr(&st.s.CueMode, e.CueMode)
		return st.updateInfo(e.Info)
	case "RulesChanged":
		setOverrides(s.Ruleset.Overrides, e.Overrides)

	case "TeamSet":
		t := st.team(e.Team)
		*t = Team{Team: t.Team, Skaters: t.Skaters}
		applyTeamFields(t, e)
	case "TeamUpdated":
		applyTeamFields(st.team(e.Team), e)

	case "SkaterAdded":
		if st.skaters[e.Skater.V] != nil {
			return fmt.Errorf("skater %q already exists", e.Skater.V)
		}
		sk := &Skater{ID: e.Skater.V}
		if err := applySkaterFields(sk, e); err != nil {
			return err
		}
		t := st.team(e.Team)
		t.Skaters = append(t.Skaters, sk)
		st.skaters[sk.ID] = sk
		st.skaterTeam[sk.ID] = e.Team
	case "SkaterUpdated":
		sk, err := st.skater(e.Skater.V)
		if err != nil {
			return err
		}
		return applySkaterFields(sk, e)
	case "SkaterRemoved":
		sk, err := st.skater(e.Skater.V)
		if err != nil {
			return err
		}
		// A skater the game refers to can't go: the paperwork would lose
		// them. Marking them Not Skating (flag ALT) is the way to take them
		// off the roster.
		if what := st.skaterUsed(sk.ID); what != "" {
			return fmt.Errorf("#%s has %s in this game, so can't be removed; replace them by the right skater (SkaterReplaced) or mark them Not Skating", sk.Number, what)
		}
		t := st.team(st.skaterTeam[sk.ID])
		t.Skaters = remove(t.Skaters, sk)
		delete(st.skaters, sk.ID)

	case "SkaterReplaced":
		from, err := st.skater(e.Skater.V)
		if err != nil {
			return err
		}
		to, err := st.skater(e.By)
		if err != nil {
			return err
		}
		if from == to || st.skaterTeam[from.ID] != st.skaterTeam[to.ID] {
			return fmt.Errorf("#%s can only be replaced by another skater of the same team", from.Number)
		}
		// Penalties: keep the slot if it's free for the other skater.
		for _, p := range st.s.Penalties {
			if p.Skater != from.ID {
				continue
			}
			want := p.Slot
			p.Skater = "" // so its own slot doesn't count as taken
			slot, err := st.penaltySlot(to.ID, &want)
			if err != nil {
				if slot, err = st.penaltySlot(to.ID, nil); err != nil {
					return err
				}
			}
			p.Skater, p.Slot = to.ID, slot
		}
		for _, bt := range st.s.BoxTrips {
			if bt.Skater == from.ID {
				bt.Skater = to.ID
			}
		}
		for _, j := range st.jams {
			for _, tj := range j.Teams {
				for _, f := range tj.Lineup {
					if f != nil && f.Skater == from.ID {
						f.Skater = to.ID
					}
				}
			}
		}
		t := st.team(st.skaterTeam[from.ID])
		t.Skaters = remove(t.Skaters, from)
		delete(st.skaters, from.ID)

	case "StaffAdded":
		if st.staff[e.Staff] != nil {
			return fmt.Errorf("staff %q already exists", e.Staff)
		}
		sf := &Staff{ID: e.Staff}
		applyStaffFields(sf, e)
		t := st.team(e.Team)
		t.Staff = append(t.Staff, sf)
		st.staff[sf.ID], st.staffTeam[sf.ID] = sf, e.Team
	case "StaffUpdated":
		sf, err := get(st.staff, "staff member", e.Staff)
		if err != nil {
			return err
		}
		applyStaffFields(sf, e)
	case "StaffRemoved":
		sf, err := get(st.staff, "staff member", e.Staff)
		if err != nil {
			return err
		}
		t := st.team(st.staffTeam[sf.ID])
		t.Staff = remove(t.Staff, sf)
		delete(st.staff, sf.ID)

	case "OfficialAssigned":
		if st.officials[e.Official] != nil {
			return fmt.Errorf("official %q already exists", e.Official)
		}
		o := &Official{ID: e.Official}
		applyOfficialFields(o, e)
		s.Officials = append(s.Officials, o)
		st.officials[o.ID] = o
	case "OfficialUpdated":
		o, err := get(st.officials, "official", e.Official)
		if err != nil {
			return err
		}
		applyOfficialFields(o, e)
	case "OfficialRemoved":
		o, err := get(st.officials, "official", e.Official)
		if err != nil {
			return err
		}
		s.Officials = remove(s.Officials, o)
		delete(st.officials, o.ID)

	// --- game flow -----------------------------------------------------
	case "PeriodStarted":
		if st.periods[e.Period] != nil {
			return fmt.Errorf("period %q already exists", e.Period)
		}
		var n int
		if err := json.Unmarshal(e.Number, &n); err != nil {
			return err
		}
		p := &Period{ID: e.Period, Number: n, Start: e.T, Jams: []*Jam{}}
		s.Periods = append(s.Periods, p)
		st.periods[p.ID] = p
		s.State = "running"
	case "JamUpcoming":
		if st.upcoming != nil {
			return fmt.Errorf("jam %q is already upcoming", st.upcoming.ID)
		}
		var n int
		if err := json.Unmarshal(e.Number, &n); err != nil {
			return err
		}
		j, err := st.newJam(nil, e.Jam, n)
		if err != nil {
			return err
		}
		st.upcoming, s.UpcomingJam = j, j
	case "JamStarted":
		p, err := get(st.periods, "period", e.Period)
		if err != nil {
			return err
		}
		var n int
		if err := json.Unmarshal(e.Number, &n); err != nil {
			return err
		}
		var j *Jam
		switch {
		case st.upcoming != nil && st.upcoming.ID == e.Jam:
			j, st.upcoming, s.UpcomingJam = st.upcoming, nil, nil
			j.Number = n
			st.jamPeriod[j.ID] = p
		case st.upcoming != nil:
			return fmt.Errorf("jam %q is upcoming, so that is the one to start, not %q", st.upcoming.ID, e.Jam)
		default:
			if j, err = st.newJam(p, e.Jam, n); err != nil {
				return err
			}
		}
		j.Overtime, j.InjuryContinuation = e.Overtime, e.InjuryContinuation
		j.Start, j.PeriodClockStart = e.T, clone(e.PC)
		for _, tj := range j.Teams {
			tj.Trips[0].JamClockStart = ptr(int64(0))
		}
		p.Jams = append(p.Jams, j)
	case "JamEnded":
		j, err := get(st.jams, "jam", e.Jam)
		if err != nil {
			return err
		}
		j.End, j.Duration, j.PeriodClockEnd, j.EndReason, j.EndDetail = e.T, clone(e.JC), clone(e.PC), e.Reason, e.Detail
		for _, tj := range j.Teams {
			if last := tj.Trips[len(tj.Trips)-1]; last.JamClockEnd == nil {
				last.JamClockEnd = clone(e.JC)
			}
		}

	case "TimeoutStarted", "TimeoutInserted":
		if st.timeouts[e.Timeout] != nil {
			return fmt.Errorf("timeout %q already exists", e.Timeout)
		}
		p, err := st.startedJamPeriod(e.AfterJam)
		if err != nil {
			return err
		}
		to := &Timeout{ID: e.Timeout, Owner: deref(e.Owner), Review: deref(e.Review), AsTimeout: deref(e.AsTimeout), AfterJam: e.AfterJam}
		if e.Type == "TimeoutStarted" {
			to.Start, to.PeriodClock = e.T, clone(e.PC)
		} else {
			to.Duration = clone(e.Duration)
		}
		p.Timeouts = append(p.Timeouts, to)
		st.timeouts[to.ID], st.toPeriod[to.ID] = to, p
	case "TimeoutUpdated":
		to, err := get(st.timeouts, "timeout", e.Timeout)
		if err != nil {
			return err
		}
		if e.Owner != nil {
			to.Owner = *e.Owner
		}
		if e.Review != nil {
			to.Review = *e.Review
		}
		if e.AsTimeout != nil {
			to.AsTimeout = *e.AsTimeout
		}
	case "TimeoutEnded":
		to, err := get(st.timeouts, "timeout", e.Timeout)
		if err != nil {
			return err
		}
		to.End, to.Duration, to.PeriodClockEnd = e.T, clone(e.Duration), clone(e.PC)
	case "OfficialReviewResolved":
		to, err := get(st.timeouts, "timeout", e.Timeout)
		if err != nil {
			return err
		}
		if !to.Review {
			st.warnf("seq %d: review result recorded on timeout %q, which is not an official review", e.Seq, to.ID)
		}
		to.Retained, to.ReviewRequest, to.ReviewResult = clone(e.Retained), deref(e.Request), deref(e.Result)
		if to.AsTimeout && deref(e.Retained) {
			st.warnf("seq %d: review %q was used as a timeout, so it can't be retained", e.Seq, to.ID)
		}
	case "TimeoutDeleted":
		to, err := get(st.timeouts, "timeout", e.Timeout)
		if err != nil {
			return err
		}
		p := st.toPeriod[to.ID]
		p.Timeouts = remove(p.Timeouts, to)
		delete(st.timeouts, to.ID)

	case "PeriodEnded":
		p, err := get(st.periods, "period", e.Period)
		if err != nil {
			return err
		}
		p.End, p.Duration = e.T, clone(e.PC)

	case "JamUpdated":
		j, err := get(st.jams, "jam", e.Jam)
		if err != nil {
			return err
		}
		if j.End == "" {
			return fmt.Errorf("jam %q hasn't ended; there's nothing to correct yet", j.ID)
		}
		if e.Reason != "" {
			j.EndReason = e.Reason
		}
		if e.Detail != "" {
			j.EndDetail = e.Detail
		}
		if e.Duration != nil {
			j.Duration = clone(e.Duration)
		}
	case "HintDismissed":
		s.DismissedHints = append(s.DismissedHints, &DismissedHint{Check: e.Check,
			Target: HintTarget{Kind: e.Target.Kind, ID: e.Target.ID}, Reason: e.Reason, Time: e.T})

	case "SuddenScoringSet":
		p, err := get(st.periods, "period", e.Period)
		if err != nil {
			return err
		}
		p.SuddenScoring = e.Value

	case "OvertimeStarted", "LineupStarted", "ClockSet", "Undone", "Checkpoint":
		// Clock state and undo bookkeeping aren't part of the summary.

	case "ScoreDeclaredOfficial":
		r := st.result()
		r.OfficialScore, r.DeclaredAt, r.HR, r.HNSO = e.Score, e.T, e.HR, e.HNSO
		s.State = "finished"
	case "GameEndedEarly":
		if s.State == "finished" {
			return fmt.Errorf("the game has already finished")
		}
		period := 0
		if n := len(s.Periods); n > 0 {
			period = s.Periods[n-1].Number
		}
		st.result().EndedEarly = &Ended{Outcome: e.Outcome, ForfeitingTeam: e.ForfeitingTeam, Reason: e.Reason,
			Time: e.T, Period: period, PeriodClock: deref(e.PC)}
		s.State = map[string]string{"cancelled": "cancelled", "forfeit": "forfeited"}[e.Outcome]

	// --- scoring -------------------------------------------------------
	case "JamFlagSet":
		tj, err := st.teamJam(e.Jam, e.Team)
		if err != nil {
			return err
		}
		switch e.Flag {
		case "lead":
			tj.Lead = e.Value
		case "lost":
			tj.Lost = e.Value
		case "calloff":
			tj.Calloff = e.Value
		case "noPivot":
			tj.NoPivot = e.Value
		}
	case "TripStarted":
		tj, err := st.teamJam(e.Jam, e.Team)
		if err != nil {
			return err
		}
		if st.trips[e.Trip.V] != nil {
			return fmt.Errorf("trip %q already exists", e.Trip.V)
		}
		if last := tj.Trips[len(tj.Trips)-1]; last.JamClockEnd == nil {
			last.JamClockEnd = clone(e.JC)
		}
		afterSP := tj.Trips[len(tj.Trips)-1].AfterStarPass
		if e.AfterStarPass != nil {
			afterSP = *e.AfterStarPass
		}
		st.addTrip(tj, len(tj.Trips), &Trip{ID: e.Trip.V, AfterStarPass: afterSP, JamClockStart: clone(e.JC)})
	case "TripPointsSet":
		t, err := get(st.trips, "trip", e.Trip.V)
		if err != nil {
			return err
		}
		t.Points = e.Points
		if e.JC != nil && t.JamClockEnd == nil {
			t.JamClockEnd = clone(e.JC) // entering points ends the trip
		}
	case "TripInserted":
		before, err := get(st.trips, "trip", e.Before)
		if err != nil {
			return err
		}
		if st.trips[e.Trip.V] != nil {
			return fmt.Errorf("trip %q already exists", e.Trip.V)
		}
		tj := st.tripTJ[before.ID]
		st.addTrip(tj, slices.Index(tj.Trips, before), &Trip{ID: e.Trip.V, AfterStarPass: before.AfterStarPass})
	case "TripRemoved":
		t, err := get(st.trips, "trip", e.Trip.V)
		if err != nil {
			return err
		}
		tj := st.tripTJ[t.ID]
		if len(tj.Trips) == 1 {
			return fmt.Errorf("cannot remove trip %q: it is the only trip of the team jam", t.ID)
		}
		if tj.StarPassTrip == t.ID {
			st.warnf("seq %d: removed trip %q, which was the star pass trip; star pass cleared", e.Seq, t.ID)
			tj.StarPassTrip = ""
		}
		tj.Trips = remove(tj.Trips, t)
		delete(st.trips, t.ID)
		delete(st.tripTJ, t.ID)
	case "StarPassSet":
		tj, err := st.teamJam(e.Jam, e.Team)
		if err != nil {
			return err
		}
		tj.StarPassTrip = ""
		if !e.Trip.Null {
			t, err := get(st.trips, "trip", e.Trip.V)
			if err != nil {
				return err
			}
			if st.tripTJ[t.ID] != tj {
				return fmt.Errorf("trip %q belongs to a different team jam", t.ID)
			}
			tj.StarPassTrip = t.ID
		}
		// The star pass trip and all later trips are after the star pass.
		after := false
		for _, t := range tj.Trips {
			if t.ID == tj.StarPassTrip {
				after = true
			}
			t.AfterStarPass = after
		}
	case "OsOffsetSet":
		tj, err := st.teamJam(e.Jam, e.Team)
		if err != nil {
			return err
		}
		tj.OsOffset, tj.OsOffsetReason = e.Offset, e.Reason

	case "ScoreAdjustmentRecorded":
		if st.adjustments[e.Adjustment] != nil {
			return fmt.Errorf("score adjustment %q already exists", e.Adjustment)
		}
		if _, err := get(st.jams, "jam", e.Jam); err != nil {
			return err
		}
		a := &ScoreAdjustment{ID: e.Adjustment, Team: e.Team, Amount: e.Amount, Jam: e.Jam,
			DuringJam: e.DuringJam, LastTwoMinutes: e.LastTwoMinutes}
		s.ScoreAdjustments = append(s.ScoreAdjustments, a)
		st.adjustments[a.ID] = a
	case "ScoreAdjustmentApplied":
		a, err := get(st.adjustments, "score adjustment", e.Adjustment)
		if err != nil {
			return err
		}
		t, err := get(st.trips, "trip", e.Trip.V)
		if err != nil {
			return err
		}
		if st.tripTJ[t.ID].Team != a.Team {
			return fmt.Errorf("adjustment %q is for team %s, trip %q is not", a.ID, a.Team, t.ID)
		}
		if t.Points+a.Amount < 0 {
			return fmt.Errorf("applying %d to trip %q would make its points negative", a.Amount, t.ID)
		}
		t.Points += a.Amount
		s.ScoreAdjustments = remove(s.ScoreAdjustments, a)
		delete(st.adjustments, a.ID)
	case "ScoreAdjustmentDiscarded":
		a, err := get(st.adjustments, "score adjustment", e.Adjustment)
		if err != nil {
			return err
		}
		s.ScoreAdjustments = remove(s.ScoreAdjustments, a)
		delete(st.adjustments, a.ID)

	// --- lineups -------------------------------------------------------
	case "FieldingSet":
		tj, err := st.teamJam(e.Jam, e.Team)
		if err != nil {
			return err
		}
		f := tj.Lineup[e.Position]
		if f == nil {
			f = &Fielding{}
		}
		if e.Skater.Set {
			f.Skater, f.BoxTrip = "", ""
			if !e.Skater.Null {
				if _, err := st.skater(e.Skater.V); err != nil {
					return err
				}
				if st.skaterTeam[e.Skater.V] != e.Team {
					return fmt.Errorf("skater %q is not on team %s", e.Skater.V, e.Team)
				}
				f.Skater = e.Skater.V
			}
		}
		if e.NotFielded != nil {
			f.NotFielded = *e.NotFielded
		}
		if e.SitFor3 != nil {
			f.SitFor3 = *e.SitFor3
		}
		if e.BoxTrip != "" {
			if st.boxTrips[e.BoxTrip] == nil {
				return fmt.Errorf("no box trip %q", e.BoxTrip)
			}
			f.BoxTrip = e.BoxTrip
		}
		st.setFielding(tj, e.Position, f)
	case "LineupCopied":
		from, err := st.teamJam(e.FromJam, e.Team)
		if err != nil {
			return err
		}
		to, err := st.teamJam(e.ToJam, e.Team)
		if err != nil {
			return err
		}
		for pos, f := range from.Lineup {
			g := to.Lineup[pos]
			if g == nil {
				g = &Fielding{}
			}
			g.Skater, g.NotFielded = f.Skater, f.NotFielded
			st.setFielding(to, pos, g)
		}

	// --- penalties -----------------------------------------------------
	case "PenaltyIssued":
		if st.penalties[e.Penalty] != nil {
			return fmt.Errorf("penalty %q already exists", e.Penalty)
		}
		if e.Code != nil && *e.Code == "FO" {
			return fmt.Errorf("foul-outs are not recorded as penalties; they follow from the penalty count")
		}
		person, team := e.Skater.V, st.skaterTeam[e.Skater.V]
		if e.Staff != "" {
			if _, err := get(st.staff, "staff member", e.Staff); err != nil {
				return err
			}
			person, team = e.Staff, st.staffTeam[e.Staff]
		} else if _, err := st.skater(e.Skater.V); err != nil {
			return err
		}
		if _, err := get(st.jams, "jam", e.Jam); err != nil {
			return err
		}
		slot, err := st.penaltySlot(person, e.Slot)
		if err != nil {
			return err
		}
		p := &Penalty{ID: e.Penalty, Team: team, Skater: e.Skater.V, Staff: e.Staff, Slot: slot,
			Code: deref(e.Code), Jam: e.Jam, Time: e.T, PeriodClock: clone(e.PC), JamClock: clone(e.JC),
			CalledBy: deref(e.CalledBy), CallingPosition: deref(e.CallingPosition)}
		if p.CalledBy != "" && st.officials[p.CalledBy] == nil {
			return fmt.Errorf("unknown official %q", p.CalledBy)
		}
		s.Penalties = append(s.Penalties, p)
		st.penalties[p.ID] = p
	case "PenaltyUpdated":
		p, err := get(st.penalties, "penalty", e.Penalty)
		if err != nil {
			return err
		}
		if e.Code != nil {
			p.Code = *e.Code
		}
		if e.Jam != "" {
			if _, err := get(st.jams, "jam", e.Jam); err != nil {
				return err
			}
			p.Jam = e.Jam
		}
		if e.CalledBy != nil {
			p.CalledBy = *e.CalledBy
		}
		if e.CallingPosition != nil {
			p.CallingPosition = *e.CallingPosition
		}
	case "PenaltyRemoved":
		p, err := get(st.penalties, "penalty", e.Penalty)
		if err != nil {
			return err
		}
		s.Penalties = remove(s.Penalties, p)
		delete(st.penalties, p.ID)
		for _, bt := range s.BoxTrips {
			bt.Penalties = slices.DeleteFunc(bt.Penalties, func(id string) bool { return id == p.ID })
		}
		s.Expulsions = slices.DeleteFunc(s.Expulsions, func(x *Expulsion) bool { return x.Penalty == p.ID })
	case "PenaltyForceServed":
		p, err := get(st.penalties, "penalty", e.Penalty)
		if err != nil {
			return err
		}
		p.ForceServed = e.Value
	case "ExpulsionRecorded":
		if _, err := get(st.penalties, "penalty", e.Penalty); err != nil {
			return err
		}
		var info string
		if err := json.Unmarshal(e.Info, &info); err != nil {
			return err
		}
		x := &Expulsion{Penalty: e.Penalty, Info: info, ExtraInfo: e.ExtraInfo, Suspension: e.Suspension}
		s.Expulsions = slices.DeleteFunc(s.Expulsions, func(old *Expulsion) bool { return old.Penalty == x.Penalty })
		s.Expulsions = append(s.Expulsions, x)
	case "SuspensionServed":
		r := st.result()
		r.SuspensionsServed = append(r.SuspensionsServed, e.Skater.V)

	// --- penalty box ---------------------------------------------------
	case "BoxTripStarted":
		if st.boxTrips[e.BoxTrip] != nil {
			return fmt.Errorf("box trip %q already exists", e.BoxTrip)
		}
		if _, err := get(st.jams, "jam", e.Jam); err != nil {
			return err
		}
		bt := &BoxTrip{ID: e.BoxTrip, Team: e.Team, Jammer: e.Jammer, Start: BoxPoint{
			Jam: e.Jam, BetweenJams: e.BetweenJams, AfterStarPass: deref(e.AfterStarPass),
			Position: e.Position, Time: e.T, JamClock: clone(e.JC)}}
		if e.Skater.Set && !e.Skater.Null {
			if _, err := st.skater(e.Skater.V); err != nil {
				return err
			}
			bt.Skater = e.Skater.V
		}
		s.BoxTrips = append(s.BoxTrips, bt)
		st.boxTrips[bt.ID] = bt
	case "BoxTripEnded":
		bt, err := get(st.boxTrips, "box trip", e.BoxTrip)
		if err != nil {
			return err
		}
		if bt.End != nil {
			return fmt.Errorf("box trip %q has already ended", bt.ID)
		}
		bt.End = &BoxPoint{Jam: e.Jam, BetweenJams: e.BetweenJams, AfterStarPass: deref(e.AfterStarPass),
			Time: e.T, JamClock: clone(e.JC)}
		bt.Duration = clone(e.Duration)
	case "BoxTripMoved":
		bt, err := get(st.boxTrips, "box trip", e.BoxTrip)
		if err != nil {
			return err
		}
		// A moved point no longer matches its original time and clock, so those are dropped.
		if e.Start != nil {
			bt.Start = BoxPoint{Jam: e.Start.Jam, BetweenJams: e.Start.BetweenJams, AfterStarPass: e.Start.AfterStarPass,
				Position: bt.Start.Position}
		}
		if e.End != nil {
			if bt.End == nil {
				return fmt.Errorf("box trip %q has not ended, so its end can't be moved", bt.ID)
			}
			bt.End = &BoxPoint{Jam: e.End.Jam, BetweenJams: e.End.BetweenJams, AfterStarPass: e.End.AfterStarPass}
		}
	case "BoxTripReopened":
		bt, err := get(st.boxTrips, "box trip", e.BoxTrip)
		if err != nil {
			return err
		}
		bt.End, bt.Duration = nil, nil
	case "BoxTripDeleted":
		bt, err := get(st.boxTrips, "box trip", e.BoxTrip)
		if err != nil {
			return err
		}
		s.BoxTrips = remove(s.BoxTrips, bt)
		delete(st.boxTrips, bt.ID)
	case "BoxTripPenaltyLinked":
		bt, err := get(st.boxTrips, "box trip", e.BoxTrip)
		if err != nil {
			return err
		}
		if _, err := get(st.penalties, "penalty", e.Penalty); err != nil {
			return err
		}
		bt.Penalties = slices.DeleteFunc(bt.Penalties, func(id string) bool { return id == e.Penalty })
		if e.Value {
			bt.Penalties = append(bt.Penalties, e.Penalty)
		}
	case "BoxTripShortened":
		bt, err := get(st.boxTrips, "box trip", e.BoxTrip)
		if err != nil {
			return err
		}
		bt.Shortened = e.Amount
		if e.Time != nil {
			bt.ShortenedTime = *e.Time
		}

	// --- structure edits -----------------------------------------------
	case "JamInserted":
		before, err := get(st.jams, "jam", e.Before)
		if err != nil {
			return err
		}
		p, err := st.startedJamPeriod(before.ID)
		if err != nil {
			return err
		}
		base := p.Jams[0].Number
		j, err := st.newJam(p, e.Jam, 0)
		if err != nil {
			return err
		}
		p.Jams = slices.Insert(p.Jams, slices.Index(p.Jams, before), j)
		renumber(p, base)
	case "JamDeleted":
		j, err := get(st.jams, "jam", e.Jam)
		if err != nil {
			return err
		}
		if j == st.upcoming {
			st.dropJam(j)
			st.upcoming, s.UpcomingJam = nil, nil
			return nil
		}
		p := st.jamPeriod[j.ID]
		base := p.Jams[0].Number
		st.dropJam(j)
		p.Jams = remove(p.Jams, j)
		renumber(p, base)
	case "PeriodDeleted":
		p, err := get(st.periods, "period", e.Period)
		if err != nil {
			return err
		}
		for _, j := range p.Jams {
			st.dropJam(j)
		}
		for _, to := range p.Timeouts {
			delete(st.timeouts, to.ID)
		}
		s.Periods = remove(s.Periods, p)
		delete(st.periods, p.ID)
		for i, q := range s.Periods {
			q.Number = i + 1
		}

	// --- annotations ---------------------------------------------------
	case "Annotated":
		return st.annotate(e.Target.Kind, e.Target.ID, e.Text)

	default:
		return fmt.Errorf("no replay handler for event type %q", e.Type)
	}
	return nil
}

// --- helpers -------------------------------------------------------------

// skaterUsed says what in the game refers to a skater, or "".
func (st *state) skaterUsed(id string) string {
	for _, p := range st.s.Penalties {
		if p.Skater == id {
			return "a penalty"
		}
	}
	for _, bt := range st.s.BoxTrips {
		if bt.Skater == id {
			return "a box trip"
		}
	}
	for _, j := range st.jams {
		for _, tj := range j.Teams {
			for _, f := range tj.Lineup {
				if f != nil && f.Skater == id {
					return "a lineup spot"
				}
			}
		}
	}
	return ""
}

func (st *state) team(id string) *Team {
	if id == "2" {
		return st.s.Teams[1]
	}
	return st.s.Teams[0]
}

func (st *state) skater(id string) (*Skater, error) { return get(st.skaters, "skater", id) }

func (st *state) teamJam(jamID, team string) (*TeamJam, error) {
	j, err := get(st.jams, "jam", jamID)
	if err != nil {
		return nil, err
	}
	if team == "2" {
		return j.Teams[1], nil
	}
	return j.Teams[0], nil
}

// startedJamPeriod returns the period of a jam that has started; the upcoming
// jam isn't in a period yet.
func (st *state) startedJamPeriod(jamID string) (*Period, error) {
	if _, err := get(st.jams, "jam", jamID); err != nil {
		return nil, err
	}
	p := st.jamPeriod[jamID]
	if p == nil {
		return nil, fmt.Errorf("jam %q is upcoming and hasn't started", jamID)
	}
	return p, nil
}

func (st *state) result() *Result {
	if st.s.Result == nil {
		st.s.Result = &Result{}
	}
	return st.s.Result
}

// newJam creates a jam with each team's initial trip (<jam>/<team>/t1).
func (st *state) newJam(p *Period, id string, number int) (*Jam, error) {
	if st.jams[id] != nil {
		return nil, fmt.Errorf("jam %q already exists", id)
	}
	j := &Jam{ID: id, Number: number}
	for _, team := range []string{"1", "2"} {
		tj := &TeamJam{Team: team}
		j.Teams = append(j.Teams, tj)
		st.addTrip(tj, 0, &Trip{ID: id + "/" + team + "/t1"})
	}
	st.jams[id], st.jamPeriod[id] = j, p
	return j, nil
}

func (st *state) dropJam(j *Jam) {
	for _, tj := range j.Teams {
		for _, t := range tj.Trips {
			delete(st.trips, t.ID)
			delete(st.tripTJ, t.ID)
		}
	}
	delete(st.jams, j.ID)
	delete(st.jamPeriod, j.ID)
	for _, p := range st.s.Periods {
		for _, to := range p.Timeouts {
			if to.AfterJam == j.ID {
				st.warnf("timeout %q follows deleted jam %q", to.ID, j.ID)
			}
		}
	}
	for _, p := range st.s.Penalties {
		if p.Jam == j.ID {
			st.warnf("penalty %q is on deleted jam %q; move it with PenaltyUpdated", p.ID, j.ID)
		}
	}
	for _, bt := range st.s.BoxTrips {
		if bt.Start.Jam == j.ID || (bt.End != nil && bt.End.Jam == j.ID) {
			st.warnf("box trip %q starts or ends in deleted jam %q; move it with BoxTripMoved", bt.ID, j.ID)
		}
	}
}

// renumber numbers a period's jams consecutively from base. Jam numbers
// follow order; see README "Stable IDs".
func renumber(p *Period, base int) {
	if base < 1 {
		base = 1
	}
	for i, j := range p.Jams {
		j.Number = base + i
	}
}

func (st *state) addTrip(tj *TeamJam, at int, t *Trip) {
	tj.Trips = slices.Insert(tj.Trips, at, t)
	st.trips[t.ID], st.tripTJ[t.ID] = t, tj
}

func (st *state) setFielding(tj *TeamJam, pos string, f *Fielding) {
	if f.empty() {
		delete(tj.Lineup, pos)
		if len(tj.Lineup) == 0 {
			tj.Lineup = nil
		}
		return
	}
	if tj.Lineup == nil {
		tj.Lineup = map[string]*Fielding{}
	}
	tj.Lineup[pos] = f
}

// penaltySlot returns the requested slot if free, otherwise the first free
// 1-9. person is a skater or staff id.
func (st *state) penaltySlot(person string, want *int) (int, error) {
	used := map[int]bool{}
	for _, p := range st.s.Penalties {
		if p.Skater == person || p.Staff == person {
			used[p.Slot] = true
		}
	}
	if want != nil {
		if used[*want] {
			return 0, fmt.Errorf("%q already has a penalty in slot %d", person, *want)
		}
		return *want, nil
	}
	for n := 1; n <= 9; n++ {
		if !used[n] {
			return n, nil
		}
	}
	return 0, fmt.Errorf("%q has no free penalty slot", person)
}

func (st *state) annotate(kind, id, text string) error {
	switch kind {
	case "trip":
		t, err := get(st.trips, "trip", id)
		if err != nil {
			return err
		}
		t.Annotation = text
	case "penalty":
		p, err := get(st.penalties, "penalty", id)
		if err != nil {
			return err
		}
		p.Annotation = text
	case "boxTrip":
		bt, err := get(st.boxTrips, "box trip", id)
		if err != nil {
			return err
		}
		bt.Annotation = text
	case "teamJamSk", "teamJamLt":
		parts := strings.Split(id, "/")
		if len(parts) != 2 {
			return fmt.Errorf("annotation target %q: want <jam>/<team>", id)
		}
		tj, err := st.teamJam(parts[0], parts[1])
		if err != nil {
			return err
		}
		if kind == "teamJamSk" {
			tj.SkAnnotation = text
		} else {
			tj.LtAnnotation = text
		}
	case "fielding":
		parts := strings.Split(id, "/")
		if len(parts) != 3 {
			return fmt.Errorf("annotation target %q: want <jam>/<team>/<position>", id)
		}
		pos := parts[2]
		if !slices.Contains([]string{"Jammer", "Pivot", "Blocker1", "Blocker2", "Blocker3"}, pos) {
			return fmt.Errorf("annotation target %q: unknown position %q", id, pos)
		}
		tj, err := st.teamJam(parts[0], parts[1])
		if err != nil {
			return err
		}
		f := tj.Lineup[pos]
		if f == nil {
			f = &Fielding{}
		}
		f.Annotation = text
		st.setFielding(tj, pos, f)
	default:
		return fmt.Errorf("unknown annotation target kind %q", kind)
	}
	return nil
}

// checkOfficialScore compares a declared official score with the jam scores.
func (st *state) checkOfficialScore() {
	r := st.s.Result
	if r == nil || r.OfficialScore == nil {
		return
	}
	var got [2]int
	for _, p := range st.s.Periods {
		for _, j := range p.Jams {
			for i, tj := range j.Teams {
				got[i] += tj.OsOffset
				for _, t := range tj.Trips {
					got[i] += t.Points
				}
			}
		}
	}
	if got[0] != r.OfficialScore.Team1 || got[1] != r.OfficialScore.Team2 {
		st.warnf("official score %d-%d does not match the jam scores %d-%d",
			r.OfficialScore.Team1, r.OfficialScore.Team2, got[0], got[1])
	}
}

func (st *state) updateInfo(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var info map[string]*string
	if err := json.Unmarshal(raw, &info); err != nil {
		return err
	}
	for k, v := range info {
		if v == nil {
			delete(st.s.Info, k)
			continue
		}
		if st.s.Info == nil {
			st.s.Info = map[string]string{}
		}
		st.s.Info[k] = *v
	}
	if len(st.s.Info) == 0 {
		st.s.Info = nil
	}
	return nil
}

func setOverrides(dst, src map[string]any) {
	for k, v := range src {
		if v == nil {
			delete(dst, k)
		} else {
			dst[k] = v
		}
	}
}

func applyTeamFields(t *Team, e *Event) {
	setStr(&t.Name, e.Name)
	setStr(&t.PreparedTeam, e.PreparedTeam)
	setStr(&t.FullName, e.FullName)
	setStr(&t.League, e.League)
	setStr(&t.TeamName, e.TeamName)
	setStr(&t.Initials, e.Initials)
	setStr(&t.NameCue, e.NameCue)
	setStr(&t.NameCuePron, e.NameCuePron)
	setStr(&t.UniformColor, e.UniformColor)
	setStr(&t.Logo, e.Logo)
	if e.Colors != nil {
		t.Colors = e.Colors
	}
	if e.AlternateNames != nil {
		t.AlternateNames = e.AlternateNames
	}
}

func applySkaterFields(sk *Skater, e *Event) error {
	if len(e.Number) > 0 {
		if err := json.Unmarshal(e.Number, &sk.Number); err != nil {
			return fmt.Errorf("skater number: %w", err)
		}
	}
	setStr(&sk.Name, e.Name)
	setStr(&sk.Pronouns, e.Pronouns)
	setStr(&sk.Pronunciation, e.Pronunciation)
	setStr(&sk.WUID, e.WUID)
	setStr(&sk.Flags, e.Flags)
	if e.Status != nil {
		sk.Status = *e.Status
		if sk.Status == "eligible" {
			sk.Status = ""
		}
	}
	return nil
}

func applyStaffFields(sf *Staff, e *Event) {
	setStr(&sf.Name, e.Name)
	setStr(&sf.Role, e.Role)
	setStr(&sf.Flags, e.Flags)
}

func applyOfficialFields(o *Official, e *Event) {
	setStr(&o.Name, e.Name)
	setStr(&o.Role, e.Role)
	setStr(&o.League, e.League)
	setStr(&o.Cert, e.Cert)
	setStr(&o.P1Team, e.P1Team)
	if e.Swap != nil {
		o.Swap = *e.Swap
	}
}

func get[T any](m map[string]*T, what, id string) (*T, error) {
	if v := m[id]; v != nil {
		return v, nil
	}
	return nil, fmt.Errorf("unknown %s %q", what, id)
}

func remove[T comparable](s []T, v T) []T {
	return slices.DeleteFunc(s, func(x T) bool { return x == v })
}

func setStr(dst *string, src *string) {
	if src != nil {
		*dst = *src
	}
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func clone[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func ptr[T any](v T) *T { return &v }
