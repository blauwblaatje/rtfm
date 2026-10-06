package engine

import (
	"strings"
	"time"

	"crgformat/derive"
	"crgformat/replay"
)

// The five clocks, in the order of ClockNames.
const (
	Period = iota
	Jam
	Lineup
	Timeout
	Intermission
	numClocks
)

// ClockNames are the clock names used in ClockSet and Undone.clocks.
var ClockNames = [numClocks]string{"period", "jam", "lineup", "timeout", "intermission"}

func clockIndex(name string) int {
	for i, n := range ClockNames {
		if n == name {
			return i
		}
	}
	return -1
}

// clock is one clock's state: its elapsed time at anchor, and whether it has
// been running since then. Values are elapsed milliseconds, as in the log.
type clock struct {
	base    int64
	anchor  time.Time
	running bool
	max     int64 // the clock's maximum (Clock.MaximumTime); 0 if it has none
	stops   bool  // stops at max (period, jam, intermission)
}

func (c *clock) at(t time.Time) int64 {
	v := c.base
	if c.running && t.After(c.anchor) {
		v += t.Sub(c.anchor).Milliseconds()
	}
	if c.stops && c.max > 0 && v > c.max {
		v = c.max
	}
	return v
}

func (c *clock) start(v int64, t time.Time) { c.base, c.anchor, c.running = v, t, true }
func (c *clock) stop(v int64)               { c.base, c.running = v, false }
func (c *clock) set(v int64, t time.Time)   { c.base, c.anchor = v, t }

// expiry is when a running clock with a maximum reaches it.
func (c *clock) expiry() (time.Time, bool) {
	if !c.running || c.max <= 0 {
		return time.Time{}, false
	}
	return c.anchor.Add(time.Duration(c.max-c.base) * time.Millisecond), true
}

// clocks follows the log: which clocks run and from what value. Lineup and
// intermission starts aren't logged; they follow from JamEnded, TimeoutEnded
// and PeriodEnded (README "Game flow").
type clocks struct {
	c        [numClocks]clock
	overtime bool // OvertimeStarted, and the overtime jam hasn't started yet

	// The running timeout: when it started and the period clock then, for
	// Timeout.StopPeriodClockOn* (see TimeoutUpdated).
	toID    string
	toStart time.Time
	toPC    int64

	// postTO: the lineup running now follows a timeout (Java's timeout clock
	// runs on then, and displays can show it: ClockAfterTimeout).
	postTO bool
}

// rules are the timing rules the engine acts on (docs/engine.md "Rules").
type rules struct {
	periods          int
	period, jam      int64
	lineup, lineupOT int64
	teamTO           int64
	intermissions    []int64
	lineupStopsPC    bool
	jamDuringTO      bool // Timeout.JamDuring: a jam can run while a timeout goes on
	resetJamNumbers  bool
	endBetweenJams   bool

	// Which timeouts stop the period clock (Rule.java STOP_PC_*).
	stopAlways, stopOTO, stopTTO, stopOR bool
	extraJamAfterOTO                     bool

	jamsPer                  int   // Period.JamsPer (Short Track): the period is this many jams long
	suddenScoring            bool  // Jam.SuddenScoring (JRDA)
	ssJam                    int64 // Jam.SuddenScoringDuration
	ssMinDiff, ssMaxTrailing int
	injuryContinuation       bool // Jam.InjuryContinuation (JRDA)
}

func rulesOf(s *replay.Summary) rules {
	r := rules{
		periods:            atoi(derive.Rule(s, "Period.Number")),
		period:             derive.ClockRule(s, "Period.Duration"),
		jam:                derive.ClockRule(s, "Jam.Duration"),
		lineup:             derive.ClockRule(s, "Lineup.Duration"),
		lineupOT:           derive.ClockRule(s, "Lineup.OvertimeDuration"),
		teamTO:             derive.ClockRule(s, "Timeout.TeamTODuration"),
		lineupStopsPC:      derive.Rule(s, "Lineup.StopsPeriodClock") == "true",
		jamDuringTO:        derive.Rule(s, "Timeout.JamDuring") == "true",
		resetJamNumbers:    derive.Rule(s, "Jam.ResetNumberEachPeriod") == "true",
		endBetweenJams:     derive.Rule(s, "Period.EndBetweenJams") == "true",
		stopAlways:         derive.Rule(s, "Timeout.StopPeriodClockAlways") == "true",
		stopOTO:            derive.Rule(s, "Timeout.StopPeriodClockOnOTO") == "true",
		stopTTO:            derive.Rule(s, "Timeout.StopPeriodClockOnTTO") == "true",
		stopOR:             derive.Rule(s, "Timeout.StopPeriodClockOnOR") == "true",
		extraJamAfterOTO:   derive.Rule(s, "Timeout.ExtraJamAfterOTO") == "true",
		jamsPer:            atoi(derive.Rule(s, "Period.JamsPer")),
		suddenScoring:      derive.Rule(s, "Jam.SuddenScoring") == "true",
		ssJam:              derive.ClockRule(s, "Jam.SuddenScoringDuration"),
		ssMinDiff:          atoi(derive.Rule(s, "Jam.SuddenScoringMinPointsDifference")),
		ssMaxTrailing:      atoi(derive.Rule(s, "Jam.SuddenScoringMaxTrailingPoints")),
		injuryContinuation: derive.Rule(s, "Jam.InjuryContinuation") == "true",
	}
	// With a fixed number of jams, the period is that many jam lengths
	// (Java ClockImpl).
	if r.jamsPer > 0 {
		r.period = int64(r.jamsPer) * r.jam
	}
	for _, d := range strings.Split(derive.Rule(s, "Intermission.Durations"), ",") {
		r.intermissions = append(r.intermissions, derive.ParseClock(d))
	}
	return r
}

func (r rules) intermission(afterPeriod int) int64 {
	if afterPeriod >= 1 && afterPeriod <= len(r.intermissions) {
		return r.intermissions[afterPeriod-1]
	}
	return 0
}

// unsupported lists the rules that are on in a game but not handled by the
// engine yet (docs/engine.md "Rules").
// Unsupported lists the rules a game has on that the engine can't run yet.
func Unsupported(s *replay.Summary) []string { return unsupported(s) }

func unsupported(s *replay.Summary) []string {
	var out []string
	for rule, want := range map[string]string{
		// Every Java rule is handled now; a rule the engine can't run
		// goes here, and the engine won't start a game with it.
	} {
		if derive.Rule(s, rule) != want {
			out = append(out, rule+"="+derive.Rule(s, rule))
		}
	}
	return out
}

// apply updates the clocks for one event that happened at t. s is the game
// after the event.
func (cs *clocks) apply(e *replay.Event, t time.Time, s *replay.Summary) {
	r := rulesOf(s)
	p, j, l, to, in := &cs.c[Period], &cs.c[Jam], &cs.c[Lineup], &cs.c[Timeout], &cs.c[Intermission]
	p.max, p.stops = r.period, true
	j.stops = true
	to.max = r.teamTO
	in.stops = true
	pc, jc := val(e.PC), val(e.JC)
	switch e.Type {
	case "GameCreated":
		*cs = clocks{}
		cs.c[Period].max, cs.c[Period].stops = r.period, true
		cs.c[Jam].max, cs.c[Jam].stops = r.jam, true
		j.max = r.jam
	case "PeriodStarted":
		p.stop(pc)
		in.stop(in.at(t))
		cs.overtime = false
	case "JamStarted":
		cs.postTO = false
		// With Timeout.JamDuring a jam can start during a timeout, which
		// goes on (with the period clock stopped) until it's ended (Java
		// GameImpl._startJam).
		duringTO := r.jamDuringTO && to.running
		if duringTO {
			p.stop(p.at(t))
		} else if e.Overtime || r.jamsPer > 0 {
			// Overtime jams don't run the period clock; with a fixed number
			// of jams it moves a jam length at the end of each (JamEnded).
			p.stop(p.at(t))
		} else {
			p.start(pc, t)
		}
		j.max = jamLength(s, e.Jam, r)
		j.start(0, t)
		l.stop(l.at(t))
		if !duringTO {
			to.stop(to.at(t))
		}
		in.stop(in.at(t))
		cs.overtime = false
	case "JamEnded":
		j.stop(jc)
		if to.running {
			break // a jam during a timeout: the timeout goes on, no lineup
		}
		l.max = r.lineup
		l.start(0, t)
		switch {
		case r.jamsPer > 0:
			p.stop(pc) // a jam length further (the engine's JamEnded)
		case !p.running:
		case r.lineupStopsPC:
			p.stop(pc)
		default:
			p.start(pc, t)
		}
	case "TimeoutStarted":
		cs.postTO = false
		to.start(0, t)
		l.stop(l.at(t))
		cs.toID, cs.toStart, cs.toPC = e.Timeout, t, pc
		if r.stopAlways && p.running {
			p.stop(pc) // the next jam restarts it
		}
	case "TimeoutUpdated":
		// Without StopPeriodClockAlways, the period clock stops once the
		// timeout's type says so, back to where it was when the timeout
		// started; or runs on from then if the type says it shouldn't stop
		// (Java GameImpl.setTimeoutType).
		x := findTimeout(s, e.Timeout)
		if r.stopAlways || x == nil || e.Timeout != cs.toID || !to.running || periodEnded(s) {
			break
		}
		stop := false
		switch x.Owner {
		case "1", "2":
			stop = x.Review && r.stopOR || !x.Review && r.stopTTO
		case "O":
			stop = r.stopOTO
		}
		switch {
		case stop && p.running:
			p.stop(cs.toPC)
		case !stop && !p.running:
			p.start(cs.toPC, cs.toStart)
		}
	case "TimeoutEnded":
		to.stop(val(e.Duration))
		if periodEnded(s) {
			break // a timeout after the period (an official review after the last jam)
		}
		l.max = r.lineup
		l.start(0, t)
		cs.postTO = true
		// With too little time left for another jam, and no timeout that
		// forces one, the period clock runs out in the lineup (Java NO_MORE_JAM).
		if r.jamsPer == 0 && !p.running && p.at(t) < p.max && noMoreJam(s, r, p.at(t)) {
			p.start(pc, t)
		}
	case "PeriodEnded":
		cs.postTO = false
		p.stop(pc)
		j.stop(j.at(t))
		l.stop(l.at(t))
		n := 0
		if len(s.Periods) > 0 {
			n = s.Periods[len(s.Periods)-1].Number
		}
		if d := r.intermission(n); d > 0 {
			in.max = d
			in.start(0, t)
		}
	case "LineupStarted":
		// The operator's Lineup before the first jam or in an intermission
		// (Java GameImpl._startLineup): it ends the intermission.
		in.stop(in.at(t))
		l.max = r.lineup
		if cs.overtime {
			l.max = r.lineupOT
		}
		l.start(0, t)
		if r.lineupStopsPC && p.running {
			p.stop(pc)
		}
	case "OvertimeStarted":
		cs.overtime = true
		in.stop(in.at(t))
		l.max = r.lineupOT
		l.start(0, t)
	case "ClockSet":
		if i := clockIndex(e.Clock); i >= 0 {
			cs.setRemaining(i, val(e.Time), t)
		}
	case "Undone":
		for name, rem := range e.Clocks {
			if i := clockIndex(name); i >= 0 {
				cs.setRemaining(i, rem, t)
			}
		}
	case "GameEndedEarly":
		for i := range cs.c {
			cs.c[i].stop(cs.c[i].at(t))
		}
	}
}

// jamLength is how long a jam can run (the jam clock's maximum): the time
// the injured jam had left for an injury continuation, the sudden scoring
// jam length in a sudden scoring period (not overtime), else Jam.Duration
// (Java ClockImpl, JamImpl.start).
func jamLength(s *replay.Summary, jamID string, r rules) int64 {
	for _, p := range s.Periods {
		for i, j := range p.Jams {
			if j.ID != jamID {
				continue
			}
			if j.InjuryContinuation && i > 0 && p.Jams[i-1].Duration != nil {
				if left := r.jam - *p.Jams[i-1].Duration; left > 0 {
					return left
				}
				return 0
			}
			if p.SuddenScoring && !j.Overtime && r.ssJam > 0 {
				return r.ssJam
			}
		}
	}
	return r.jam
}

func findTimeout(s *replay.Summary, id string) *replay.Timeout {
	for _, p := range s.Periods {
		for _, to := range p.Timeouts {
			if to.ID == id {
				return to
			}
		}
	}
	return nil
}

func periodEnded(s *replay.Summary) bool {
	return len(s.Periods) > 0 && s.Periods[len(s.Periods)-1].End != ""
}

// noMoreJam says whether the period will end without another jam: the last
// jam ended with less than a lineup's time left, and no timeout since then
// gives the teams another jam (Rules 2.5; Java GameImpl NO_MORE_JAM).
func noMoreJam(s *replay.Summary, r rules, pc int64) bool {
	if !r.endBetweenJams || r.lineupStopsPC || r.jamsPer > 0 || pc == 0 || len(s.Periods) == 0 {
		return false
	}
	p := s.Periods[len(s.Periods)-1]
	if len(p.Jams) == 0 {
		return false
	}
	last := p.Jams[len(p.Jams)-1]
	if last.PeriodClockEnd == nil || r.period-*last.PeriodClockEnd > r.lineup {
		return false
	}
	tto, or := r.stopAlways || r.stopTTO, r.stopAlways || r.stopOR
	oto := r.extraJamAfterOTO && (r.stopAlways || r.stopTTO)
	for _, to := range p.Timeouts {
		if to.AfterJam != last.ID {
			continue
		}
		switch {
		case (to.Owner == "1" || to.Owner == "2") && to.Review && or,
			(to.Owner == "1" || to.Owner == "2") && !to.Review && tto,
			to.Owner != "1" && to.Owner != "2" && oto:
			return false
		}
	}
	return true
}

// setRemaining sets a clock from the remaining time an operator typed in
// (README: ClockSet logs remaining time). A clock without a maximum has none
// to count down from, so its remaining time is taken as elapsed.
func (cs *clocks) setRemaining(i int, remaining int64, t time.Time) {
	c := &cs.c[i]
	v := remaining
	if c.max > 0 {
		v = c.max - remaining
	}
	if v < 0 {
		v = 0
	}
	c.set(v, t)
}

// remaining is the inverse of setRemaining.
func (cs *clocks) remaining(i int, t time.Time) int64 {
	c := &cs.c[i]
	if c.max > 0 {
		return c.max - c.at(t)
	}
	return c.at(t)
}

func val(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
