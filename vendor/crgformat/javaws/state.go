// Package javaws speaks the Java scoreboard's WebSocket protocol, so tools
// written for it (Jamstats, CRG-Stats, overlays) keep working with the new
// server (docs/engine.md "Java WebSocket compatibility"). This file builds the
// Java scoreboard's flat key tree for a game; ws.go serves it.
package javaws

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"crgformat/derive"
	"crgformat/engine"
	"crgformat/legacy"
	"crgformat/replay"
)

// Version is what ScoreBoard.Version(release) says: the Java version whose
// keys this reproduces. Jamstats reads it to pick its parser.
const Version = "v2025.10"

// Keys is a flat key tree: "ScoreBoard.Game(id).Team(1).Score" -> 143.
type Keys map[string]any

// GameKeys builds the keys of one game, under "ScoreBoard.Game(<id>).". st is
// the engine's state (clocks and phase), or nil for a game that isn't running.
func GameKeys(s *replay.Summary, st *engine.State) Keys {
	b := &builder{k: make(Keys, 16384), s: s, st: st, g: derive.New(s), gid: s.ID}
	b.p = "ScoreBoard.Game(" + b.gid + ")."
	b.build()
	return b.k
}

// CurrentGame adds the ScoreBoard.CurrentGame mirror of a game's keys, which
// is what clients read, and the keys outside the game.
func CurrentGame(k Keys, gid string) Keys {
	out := make(Keys, 2*len(k)+2)
	prefix := "ScoreBoard.Game(" + gid + ")."
	for key, v := range k {
		out[key] = v
		if strings.HasPrefix(key, prefix) {
			out["ScoreBoard.CurrentGame."+key[len(prefix):]] = v
		}
	}
	out["ScoreBoard.CurrentGame.Game"] = gid
	out["ScoreBoard.Version(release)"] = Version
	return out
}

type builder struct {
	k   Keys
	s   *replay.Summary
	st  *engine.State
	g   *derive.Game
	gid string
	p   string // key prefix of the game

	syms    map[string]*derive.Symbols
	order   []string       // jam ids in game order, with Jam(0) first and the upcoming jam last
	orderAt map[string]int // jam id -> index in order
	jams    map[string]*replay.Jam

	btFields map[string][]string // box trip -> the fielding ids it spans
	fieldBTs map[string][]string // fielding id -> box trips in it
	skFields map[string][]string // skater -> fielding ids, in game order
	openTrip map[string]*replay.BoxTrip
	penCodes map[string]string // penalty id -> code
	jamNum   map[string]int    // jam id -> number
	jamPer   map[string]int    // jam id -> period number
	penNum   map[string]int    // penalty id -> Penalty(n) of its skater
	tripsOf  map[string][]string
}

func (b *builder) set(path string, v any) { b.k[b.p+path] = v }

func ms(t string) int64 {
	if t == "" {
		return 0
	}
	x, err := time.Parse(time.RFC3339Nano, t)
	if err != nil {
		return 0
	}
	return x.UnixMilli()
}

func val(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func (b *builder) teamID(team string) string { return b.gid + "_" + team }

func (b *builder) build() {
	s := b.s
	b.syms = b.g.BoxSymbols()
	b.jamNum, b.jamPer = map[string]int{}, map[string]int{}
	b.order, b.orderAt = []string{b.jam0()}, map[string]int{}
	for _, p := range s.Periods {
		for _, j := range p.Jams {
			b.jamNum[j.ID], b.jamPer[j.ID] = j.Number, p.Number
			b.order = append(b.order, j.ID)
		}
	}
	if s.UpcomingJam != nil {
		b.order = append(b.order, s.UpcomingJam.ID)
	}
	for i, id := range b.order {
		b.orderAt[id] = i
	}
	b.links()
	b.game()
	b.clocks()
	b.rules()
	b.officials()
	b.placeholder()
	totals := b.periods()
	b.upcoming()
	for i, t := range s.Teams {
		b.team(i, t, totals)
	}
}

// --- the game ---------------------------------------------------------------------

func (b *builder) game() {
	s := b.s
	state := map[string]string{"prepared": "Prepared", "running": "Running", "finished": "Finished",
		"cancelled": "Finished", "forfeited": "Finished"}[s.State]
	if state == "" {
		state = "Prepared"
	}
	b.set("Id", b.gid)
	b.set("Name", b.name(state))
	b.set("State", state)
	b.set("Readonly", false)
	b.set("NameFormat", "%d %G %1 vs. %2 (%s: %S)")
	b.set("Filename", "")
	b.set("AbortReason", "")
	if r := s.Result; r != nil && r.EndedEarly != nil {
		b.set("AbortReason", r.EndedEarly.Reason)
	}
	for k, v := range s.Info {
		b.set("EventInfo("+k+")", v)
	}
	if hr, hnso := derive.Heads(s); hr != "" || hnso != "" {
		b.set("HR", hr)
		b.set("HNSO", hnso)
	}
	b.set("OfficialScore", s.Result != nil && s.Result.OfficialScore != nil)
	b.set("RulesetName", s.Ruleset.Name)
	phase := ""
	if b.st != nil {
		phase = b.st.Phase
	}
	b.set("InJam", phase == engine.PhaseJam)
	b.set("InOvertime", phase == engine.PhaseOvertime)
	b.set("InPeriod", phase == engine.PhaseJam || phase == engine.PhaseLineup || phase == engine.PhaseTimeout || phase == engine.PhaseOvertime)
	inSS := false
	if n := len(s.Periods); n > 0 {
		inSS = s.Periods[n-1].SuddenScoring
	}
	b.set("InSuddenScoring", inSS)
	b.set("InjuryContinuationUpcoming", false)
	b.set("ClockDuringFinalScore", false)
	b.set("InhibitFinalScore", false)
	b.set("ExportBlockedBy", "")
	b.set("UpdateInProgress", false)
	b.set("SuspensionsServed", "")
	b.set("JsonExists", false)
	b.set("StatsbookExists", false)
	cur := "noTimeout"
	owner, review, asTO := "", false, false
	if b.st != nil && b.st.Timeout != "" {
		cur = b.st.Timeout
		for _, p := range s.Periods {
			for _, to := range p.Timeouts {
				if to.ID == cur {
					owner, review, asTO = b.owner(to.Owner), to.Review, to.AsTimeout
				}
			}
		}
	}
	b.set("CurrentTimeout", cur)
	b.set("TimeoutOwner", owner)
	b.set("OfficialReview", review)
	b.set("ReviewIsTo", asTO)
	n := 0
	if len(s.Periods) > 0 {
		last := s.Periods[len(s.Periods)-1]
		b.set("CurrentPeriod", last.ID)
		n = last.Number
	} else {
		b.set("CurrentPeriod", b.period0()) // Java looks it up by this id
	}
	b.set("CurrentPeriodNumber", n)
	b.set("NoMoreJam", false)
	labels := map[string]string{"Start": "Start Jam", "Stop": "Lineup", "Timeout": "Timeout", "Undo": "No Action", "Replaced": "---"}
	switch phase {
	case engine.PhaseJam:
		labels["Start"], labels["Stop"] = "---", "Stop Jam"
	case engine.PhaseTimeout:
		labels["Stop"], labels["Timeout"] = "End Timeout", "New Timeout"
	}
	if b.st != nil && b.st.Undo != nil {
		labels["Undo"] = "Un-" + b.st.Undo.Label
	}
	for k, v := range labels {
		b.set("Label("+k+")", v)
	}
	for code, name := range penaltyCodes {
		b.set("PenaltyCode("+code+")", name)
	}
}

// name is the game's name the way Java's default name format ends it: with
// the state and score, "(Finished: 143 - 142)".
func (b *builder) name(state string) string {
	var score [2]int
	for _, p := range b.s.Periods {
		for _, j := range p.Jams {
			for i, tj := range j.Teams {
				score[i] += tj.OsOffset
				for _, tr := range tj.Trips {
					score[i] += tr.Points
				}
			}
		}
	}
	base := b.s.Name
	if i := strings.LastIndex(base, " ("); i > 0 && strings.HasSuffix(base, ")") {
		base = base[:i]
	}
	return fmt.Sprintf("%s (%s: %d - %d)", base, state, score[0], score[1])
}

// owner is a timeout owner as Java writes it: the team's id, "O" or "".
func (b *builder) owner(o string) string {
	if o == "1" || o == "2" {
		return b.teamID(o)
	}
	return o
}

// penaltyCodes are the WFTDA codes as the Java scoreboard lists them
// (penalties.json of v2025).
var penaltyCodes = map[string]string{
	"A": "High Block", "B": "Back Block", "C": "Illegal Contact,Illegal Assist,OOP Block,Early/Late Hit",
	"D": "Direction,Stop Block", "E": "Leg Block", "F": "Forearm", "G": "Misconduct,Insubordination",
	"H": "Head Block", "I": "Illegal Procedure,Star Pass Violation,Pass Interference", "L": "Low Block",
	"M": "Multiplayer", "N": "Interference,Delay Of Game", "P": "Illegal Position,Destruction,Skating OOB,Failure to...",
	"X": "Cut,Illegal Re-Entry", "?": "Unknown",
}

func (b *builder) clocks() {
	names := map[string]string{"period": "Period", "jam": "Jam", "lineup": "Lineup", "timeout": "Timeout", "intermission": "Intermission"}
	numbers := map[string]int{}
	if b.st != nil {
		numbers["period"], numbers["jam"] = b.st.Period, b.st.Jam
		for _, c := range b.st.Clocks {
			name := names[c.Name]
			down := c.Name == "period" || c.Name == "jam" || c.Name == "intermission"
			p := "Clock(" + name + ")."
			b.set(p+"Id", b.gid+"_"+name)
			b.set(p+"Name", name)
			b.set(p+"Number", numbers[c.Name])
			b.set(p+"Direction", down)
			b.set(p+"MaximumTime", c.Max)
			b.set(p+"Running", c.Running)
			b.set(p+"Readonly", false)
			// Time is what the clock shows; InvertedTime the other way round.
			if down {
				b.set(p+"Time", c.Remaining)
				b.set(p+"InvertedTime", c.Elapsed)
			} else {
				b.set(p+"Time", c.Elapsed)
				b.set(p+"InvertedTime", c.Remaining)
			}
		}
		// The jam timer's five-second call (Java Game.FiveSeconds): the
		// standard view and the overlay show FiveIndicator instead of the
		// lineup clock.
		f := b.st.Five
		b.set("FiveSeconds", f != nil && f.On)
		b.set("AutoFive", f != nil && f.Auto)
		ind := "5"
		if f != nil && f.On && f.StartsIn > 0 {
			ind = strconv.FormatInt(min(5, (f.StartsIn+999)/1000), 10)
		}
		b.set("FiveIndicator", ind)
	}
}

var ruleDefaults = legacy.RuleDefaults(Version)

func (b *builder) rules() {
	for name, v := range ruleDefaults {
		if _, ok := b.s.Ruleset.Overrides[name]; ok {
			v = derive.Rule(b.s, name)
		}
		b.set("Rule("+name+")", v)
	}
}

func (b *builder) officials() {
	for _, o := range b.s.Officials {
		kind := "Nso"
		if strings.Contains(o.Role, "Referee") {
			kind = "Ref"
		}
		p := kind + "(" + o.ID + ")."
		b.set(p+"Id", o.ID)
		b.set(p+"Name", o.Name)
		b.set(p+"Role", o.Role)
		b.set(p+"League", o.League)
		b.set(p+"Cert", o.Cert)
		b.set(p+"Swap", o.Swap)
		b.set(p+"Readonly", false)
		if o.P1Team != "" {
			b.set(p+"P1Team", b.teamID(o.P1Team))
		}
	}
}

// --- periods, jams, team jams ------------------------------------------------------

type teamTotals struct {
	score     [2]int // running total
	penalties [2]int
}

func (b *builder) periods() *teamTotals {
	s := b.s
	tot := &teamTotals{}
	pens := map[string][]*replay.Penalty{} // jam id -> penalties
	for _, p := range s.Penalties {
		pens[p.Jam] = append(pens[p.Jam], p)
	}
	b.penNum = b.numberPenalties()
	for pi, p := range s.Periods {
		pp := "Period(" + strconv.Itoa(p.Number) + ")."
		b.set(pp+"Id", p.ID)
		b.set(pp+"Number", p.Number)
		b.set(pp+"Readonly", false)
		b.set(pp+"SuddenScoring", p.SuddenScoring)
		b.set(pp+"Running", p.End == "" && b.st != nil && b.st.Phase != engine.PhaseIntermission)
		b.set(pp+"WalltimeStart", ms(p.Start))
		b.set(pp+"WalltimeEnd", ms(p.End))
		if p.Start != "" && p.End != "" {
			b.set(pp+"Duration", ms(p.End)-ms(p.Start))
		} else {
			b.set(pp+"Duration", int64(0))
		}
		if pi > 0 {
			b.set(pp+"Previous", s.Periods[pi-1].ID)
		} else {
			b.set(pp+"Previous", b.period0())
		}
		b.noTimeout(pp)
		if pi+1 < len(s.Periods) {
			b.set(pp+"Next", s.Periods[pi+1].ID)
		}
		var points, penCount [2]int
		for ji, j := range p.Jams {
			jp := pp + "Jam(" + strconv.Itoa(j.Number) + ")."
			b.jam(jp, p, ji, j)
			for _, pen := range pens[j.ID] {
				b.set(jp+"Penalty("+pen.ID+")", pen.ID)
				if ti := derive.TeamIndex(pen.Team); ti >= 0 {
					penCount[ti]++
				}
			}
			for ti, tj := range j.Teams {
				score := tj.OsOffset
				for _, tr := range tj.Trips {
					score += tr.Points
				}
				last := tot.score[ti]
				tot.score[ti] += score
				points[ti] += score
				b.teamJam(jp+"TeamJam("+tj.Team+").", p, ji, j, ti, tj, last, tot.score[ti])
			}
			for _, to := range p.Timeouts {
				if to.AfterJam == j.ID {
					b.set(jp+"TimeoutsAfter("+to.ID+")", to.ID)
				}
			}
		}
		if n := len(p.Jams); n > 0 {
			b.set(pp+"CurrentJam", p.Jams[n-1].ID)
			b.set(pp+"CurrentJamNumber", p.Jams[n-1].Number)
		} else {
			b.set(pp+"CurrentJamNumber", 0)
		}
		b.set(pp+"Team1Points", points[0])
		b.set(pp+"Team2Points", points[1])
		b.set(pp+"Team1PenaltyCount", penCount[0])
		b.set(pp+"Team2PenaltyCount", penCount[1])
		for _, to := range p.Timeouts {
			b.timeout(pp+"Timeout("+to.ID+").", p, to)
		}
	}
	return tot
}

// prevNextJam are a jam's neighbours in game order, across periods, as in
// Java: the first jam follows the Jam(0) placeholder, the last is followed
// by the upcoming jam.
func (b *builder) prevNextJam(p *replay.Period, ji int) (prev, next string) {
	i := b.orderAt[p.Jams[ji].ID]
	if i > 0 {
		prev = b.order[i-1]
	}
	if i+1 < len(b.order) {
		next = b.order[i+1]
	}
	return
}

// links works out Java's links between skaters, lineups and box trips: a
// box trip spans the lineup spots of its skater from the jam it started in
// to the one it ended in (or the upcoming jam while it's open).
func (b *builder) links() {
	s := b.s
	b.jams = map[string]*replay.Jam{}
	for _, p := range s.Periods {
		for _, j := range p.Jams {
			b.jams[j.ID] = j
		}
	}
	if s.UpcomingJam != nil {
		b.jams[s.UpcomingJam.ID] = s.UpcomingJam
	}
	b.skFields, b.btFields, b.fieldBTs = map[string][]string{}, map[string][]string{}, map[string][]string{}
	b.openTrip, b.penCodes = map[string]*replay.BoxTrip{}, map[string]string{}
	fieldOf := func(j *replay.Jam, team, skater string) string {
		for _, tj := range j.Teams {
			if tj.Team != team {
				continue
			}
			for _, pos := range derive.Positions {
				if f := tj.Lineup[pos]; f != nil && f.Skater == skater {
					return j.ID + "_" + team + "_" + pos
				}
			}
		}
		return ""
	}
	for _, id := range b.order {
		j := b.jams[id]
		if j == nil {
			continue
		}
		for _, tj := range j.Teams {
			for _, pos := range derive.Positions {
				if f := tj.Lineup[pos]; f != nil && f.Skater != "" {
					b.skFields[f.Skater] = append(b.skFields[f.Skater], j.ID+"_"+tj.Team+"_"+pos)
				}
			}
		}
	}
	for _, p := range s.Penalties {
		b.penCodes[p.ID] = p.Code
	}
	for _, bt := range s.BoxTrips {
		if bt.End == nil && bt.Skater != "" {
			b.openTrip[bt.Skater] = bt
		}
		from, ok := b.orderAt[bt.Start.Jam]
		if !ok {
			continue
		}
		to := len(b.order) - 1
		if bt.End != nil {
			if e, ok := b.orderAt[bt.End.Jam]; ok {
				to = e
			}
		}
		if bt.Skater == "" && bt.Start.Position != "" {
			// A trip without a skater (the number wasn't recorded) is still
			// on its lineup spot.
			fid := bt.Start.Jam + "_" + bt.Team + "_" + bt.Start.Position
			b.btFields[bt.ID] = append(b.btFields[bt.ID], fid)
			b.fieldBTs[fid] = append(b.fieldBTs[fid], bt.ID)
			continue
		}
		for i := from; i <= to; i++ {
			if j := b.jams[b.order[i]]; j != nil {
				if fid := fieldOf(j, bt.Team, bt.Skater); fid != "" {
					b.btFields[bt.ID] = append(b.btFields[bt.ID], fid)
					b.fieldBTs[fid] = append(b.fieldBTs[fid], bt.ID)
				}
			}
		}
	}
}

func (b *builder) jam0() string    { return b.gid + "_jam0" }
func (b *builder) period0() string { return b.gid + "_period0" }

// placeholder writes Period(0) and its Jam(0), which Java always has: the
// state before the first jam.
func (b *builder) placeholder() {
	pp, jp := "Period(0).", "Period(0).Jam(0)."
	b.jamNum[b.jam0()], b.jamPer[b.jam0()] = 0, 0
	b.set(pp+"Id", b.period0())
	b.set(pp+"Number", 0)
	b.set(pp+"CurrentJam", b.jam0())
	b.set(pp+"CurrentJamNumber", 0)
	b.set(pp+"Duration", int64(0))
	b.set(pp+"LocalTimeStart", "")
	b.set(pp+"Readonly", false)
	b.set(pp+"Running", false)
	b.set(pp+"SuddenScoring", false)
	for _, k := range []string{"Team1PenaltyCount", "Team1Points", "Team2PenaltyCount", "Team2Points"} {
		b.set(pp+k, 0)
	}
	b.set(pp+"WalltimeStart", int64(0))
	b.set(pp+"WalltimeEnd", int64(0))
	if len(b.s.Periods) > 0 {
		b.set(pp+"Next", b.s.Periods[0].ID)
	}
	b.noTimeout(pp)
	j0 := &replay.Jam{ID: b.jam0(), Teams: []*replay.TeamJam{
		{Team: "1", Trips: []*replay.Trip{{ID: b.jam0() + "/1/t1"}}},
		{Team: "2", Trips: []*replay.Trip{{ID: b.jam0() + "/2/t1"}}},
	}}
	p0 := &replay.Period{ID: b.period0(), Jams: []*replay.Jam{j0}}
	b.jam(jp, p0, 0, j0)
	for _, tj := range j0.Teams {
		b.teamJam(jp+"TeamJam("+tj.Team+").", p0, 0, j0, 0, tj, 0, 0)
	}
}

// noTimeout is Java's placeholder timeout, in every period.
func (b *builder) noTimeout(pp string) {
	tp := pp + "Timeout(noTimeout)."
	b.set(tp+"Id", "noTimeout")
	for _, k := range []string{"Duration", "PeriodClockElapsedEnd", "PeriodClockElapsedStart", "PeriodClockEnd", "WalltimeEnd", "WalltimeStart"} {
		b.set(tp+k, int64(0))
	}
	b.set(tp+"PrecedingJamNumber", 0)
	for _, k := range []string{"OrRequest", "OrResult", "Owner"} {
		b.set(tp+k, "")
	}
	for _, k := range []string{"RetainedReview", "Review", "Running"} {
		b.set(tp+k, false)
	}
	b.set(tp+"Readonly", true)
}

func (b *builder) jam(jp string, p *replay.Period, ji int, j *replay.Jam) {
	b.set(jp+"Id", j.ID)
	b.set(jp+"Number", j.Number)
	b.set(jp+"PeriodNumber", p.Number)
	b.set(jp+"Readonly", false)
	b.set(jp+"Overtime", j.Overtime)
	b.set(jp+"InjuryContinuation", j.InjuryContinuation)
	b.set(jp+"Duration", val(j.Duration))
	b.set(jp+"PeriodClockElapsedStart", val(j.PeriodClockStart))
	b.set(jp+"PeriodClockElapsedEnd", val(j.PeriodClockEnd))
	if j.PeriodClockEnd != nil { // Java: 0 until the jam has ended
		b.set(jp+"PeriodClockDisplayEnd", b.g.ClockRule("Period.Duration")-*j.PeriodClockEnd)
	} else {
		b.set(jp+"PeriodClockDisplayEnd", int64(0))
	}
	b.set(jp+"WalltimeStart", ms(j.Start))
	b.set(jp+"WalltimeEnd", ms(j.End))
	b.set(jp+"StarPass", j.Teams[0].StarPassTrip != "" || j.Teams[1].StarPassTrip != "")
	prev, next := b.prevNextJam(p, ji)
	if prev != "" {
		b.set(jp+"Previous", prev)
	}
	if next != "" {
		b.set(jp+"Next", next)
	}
}

func (b *builder) teamJam(tp string, p *replay.Period, ji int, j *replay.Jam, ti int, tj *replay.TeamJam, last, total int) {
	id := j.ID + "_" + tj.Team
	b.set(tp+"Id", id)
	b.set(tp+"Number", j.Number)
	b.set(tp+"Readonly", false)
	prev, next := b.prevNextJam(p, ji)
	if prev != "" {
		b.set(tp+"Previous", prev+"_"+tj.Team)
	}
	if next != "" {
		b.set(tp+"Next", next+"_"+tj.Team)
	}
	jamScore, afterSP := 0, 0
	for _, tr := range tj.Trips {
		jamScore += tr.Points
		if tr.AfterStarPass {
			afterSP += tr.Points
		}
	}
	b.set(tp+"JamScore", jamScore)
	b.set(tp+"AfterSPScore", afterSP)
	b.set(tp+"LastScore", last)
	b.set(tp+"TotalScore", total)
	b.set(tp+"OsOffset", tj.OsOffset)
	b.set(tp+"OsOffsetReason", tj.OsOffsetReason)
	b.set(tp+"Lead", tj.Lead)
	b.set(tp+"Lost", tj.Lost)
	b.set(tp+"Calloff", tj.Calloff)
	b.set(tp+"DisplayLead", tj.Lead && !tj.Lost)
	b.set(tp+"Injury", j.EndReason == "injury")
	b.set(tp+"NoPivot", tj.NoPivot)
	b.set(tp+"StarPass", tj.StarPassTrip != "")
	b.set(tp+"NoInitial", len(tj.Trips) <= 1)
	if tj.StarPassTrip != "" {
		b.set(tp+"StarPassTrip", tj.StarPassTrip)
	}
	if n := len(tj.Trips); n > 0 {
		b.set(tp+"CurrentTrip", tj.Trips[n-1].ID)
		b.set(tp+"CurrentTripNumber", n)
	}
	running := j.End == ""
	for i, tr := range tj.Trips {
		sp := tp + "ScoringTrip(" + strconv.Itoa(i+1) + ")."
		b.set(sp+"Id", tr.ID)
		b.set(sp+"Number", i+1)
		b.set(sp+"Score", tr.Points)
		b.set(sp+"AfterSP", tr.AfterStarPass)
		b.set(sp+"Annotation", tr.Annotation)
		b.set(sp+"Readonly", false)
		b.set(sp+"Current", running && i == len(tj.Trips)-1)
		b.set(sp+"JamClockStart", val(tr.JamClockStart))
		b.set(sp+"JamClockEnd", val(tr.JamClockEnd))
		b.set(sp+"Duration", val(tr.JamClockEnd)-val(tr.JamClockStart))
		if i > 0 {
			b.set(sp+"Previous", tj.Trips[i-1].ID)
		}
		if i+1 < len(tj.Trips) {
			b.set(sp+"Next", tj.Trips[i+1].ID)
		}
	}
	for _, pos := range derive.Positions {
		b.fielding(tp+"Fielding("+pos+").", j, tj, pos, prev, next)
	}
}

func (b *builder) fielding(fp string, j *replay.Jam, tj *replay.TeamJam, pos, prev, next string) {
	f := tj.Lineup[pos]
	if f == nil {
		f = &replay.Fielding{}
	}
	b.set(fp+"Id", j.ID+"_"+tj.Team+"_"+pos)
	b.set(fp+"Position", b.gid+"_"+tj.Team+"_"+pos)
	b.set(fp+"Number", j.Number)
	b.set(fp+"Readonly", false)
	if prev != "" {
		b.set(fp+"Previous", prev+"_"+tj.Team+"_"+pos)
	}
	if next != "" {
		b.set(fp+"Next", next+"_"+tj.Team+"_"+pos)
	}
	b.set(fp+"NotFielded", f.NotFielded)
	b.set(fp+"SitFor3", f.SitFor3)
	b.set(fp+"Annotation", f.Annotation)
	if f.Skater != "" {
		b.set(fp+"Skater", f.Skater)
		b.set(fp+"SkaterNumber", b.g.Number(f.Skater))
	} else if f.NotFielded {
		b.set(fp+"SkaterNumber", "n/a")
	} else {
		b.set(fp+"SkaterNumber", "?")
	}
	sy := b.syms[derive.FieldingKey(j.ID, tj.Team, pos)]
	if sy == nil {
		sy = &derive.Symbols{}
	}
	b.set(fp+"BoxTripSymbols", strings.Join(sy.Jam, " "))
	b.set(fp+"BoxTripSymbolsBeforeSP", strings.Join(sy.BeforeSP, " "))
	b.set(fp+"BoxTripSymbolsAfterSP", strings.Join(sy.AfterSP, " "))
	fid := j.ID + "_" + tj.Team + "_" + pos
	inBox := false
	for _, id := range b.fieldBTs[fid] {
		b.set(fp+"BoxTrip("+id+")", id)
		b.set(fp+"CurrentBoxTrip", id)
		// Still in the box when this jam is over: the trip goes on into a
		// later lineup spot, or hasn't ended.
		if fs := b.btFields[id]; fs[len(fs)-1] != fid || b.btOpen(id) {
			inBox = true
		}
	}
	b.set(fp+"PenaltyBox", inBox)
}

func (b *builder) btOpen(id string) bool {
	for _, bt := range b.s.BoxTrips {
		if bt.ID == id {
			return bt.End == nil
		}
	}
	return false
}

func (b *builder) timeout(tp string, p *replay.Period, to *replay.Timeout) {
	b.set(tp+"Id", to.ID)
	b.set(tp+"Owner", b.owner(to.Owner))
	b.set(tp+"Review", to.Review)
	b.set(tp+"RetainedReview", to.Retained != nil && *to.Retained)
	b.set(tp+"OrRequest", to.ReviewRequest)
	b.set(tp+"OrResult", to.ReviewResult)
	b.set(tp+"Readonly", false)
	running := b.st != nil && b.st.Timeout == to.ID
	b.set(tp+"Running", running)
	b.set(tp+"Duration", val(to.Duration))
	b.set(tp+"PeriodClockElapsedStart", val(to.PeriodClock))
	b.set(tp+"PeriodClockElapsedEnd", val(to.PeriodClockEnd))
	b.set(tp+"PeriodClockEnd", b.g.ClockRule("Period.Duration")-val(to.PeriodClockEnd))
	b.set(tp+"WalltimeStart", ms(to.Start))
	b.set(tp+"WalltimeEnd", ms(to.End))
	b.set(tp+"PrecedingJam", to.AfterJam)
	b.set(tp+"PrecedingJamNumber", b.jamNum[to.AfterJam])
}

func (b *builder) upcoming() {
	u := b.s.UpcomingJam
	if u == nil {
		return
	}
	n := u.Number
	b.set("UpcomingJam", u.ID)
	b.set("UpcomingJamNumber", n)
	jp := "Jam(" + strconv.Itoa(n) + ")."
	b.set(jp+"Id", u.ID)
	b.set(jp+"Number", n)
	b.set(jp+"Readonly", false)
	b.set(jp+"Overtime", false)
	b.set(jp+"InjuryContinuation", false)
	b.set(jp+"StarPass", false)
	for _, k := range []string{"Duration", "PeriodClockDisplayEnd", "PeriodClockElapsedEnd", "PeriodClockElapsedStart", "WalltimeEnd", "WalltimeStart"} {
		b.set(jp+k, int64(0))
	}
	pn := 0
	prev := b.jam0() // before the first jam: Period(0)'s Jam(0), as in Java
	if len(b.s.Periods) > 0 {
		last := b.s.Periods[len(b.s.Periods)-1]
		pn = last.Number
		if len(last.Jams) > 0 {
			prev = last.Jams[len(last.Jams)-1].ID
		}
	}
	b.set(jp+"Previous", prev)
	b.set(jp+"PeriodNumber", pn)
}

// --- teams, skaters, penalties, box trips ---------------------------------------

// numberPenalties gives each penalty its Penalty(n) under its skater: 1-9
// in the order of the slots, as in Java.
func (b *builder) numberPenalties() map[string]int {
	out := map[string]int{}
	per := map[string][]*replay.Penalty{}
	for _, p := range b.s.Penalties {
		if p.Skater != "" {
			per[p.Skater] = append(per[p.Skater], p)
		}
	}
	for _, list := range per {
		sort.SliceStable(list, func(i, j int) bool { return list[i].Slot < list[j].Slot })
		for i, p := range list {
			out[p.ID] = i + 1
		}
	}
	return out
}

func (b *builder) team(ti int, t *replay.Team, tot *teamTotals) {
	s := b.s
	tp := "Team(" + t.Team + ")."
	b.set(tp+"Id", b.teamID(t.Team))
	b.set(tp+"Name", t.Name)
	b.set(tp+"FullName", t.FullName)
	b.set(tp+"LeagueName", t.League)
	b.set(tp+"TeamName", t.TeamName)
	b.set(tp+"Initials", t.Initials)
	b.set(tp+"UniformColor", t.UniformColor)
	b.set(tp+"Logo", t.Logo)
	b.set(tp+"FileName", t.FullName)
	b.set(tp+"Readonly", false)
	b.set(tp+"PreparedTeam", t.PreparedTeam)
	b.set(tp+"PreparedTeamConnected", false)
	b.set(tp+"ActiveScoreAdjustmentAmount", 0)
	b.set(tp+"FieldingAdvancePending", false)
	for k, v := range t.AlternateNames {
		b.set(tp+"AlternateName("+k+")", v)
	}
	for k, v := range t.Colors {
		b.set(tp+"Color("+k+")", v)
	}
	b.set(tp+"Score", derive.Scores(s)[ti])

	// The team jam shown: the running jam, or the last one.
	var cur *replay.TeamJam
	var curJam *replay.Jam
	for _, p := range s.Periods {
		for _, j := range p.Jams {
			cur, curJam = j.Teams[ti], j
		}
	}
	jamScore, tripScore := 0, 0
	if cur != nil {
		for _, tr := range cur.Trips {
			jamScore += tr.Points
		}
		if n := len(cur.Trips); n > 0 {
			tripScore = cur.Trips[n-1].Points
			b.set(tp+"CurrentTrip", cur.Trips[n-1].ID)
		}
		b.set(tp+"RunningOrEndedTeamJam", curJam.ID+"_"+t.Team)
		b.set(tp+"Lead", cur.Lead)
		b.set(tp+"Lost", cur.Lost)
		b.set(tp+"Calloff", cur.Calloff)
		b.set(tp+"DisplayLead", cur.Lead && !cur.Lost)
		b.set(tp+"StarPass", cur.StarPassTrip != "")
		b.set(tp+"NoInitial", len(cur.Trips) <= 1)
		b.set(tp+"Injury", curJam.EndReason == "injury")
		if cur.StarPassTrip != "" {
			b.set(tp+"StarPassTrip", cur.StarPassTrip)
		}
	} else {
		// No jam yet: Period(0)'s Jam(0) is the one that ended (Java's
		// prepared games), and its initial trip the current trip.
		tj := b.jam0() + "_" + t.Team
		b.set(tp+"RunningOrEndedTeamJam", tj)
		if trip, ok := b.k[b.p+"Period(0).Jam(0).TeamJam("+t.Team+").CurrentTrip"]; ok {
			b.set(tp+"CurrentTrip", trip)
		}
		for _, k := range []string{"Lead", "Lost", "Calloff", "DisplayLead", "StarPass", "Injury"} {
			b.set(tp+k, false)
		}
		b.set(tp+"NoInitial", true)
	}
	b.set(tp+"JamScore", jamScore)
	b.set(tp+"TripScore", tripScore)
	b.set(tp+"LastScore", tot.score[ti]-jamScore)
	upcoming := curJam
	if s.UpcomingJam != nil && (b.st == nil || b.st.Phase != engine.PhaseJam) {
		upcoming = s.UpcomingJam
	}
	if upcoming != nil {
		b.set(tp+"RunningOrUpcomingTeamJam", upcoming.ID+"_"+t.Team)
		b.set(tp+"NoPivot", upcoming.Teams[ti].NoPivot)
	}

	// Timeouts and reviews.
	retained := false
	lastPeriod := ""
	if len(s.Periods) > 0 {
		lastPeriod = s.Periods[len(s.Periods)-1].ID
	}
	inTO, inOR := false, false
	for _, p := range s.Periods {
		for _, to := range p.Timeouts {
			if to.Owner != t.Team {
				continue
			}
			b.set(tp+"TimeOut("+to.ID+")", to.ID)
			running := b.st != nil && b.st.Timeout == to.ID
			if !to.Review || to.AsTimeout {
				inTO = inTO || running
			} else {
				b.set(tp+"LastReview", to.ID)
				ret := to.Retained != nil && *to.Retained
				if p.ID == lastPeriod {
					retained = ret
				}
				inOR = inOR || running
			}
		}
	}
	toLeft, orLeft := derive.TimeoutsLeft(s, t.Team)
	b.set(tp+"Timeouts", toLeft)
	b.set(tp+"OfficialReviews", orLeft)
	b.set(tp+"RetainedOfficialReview", retained)
	b.set(tp+"InTimeout", inTO)
	b.set(tp+"InOfficialReview", inOR)

	// Skaters and their penalties.
	fouled := b.g.FoulOuts()
	total := 0
	penCount := map[string]int{}
	for _, sk := range t.Skaters {
		sp := tp + "Skater(" + sk.ID + ")."
		b.set(sp+"Id", sk.ID)
		b.set(sp+"Name", sk.Name)
		b.set(sp+"RosterNumber", sk.Number)
		b.set(sp+"Pronouns", sk.Pronouns)
		b.set(sp+"Flags", sk.Flags)
		b.set(sp+"Readonly", false)
		base := "Bench"
		if strings.EqualFold(sk.Status, "notInGame") || strings.Contains(" "+sk.Flags+" ", " ALT ") {
			base = "NotInGame"
		}
		role := base
		if upcoming != nil && base != "NotInGame" {
			for pos, f := range upcoming.Teams[ti].Lineup {
				if f != nil && f.Skater == sk.ID {
					role = map[string]string{"Jammer": "Jammer", "Pivot": "Pivot"}[pos]
					if role == "" {
						role = "Blocker"
					}
				}
			}
		}
		b.set(sp+"BaseRole", base)
		b.set(sp+"Role", role)
		for _, fid := range b.skFields[sk.ID] {
			b.set(sp+"Fielding("+fid+")", fid)
		}
		cur := b.current(upcoming, ti, sk.ID)
		// Color: the position they skated last (the SBO greys roster
		// buttons by it).
		cur["Color"] = b.played(sk.ID)
		for k, v := range cur {
			b.set(sp+k, v)
		}
		count := 0
		var mine []*replay.Penalty
		for _, p := range s.Penalties {
			if p.Skater == sk.ID {
				mine = append(mine, p)
			}
		}
		sort.SliceStable(mine, func(i, j int) bool { return b.penNum[mine[i].ID] < b.penNum[mine[j].ID] })
		for i, p := range mine {
			n := b.penNum[p.ID]
			pp := sp + "Penalty(" + strconv.Itoa(n) + ")."
			b.penalty(pp, p, n)
			if i > 0 {
				b.set(pp+"Previous", mine[i-1].ID)
			}
			if i+1 < len(mine) {
				b.set(pp+"Next", mine[i+1].ID)
			}
			count++
		}
		penCount[sk.ID] = count
		// Penalty(0) is Java's foul-out or expulsion entry: code FO, or the
		// expelled penalty's code, in the jam of the penalty that caused it.
		zero := func(p *replay.Penalty, code string) {
			pp := sp + "Penalty(0)."
			b.penalty(pp, p, 0)
			b.set(pp+"Id", p.ID+"_0")
			b.set(pp+"Code", code)
			b.set(pp+"ForceServed", true)
			b.set(pp+"Served", true)
			b.set(pp+"Serving", false)
			b.set(pp+"Next", nil)
			delete(b.k, b.p+pp+"Next")
		}
		if fo := fouled[sk.ID]; fo != nil {
			zero(fo, "FO")
		}
		for _, e := range s.Expulsions {
			if p := b.g.Penalties[e.Penalty]; p != nil && p.Skater == sk.ID {
				zero(p, p.Code)
			}
		}
		b.set(sp+"PenaltyCount", count)
		total += count
	}
	b.set(tp+"TotalPenalties", total)

	for _, bt := range s.BoxTrips {
		if bt.Team == t.Team {
			b.boxTrip(tp+"BoxTrip("+bt.ID+").", bt, penCount[bt.Skater])
		}
	}
	for _, pos := range derive.Positions {
		pp := tp + "Position(" + pos + ")."
		b.set(pp+"Id", b.gid+"_"+t.Team+"_"+pos)
		b.set(pp+"Readonly", false)
		name, number, flags, note := "", "", "", ""
		cur := Keys{"PenaltyBox": false, "PenaltyCount": 0, "CurrentPenalties": "", "CurrentBoxSymbols": "",
			"HasUnserved": false, "ExtraPenaltyTime": 0, "PenaltyDetails": ""}
		if upcoming != nil {
			if f := upcoming.Teams[ti].Lineup[pos]; f != nil {
				note = f.Annotation
				if sk := b.g.SkaterByID[f.Skater]; sk != nil {
					name, number, flags = sk.Name, sk.Number, sk.Flags
					b.set(pp+"Skater", sk.ID)
					cur = b.current(upcoming, ti, sk.ID)
					cur["PenaltyCount"] = b.penaltiesOf(sk.ID)
					delete(cur, "CurrentFielding")
					delete(cur, "Position")
					delete(cur, "Color")
				}
			}
			b.set(pp+"CurrentFielding", upcoming.ID+"_"+t.Team+"_"+pos)
		}
		for k, v := range cur {
			b.set(pp+k, v)
		}
		b.set(pp+"Name", name)
		b.set(pp+"RosterNumber", number)
		b.set(pp+"Flags", flags)
		b.set(pp+"Annotation", note)
	}
}

// current is what Java says about a skater now: their spot in the running
// or upcoming jam, whether they're in the box and for what.
func (b *builder) current(upcoming *replay.Jam, ti int, skater string) Keys {
	k := Keys{"PenaltyBox": false, "CurrentPenalties": "", "CurrentBoxSymbols": "", "HasUnserved": false,
		"ExtraPenaltyTime": 0, "PenaltyDetails": "", "Color": ""}
	if upcoming != nil {
		for _, pos := range derive.Positions {
			if f := upcoming.Teams[ti].Lineup[pos]; f != nil && f.Skater == skater {
				k["CurrentFielding"] = upcoming.ID + "_" + upcoming.Teams[ti].Team + "_" + pos
				k["Position"] = b.gid + "_" + upcoming.Teams[ti].Team + "_" + pos
				k["Color"] = map[string]string{"Jammer": "Jammer", "Pivot": "Pivot"}[pos]
				if k["Color"] == "" {
					k["Color"] = "Blocker"
				}
				if sy := b.syms[derive.FieldingKey(upcoming.ID, upcoming.Teams[ti].Team, pos)]; sy != nil {
					k["CurrentBoxSymbols"] = strings.Join(sy.Jam, " ")
				}
			}
		}
	}
	var codes []string
	if bt := b.openTrip[skater]; bt != nil {
		k["PenaltyBox"] = true
		for _, id := range bt.Penalties {
			codes = append(codes, b.penCodes[id])
		}
	}
	linked := map[string]bool{}
	for _, bt := range b.s.BoxTrips {
		for _, id := range bt.Penalties {
			linked[id] = true
		}
	}
	for _, p := range b.s.Penalties {
		if p.Skater == skater && !linked[p.ID] && !p.ForceServed {
			k["HasUnserved"] = true
			codes = append(codes, p.Code)
		}
	}
	k["CurrentPenalties"] = strings.Join(codes, " ")
	return k
}

// played is Java's Skater.Color: "Jammer" if the skater has jammed in the
// current period, else "Pivot" if they have pivoted, else "".
func (b *builder) played(skater string) string {
	if len(b.s.Periods) == 0 {
		return ""
	}
	cur := b.s.Periods[len(b.s.Periods)-1].Number
	out := ""
	for _, fid := range b.skFields[skater] {
		jam := fid[:strings.Index(fid, "_")]
		if b.jamPer[jam] != cur {
			continue
		}
		switch fid[strings.LastIndex(fid, "_")+1:] {
		case "Jammer":
			return "Jammer"
		case "Pivot":
			out = "Pivot"
		}
	}
	return out
}

func (b *builder) penaltiesOf(skater string) int {
	n := 0
	for _, p := range b.s.Penalties {
		if p.Skater == skater {
			n++
		}
	}
	return n
}

func (b *builder) penalty(pp string, p *replay.Penalty, n int) {
	b.set(pp+"Id", p.ID)
	b.set(pp+"Number", n)
	b.set(pp+"Code", p.Code)
	b.set(pp+"Jam", p.Jam)
	b.set(pp+"JamNumber", b.jamNum[p.Jam])
	b.set(pp+"PeriodNumber", b.jamPer[p.Jam])
	b.set(pp+"ForceServed", p.ForceServed)
	b.set(pp+"Readonly", false)
	b.set(pp+"Time", ms(p.Time))
	// Java: Served once a box trip has been assigned (even while still
	// in the box), Serving while that trip is open.
	served, serving := p.ForceServed, false
	for _, bt := range b.s.BoxTrips {
		for _, id := range bt.Penalties {
			if id == p.ID {
				b.set(pp+"BoxTrip", bt.ID)
				served = true
				serving = bt.End == nil
			}
		}
	}
	b.set(pp+"Served", served)
	b.set(pp+"Serving", serving)
}

func (b *builder) boxTrip(bp string, bt *replay.BoxTrip, skaterPenalties int) {
	b.set(bp+"Id", bt.ID)
	b.set(bp+"Readonly", false)
	b.set(bp+"IsCurrent", bt.End == nil)
	b.set(bp+"CurrentSkater", bt.Skater)
	b.set(bp+"RosterNumber", b.g.Number(bt.Skater))
	b.set(bp+"StartJamNumber", b.jamNum[bt.Start.Jam])
	b.set(bp+"StartBetweenJams", bt.Start.BetweenJams)
	b.set(bp+"StartAfterSP", bt.Start.AfterStarPass)
	b.set(bp+"JamClockStart", val(bt.Start.JamClock))
	b.set(bp+"WalltimeStart", ms(bt.Start.Time))
	if bt.End != nil {
		b.set(bp+"EndJamNumber", b.jamNum[bt.End.Jam])
		b.set(bp+"EndBetweenJams", bt.End.BetweenJams)
		b.set(bp+"EndAfterSP", bt.End.AfterStarPass)
		b.set(bp+"JamClockEnd", val(bt.End.JamClock))
		b.set(bp+"WalltimeEnd", ms(bt.End.Time))
	} else {
		b.set(bp+"EndJamNumber", 0)
		b.set(bp+"EndBetweenJams", false)
		b.set(bp+"EndAfterSP", false)
	}
	b.set(bp+"Duration", val(bt.Duration))
	b.set(bp+"Shortened", bt.Shortened)
	b.set(bp+"TimingStopped", false)
	var codes []string
	for _, id := range bt.Penalties {
		b.set(bp+"Penalty("+id+")", id)
		if p := b.g.Penalties[id]; p != nil {
			codes = append(codes, p.Code)
		}
	}
	b.set(bp+"PenaltyCodes", strings.Join(codes, " "))
	var details []string
	for _, id := range bt.Penalties {
		details = append(details, bt.Skater+"_"+strconv.Itoa(b.penNum[id]))
	}
	b.set(bp+"PenaltyDetails", strings.Join(details, ","))
	if fs := b.btFields[bt.ID]; len(fs) > 0 {
		for _, fid := range fs {
			b.set(bp+"Fielding("+fid+")", fid)
		}
		b.set(bp+"StartFielding", fs[0])
		b.set(bp+"CurrentFielding", fs[len(fs)-1])
		if bt.End != nil {
			b.set(bp+"EndFielding", fs[len(fs)-1])
		}
	}
	b.set(bp+"TotalPenalties", skaterPenalties) // Java: the skater's penalties
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// ClockKeys are just the clock keys of a game, under both Game(id) and
// CurrentGame, for the tenth-of-a-second updates.
func ClockKeys(gid string, st *engine.State) Keys {
	b := &builder{k: Keys{}, st: st, gid: gid, p: "ScoreBoard.Game(" + gid + ")."}
	b.clocks()
	out := Keys{}
	for k, v := range b.k {
		out[k] = v
		out["ScoreBoard.CurrentGame."+strings.TrimPrefix(k, b.p)] = v
	}
	return out
}

// SettingsKeys are the Java scoreboard's settings (ScoreBoard.Settings), with
// Java's defaults (SettingsImpl.java). The display pages written for Java
// read them: which view to show, the intermission texts, and so on. The two
// automation settings are the server's.
func SettingsKeys(st engine.Settings) Keys {
	autoEndJam, autoEndTTO, usePBT := st.AutoEndJam, st.AutoEndTeamTimeouts, st.UsePBT
	auto := map[string]string{engine.AutoStartJam: "Jam", engine.AutoStartTimeout: "Timeout"}
	set := map[string]string{
		"Overlay.Interactive.Clock": "true", "Overlay.Interactive.ClockAfterTimeout": "Lineup", "Overlay.Interactive.Scaling": "100",
		"Overlay.Interactive.Score": "true", "Overlay.Interactive.ShowJammers": "true", "Overlay.Interactive.ShowLineups": "true",
		"Overlay.Interactive.ShowNames": "true", "Overlay.Interactive.ShowPenaltyClocks": "true",
		"ScoreBoard.Penalties.UseLT": "true", "ScoreBoard.Penalties.UsePBT": strconv.FormatBool(usePBT), "ScoreBoard.HideLineups": "false",
		"ScoreBoard.Stats.InputFile": "", "ScoreBoard.AutoStart": auto[st.AutoStart], "ScoreBoard.AutoStart5": auto[st.AutoStart5], "ScoreBoard.Auto5": strconv.FormatBool(st.Auto5),
		"ScoreBoard.Early5Delay": strconv.Itoa(st.Early5Delay), "ScoreBoard.Clock.Synch": strconv.FormatBool(st.SyncClocks), "ScoreBoard.Teams.DisplayName": "League", "ScoreBoard.Teams.FileName": "Full",
		"ScoreBoard.Game.DefaultNameFormat": "%d %G %1 vs. %2 (%s: %S)",
		"ScoreBoard.Intermission.PreGame":   "Time To Derby", "ScoreBoard.Intermission.Intermission": "Intermission",
		"ScoreBoard.Intermission.Unofficial": "Unofficial Score", "ScoreBoard.Intermission.Official": "Final Score",
		"ScoreBoard.Intermission.OfficialWithClock": "Final Score",
		"ScoreBoard.AutoEndJam":                     strconv.FormatBool(autoEndJam), "ScoreBoard.AutoEndTTO": strconv.FormatBool(autoEndTTO),
	}
	for k, v := range map[string]string{
		"BoxStyle": "box_flat_bright", "ClockAfterTimeout": "Both", "CurrentView": "scoreboard",
		"CustomHtml": "/customhtml/fullscreen/example.html", "Image": "/images/fullscreen/test-image.png", "ImageScaling": "contain",
		"HideBanners": "false", "HideLogos": "false", "HidePenaltyClocks": "false", "SidePadding": "", "SponsorScaling": "contain",
		"SwapTeams": "false", "Video": "/videos/fullscreen/test-video.webm", "VideoScaling": "contain",
	} {
		set["ScoreBoard.Preview_"+k], set["ScoreBoard.View_"+k] = v, v
	}
	out := Keys{}
	for k, v := range set {
		out["ScoreBoard.Settings.Setting("+k+")"] = v
	}
	return out
}
