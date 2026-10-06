// Package engine runs a live game (docs/engine.md): it keeps the clocks,
// turns operator commands into events, and ends jams and periods when their
// clocks run out. The game state is what package replay makes of the events;
// the engine only adds the clocks.
package engine

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"crgformat"
	"crgformat/derive"
	"crgformat/replay"
)

// Now is the engine's time source. Tests use a fake one.
type Now func() time.Time

// Log is where events go. Append must have the lines on disk (fsync) before
// it returns; if it returns an error, none of them may count.
type Log interface {
	Append(lines [][]byte) error
}

// Settings are server settings, not rules of the game. Both are off by
// default, as in Java: the log records what happens on track, and a jam or
// timeout is only over when an official whistles.
type Settings struct {
	// AutoEndJam ends the jam when the jam clock reaches Jam.Duration
	// (Java ScoreBoard.AutoEndJam). Off: the jam clock stops at 0:00 but the
	// jam goes on, and points still count, until the Jam Timer whistles and
	// the operator presses Stop jam.
	AutoEndJam bool

	// AutoEndTeamTimeouts ends a team timeout when Timeout.TeamTODuration
	// has passed. Off by default: a timeout lasts until the Jam Timer
	// whistles, and the operator records that.
	AutoEndTeamTimeouts bool
	// UsePBT turns on penalty box timing (the box timers' page). Off by
	// default, as in Java.
	UsePBT bool

	// The electronic jam timer's lineup (fivesec.go), all off by default:
	// AutoStart starts a jam or a timeout ("jam", "timeout" or "") when the
	// lineup time is up without five seconds called, AutoStart5 when it was
	// called; Auto5 calls five seconds 5 s before the lineup time is up;
	// Early5Delay is the N of "Jam in 5+N".
	AutoStart   string
	AutoStart5  string
	Auto5       bool
	Early5Delay int

	// SyncClocks shows the jam, lineup and timeout clocks turning over their
	// seconds with the period clock (Java's Clock.Synch; not WFTDA's rules,
	// but it looks odd when they don't). Only what screens show moves, by
	// less than half a second; the log keeps the real times.
	SyncClocks bool

	// AutoTrip starts the next scoring trip this many seconds after points
	// were entered on the current one during a jam (autotrip.go); 0 is off.
	AutoTrip int
}

// Source says who caused an event (src in the log).
type Source struct {
	Op     string `json:"op,omitempty"`
	Device string `json:"device,omitempty"`
	Role   string `json:"role,omitempty"`
	// Official is the official (id, from the game's roster) who pressed the
	// button, when they picked themselves at the login.
	Official string `json:"official,omitempty"`
}

// ConflictError is a command that doesn't fit the game's state, such as
// Stop jam with no jam running. The server answers it with 409.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

func conflict(format string, a ...any) error { return &ConflictError{fmt.Sprintf(format, a...)} }

// Engine runs one game. It is safe for concurrent use.
type Engine struct {
	mu       sync.Mutex
	now      Now
	wall0    time.Time // wall clock at start, see time()
	mono0    time.Time
	v        *crgformat.Validators
	log      Log
	settings Settings

	events []*replay.Event
	times  []time.Time // when each event happened, for the clocks
	rp     *replay.Replayer
	cl     clocks
	undo   []*step
	gap    time.Duration // time between the last event and Open, see Recovered
	plan   lineupPlan    // the jam timer's five-second call and automatic start

	tripAdv      map[string]*tripAdvance // by jam/team: the next trip to start (autotrip.go)
	lastAutoTrip map[string]*autoTrip    // by jam/team: the last trip started that way

	// readOnly, when set, is why the game can't be changed here: a copy of
	// a game that runs somewhere else (a track server). Commands and events
	// are refused with it, and nothing ends by itself.
	readOnly string
}

// step is an undoable game-flow action (docs/engine.md "Undo").
type step struct {
	label   string
	seqs    []int
	at      time.Time
	creates []string // ids made by this step, which an undo removes
	started string   // the jam this step started, which an undo makes upcoming again
	cmd     string
}

// Open starts an engine on an existing log (at least GameCreated). events
// must be the log's events in order.
func Open(events []*replay.Event, log Log, now Now, settings Settings) (*Engine, error) {
	v, err := crgformat.NewValidators()
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	start := now()
	e := &Engine{now: now, wall0: start.Round(0), mono0: start, v: v, log: log, settings: settings}
	e.events = append([]*replay.Event(nil), events...)
	for _, ev := range e.events {
		t, err := time.Parse(time.RFC3339Nano, ev.T)
		if err != nil {
			return nil, fmt.Errorf("seq %d: time %q: %w", ev.Seq, ev.T, err)
		}
		e.times = append(e.times, t)
	}
	if err := e.rebuild(); err != nil {
		return nil, err
	}
	if n := len(e.times); n > 0 {
		e.gap = e.time().Sub(e.times[n-1])
	}
	e.syncPlan(e.time())
	return e, nil
}

// time is the engine's current time. The wall clock is read once, at Open;
// after that time follows the monotonic clock, so a wall clock that jumps
// (a Raspberry Pi catching up with NTP) doesn't move the game clocks. It has
// millisecond precision, like the log.
func (e *Engine) time() time.Time {
	return e.wall0.Add(e.now().Sub(e.mono0)).Truncate(time.Millisecond)
}

// rebuild replays the whole log, skipping undone events, and works out the
// clocks from it.
func (e *Engine) rebuild() error {
	skip, err := replay.UndoneSeqs(e.events)
	if err != nil {
		return err
	}
	rp, err := replay.NewReplayer(nil)
	if err != nil {
		return err
	}
	var cl clocks
	for i, ev := range e.events {
		if skip[ev.Seq] {
			continue
		}
		if err := rp.Apply(ev); err != nil {
			return err
		}
		cl.apply(ev, e.times[i], rp.Summary())
	}
	if rp.Summary() == nil {
		return fmt.Errorf("the log has no GameCreated event")
	}
	e.rp, e.cl = rp, cl
	return nil
}

// Recovered is the time between the last event in the log and Open. After a
// crash, running clocks have been counted on over this gap; the operator
// should confirm them (docs/engine.md "Crash recovery").
func (e *Engine) Recovered() time.Duration { return e.gap }

// --- writing events ----------------------------------------------------------

type ev = map[string]any

// commit stamps events, checks them against the schema and the game, writes
// them to the log and applies them. Nothing changes if any step fails.
func (e *Engine) commit(at time.Time, src *Source, batch []ev) ([]int, error) {
	var lines [][]byte
	var parsed []*replay.Event
	seq := len(e.events)
	for _, fields := range batch {
		seq++
		line := ev{}
		for k, v := range fields {
			line[k] = v
		}
		line["v"], line["seq"], line["t"] = 1, seq, at.UTC().Format("2006-01-02T15:04:05.000Z")
		if src != nil && *src != (Source{}) {
			line["src"] = src
		}
		b, err := json.Marshal(line)
		if err != nil {
			return nil, err
		}
		doc, err := crgformat.DecodeJSON(b)
		if err != nil {
			return nil, err
		}
		if err := e.v.ValidateEvent(doc); err != nil {
			return nil, fmt.Errorf("%s: %w", line["type"], err)
		}
		var pe replay.Event
		if err := json.Unmarshal(b, &pe); err != nil {
			return nil, err
		}
		lines, parsed = append(lines, b), append(parsed, &pe)
	}
	// Try the events on the game before writing them.
	undo := false
	for _, pe := range parsed {
		if pe.Type == "Undone" {
			undo = true
			continue
		}
		if err := e.rp.Apply(pe); err != nil {
			e.mustRebuild()
			return nil, err
		}
	}
	if undo {
		// What an Undone reverts takes a replay of the whole log.
		saved := len(e.events)
		e.events = append(e.events, parsed...)
		for range parsed {
			e.times = append(e.times, at)
		}
		err := e.rebuild()
		e.events, e.times = e.events[:saved], e.times[:saved]
		if err != nil {
			e.mustRebuild()
			return nil, err
		}
	}
	if err := e.log.Append(lines); err != nil {
		e.mustRebuild()
		return nil, fmt.Errorf("writing the log: %w", err)
	}
	var seqs []int
	for _, pe := range parsed {
		e.events = append(e.events, pe)
		e.times = append(e.times, at)
		seqs = append(seqs, pe.Seq)
	}
	if undo {
		if err := e.rebuild(); err != nil {
			return nil, err
		}
	} else {
		for _, pe := range parsed {
			e.cl.apply(pe, at, e.rp.Summary())
		}
	}
	e.syncPlan(at)
	return seqs, nil
}

// mustRebuild restores the game from the committed log after a failed batch.
func (e *Engine) mustRebuild() {
	if err := e.rebuild(); err != nil {
		panic(fmt.Sprintf("engine: the committed log no longer replays: %v", err))
	}
}

func newID(prefix string) string {
	b := make([]byte, 5)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// --- state ---------------------------------------------------------------------

// Phases of a game.
const (
	PhaseBefore       = "before"       // no jam yet
	PhaseJam          = "jam"          // a jam is running
	PhaseLineup       = "lineup"       // between jams
	PhaseTimeout      = "timeout"      // a timeout is running
	PhaseIntermission = "intermission" // between periods
	PhaseOvertime     = "overtime"     // overtime announced, its jam not started
	PhaseEnded        = "ended"        // the last period is over (overtime is still possible)
	PhaseOver         = "over"         // official score declared, or ended early
)

func (e *Engine) phase() string {
	s := e.rp.Summary()
	switch {
	case s.State == "finished" || s.State == "cancelled" || s.State == "forfeited":
		return PhaseOver
	case e.cl.c[Jam].running:
		return PhaseJam
	case e.cl.c[Timeout].running:
		return PhaseTimeout
	case e.cl.overtime:
		return PhaseOvertime
	case len(s.Periods) == 0:
		return PhaseBefore
	}
	p := s.Periods[len(s.Periods)-1]
	if p.End != "" {
		if p.Number < rulesOf(s).periods {
			return PhaseIntermission
		}
		return PhaseEnded
	}
	return PhaseLineup
}

// ClockView is one clock as screens show it.
type ClockView struct {
	Name      string `json:"name"`
	Elapsed   int64  `json:"elapsed"`
	Remaining int64  `json:"remaining"`
	Max       int64  `json:"max"`
	Running   bool   `json:"running"`
}

// UndoView describes what the next undo does.
type UndoView struct {
	Label   string `json:"label"`             // e.g. "Timeout at 17:03"
	BackTo  string `json:"backTo"`            // e.g. "lineup, period clock running at 17:21"
	Blocked string `json:"blocked,omitempty"` // why it can't be undone, if it can't
}

// State is the engine's view of the game for screens.
type State struct {
	Seq      int         `json:"seq"`
	Time     time.Time   `json:"time"`
	Phase    string      `json:"phase"`
	Period   int         `json:"period"` // current or last period's number, 0 before the game
	Jam      int         `json:"jam"`    // current or last jam's number
	JamID    string      `json:"jamId,omitempty"`
	Timeout  string      `json:"timeoutId,omitempty"`
	Upcoming string      `json:"upcomingJamId,omitempty"`
	Clocks   []ClockView `json:"clocks"`
	Undo     *UndoView   `json:"undo,omitempty"`
	Five     *FiveView   `json:"five,omitempty"` // the jam timer's five-second call, in a lineup
	// PostTimeout: the lineup running now follows a timeout.
	PostTimeout bool `json:"postTimeout,omitempty"`
}

// State returns the current state, after ending anything whose clock ran out.
func (e *Engine) State() (*State, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.advance(); err != nil {
		return nil, err
	}
	return e.state(), nil
}

// View returns the current state without ending anything whose clock ran
// out (Tick does that), so it never writes to the log.
func (e *Engine) View() *State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state()
}

func (e *Engine) state() *State {
	t := e.time()
	s := e.rp.Summary()
	st := &State{Seq: len(e.events), Time: t, Phase: e.phase()}
	if s.UpcomingJam != nil {
		st.Upcoming = s.UpcomingJam.ID
	}
	if p, j := e.lastJam(); p != nil {
		st.Period = p.Number
		if j != nil {
			st.Jam, st.JamID = j.Number, j.ID
		}
	}
	if to := e.openTimeout(); to != nil {
		st.Timeout = to.ID
	}
	for i := range e.cl.c {
		c := &e.cl.c[i]
		st.Clocks = append(st.Clocks, ClockView{Name: ClockNames[i], Elapsed: c.at(t), Remaining: e.cl.remaining(i, t),
			Max: c.max, Running: c.running && !(c.stops && c.max > 0 && c.at(t) >= c.max)})
	}
	if e.settings.SyncClocks {
		syncClocks(st.Clocks)
	}
	st.Undo = e.undoView()
	st.Five = e.fiveView(t)
	st.PostTimeout = st.Phase == PhaseLineup && e.cl.postTO
	return st
}

// Summary returns the game. The caller must not keep it past the next
// command; marshal it straight away.
func (e *Engine) Summary(f func(*replay.Summary)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f(e.rp.Summary())
}

// Events returns the number of events in the log.
func (e *Engine) Events() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.events)
}

func (e *Engine) lastJam() (*replay.Period, *replay.Jam) {
	s := e.rp.Summary()
	if len(s.Periods) == 0 {
		return nil, nil
	}
	p := s.Periods[len(s.Periods)-1]
	if len(p.Jams) == 0 {
		return p, nil
	}
	return p, p.Jams[len(p.Jams)-1]
}

// lastStartedJam is the last jam of the game, looking back past periods
// without jams.
func (e *Engine) lastStartedJam() *replay.Jam {
	s := e.rp.Summary()
	for i := len(s.Periods) - 1; i >= 0; i-- {
		if js := s.Periods[i].Jams; len(js) > 0 {
			return js[len(js)-1]
		}
	}
	return nil
}

func (e *Engine) openTimeout() *replay.Timeout {
	if !e.cl.c[Timeout].running {
		return nil
	}
	s := e.rp.Summary()
	for i := len(s.Periods) - 1; i >= 0; i-- {
		for k := len(s.Periods[i].Timeouts) - 1; k >= 0; k-- {
			if to := s.Periods[i].Timeouts[k]; to.End == "" && to.Duration == nil {
				return to
			}
		}
	}
	return nil
}

// --- clocks running out ----------------------------------------------------------

// Tick ends whatever ran out of time by now: the jam at Jam.Duration, the
// period when its clock runs out between jams, and team timeouts if
// Settings.AutoEndTeamTimeouts. The server calls it at NextDeadline; commands
// call it first too.
func (e *Engine) Tick() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.advance()
}

// NextDeadline is when the next thing runs out, if anything is running.
func (e *Engine) NextDeadline() (time.Time, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	at, _, ok := e.nextExpiry()
	return at, ok
}

const (
	expJam = iota + 1
	expPeriod
	expTimeout
	expFive      // Auto 5s: five seconds called
	expAutoStart // the lineup time is up: start a jam or a timeout
	expTrip      // Settings.AutoTrip: the next scoring trip starts
)

func (e *Engine) nextExpiry() (time.Time, int, bool) {
	var best time.Time
	kind := 0
	consider := func(t time.Time, k int) {
		if kind == 0 || t.Before(best) {
			best, kind = t, k
		}
	}
	switch e.phase() {
	case PhaseJam:
		if t, ok := e.cl.c[Jam].expiry(); ok && e.settings.AutoEndJam {
			consider(t, expJam)
		}
	case PhaseLineup, PhaseBefore, PhaseIntermission:
		// Before the first jam and in an intermission the lineup clock runs
		// only after the operator's Lineup.
		if t, ok := e.cl.c[Period].expiry(); ok && e.phase() == PhaseLineup {
			consider(t, expPeriod)
		}
		if e.cl.c[Lineup].running {
			if e.plan.autoFive && !e.plan.five {
				consider(e.lineupAt(e.plan.fiveAt), expFive)
			}
			if !e.plan.done && e.plan.startAt > 0 && e.autoStartKind() != AutoStartOff {
				consider(e.lineupAt(e.plan.startAt), expAutoStart)
			}
		}
	case PhaseTimeout:
		// The period clock may run during a timeout (StopPeriodClockOn*),
		// but the period only ends once the timeout is over.
		if to := e.openTimeout(); to != nil && e.settings.AutoEndTeamTimeouts && (to.Owner == "1" || to.Owner == "2") && !to.Review {
			if t, ok := e.cl.c[Timeout].expiry(); ok {
				consider(t, expTimeout)
			}
		}
	}
	if _, a := e.nextTrip(); a != nil {
		consider(a.due, expTrip)
	}
	return best, kind, kind != 0
}

// advance handles every expiry up to now, each at the moment it happened, so
// the log shows the jam ending at exactly 2:00 however late the timer fired.
func (e *Engine) advance() error { return e.advanceTo(e.time()) }

func (e *Engine) advanceTo(now time.Time) error {
	if e.readOnly != "" {
		return nil // the copy changes only with its log
	}
	for {
		at, kind, ok := e.nextExpiry()
		if !ok || at.After(now) {
			return nil
		}
		var batch []ev
		switch kind {
		case expFive:
			e.plan.five = true
			continue
		case expTrip:
			if err := e.advanceTrip(at); err != nil {
				return err
			}
			continue
		case expAutoStart:
			e.plan.done = true
			name := map[string]string{AutoStartJam: "start-jam", AutoStartTimeout: "timeout"}[e.autoStartKind()]
			// An ordinary command at the moment the lineup time was up,
			// which Undo takes back like any other.
			if err := e.run(Command{Name: name}, at, &Source{Role: "clock"}); err != nil {
				var ce *ConflictError
				if errors.As(err, &ce) {
					continue // e.g. the period ended in the lineup
				}
				return err
			}
			continue
		case expJam:
			batch = append(batch, e.jamEnded(e.lastStartedJam(), "time", at, e.cl.c[Jam].max, true)...)
		case expPeriod:
			batch = append(batch, e.periodEnded(at))
		case expTimeout:
			to := e.openTimeout()
			batch = append(batch, ev{"type": "TimeoutEnded", "timeout": to.ID, "duration": e.cl.c[Timeout].max,
				"pc": e.cl.c[Period].at(at)})
			if e.periodOver(at) && !periodEnded(e.rp.Summary()) {
				batch = append(batch, e.periodEnded(at))
			}
		}
		if _, err := e.commit(at, &Source{Role: "clock"}, batch); err != nil {
			return err
		}
		e.undo = nil // what the clock did isn't an operator's action to undo
	}
}

// jamEnded is a JamEnded, and the PeriodEnded if the period is over with it
// (unless a timeout follows: then the period ends with the timeout). With a
// fixed number of jams per period (Period.JamsPer, Short Track), the period
// clock moves one jam length at the end of each jam (Java GameImpl._endJam).
func (e *Engine) jamEnded(j *replay.Jam, reason string, at time.Time, jc int64, endPeriod bool) []ev {
	r := rulesOf(e.rp.Summary())
	pc := e.cl.c[Period].at(at)
	if r.jamsPer > 0 {
		pc = min(pc+r.jam, r.period)
	}
	out := []ev{{"type": "JamEnded", "jam": j.ID, "reason": reason, "pc": pc, "jc": jc}}
	out = append(out, e.carryBoxed(j)...)
	if p := &e.cl.c[Period]; endPeriod && p.max > 0 && pc >= p.max {
		pe := e.periodEnded(at)
		pe["pc"] = pc
		out = append(out, pe)
	}
	return out
}

// carryBoxed puts the skaters still in the box when a jam ends into the next
// jam's lineup, in the position they had (as Java: they're still on track,
// sitting): unless that position is taken already, or they're in the next
// lineup somewhere else.
func (e *Engine) carryBoxed(j *replay.Jam) []ev {
	s := e.rp.Summary()
	up := s.UpcomingJam
	if up == nil {
		return nil
	}
	var out []ev
	for _, bt := range s.BoxTrips {
		if bt.End != nil || bt.Skater == "" {
			continue
		}
		for ti, tj := range j.Teams {
			for _, pos := range derive.Positions {
				f := tj.Lineup[pos]
				// The skater of the trip, or a substitute sitting in it.
				if f == nil || f.Skater == "" || (f.Skater != bt.Skater && f.BoxTrip != bt.ID) || ti >= len(up.Teams) {
					continue
				}
				next := up.Teams[ti]
				if nf := next.Lineup[pos]; nf != nil && nf.Skater != "" {
					continue // someone's in that position already
				}
				placed := false
				for _, nf := range next.Lineup {
					placed = placed || (nf != nil && nf.Skater == f.Skater)
				}
				if !placed {
					carry := ev{"type": "FieldingSet", "jam": up.ID, "team": tj.Team, "position": pos, "skater": f.Skater}
					if f.Skater != bt.Skater {
						carry["boxTrip"] = bt.ID
					}
					out = append(out, carry)
				}
			}
		}
	}
	return out
}

func (e *Engine) periodOver(at time.Time) bool {
	p := &e.cl.c[Period]
	return p.max > 0 && p.at(at) >= p.max
}

func (e *Engine) periodEnded(at time.Time) ev {
	p, _ := e.lastJam()
	return ev{"type": "PeriodEnded", "period": p.ID, "pc": e.cl.c[Period].at(at)}
}

// --- game flow commands ----------------------------------------------------------

// A command is what an operator asks for (docs/engine.md "Commands").
type Command struct {
	Name string `json:"name"` // see the switch in run

	Reason  string  `json:"reason,omitempty"`  // jam-reason: time|calloff|injury|official|unknown
	Detail  string  `json:"detail,omitempty"`  // jam-reason
	Jam     string  `json:"jam,omitempty"`     // jam-reason: which jam; default the last
	Timeout string  `json:"timeout,omitempty"` // timeout-type: which timeout; default the open or last one
	Owner   *string `json:"owner,omitempty"`   // timeout-type: "1", "2", "O" or ""
	Review  *bool   `json:"review,omitempty"`  // timeout-type
	AsTO    *bool   `json:"asTimeout,omitempty"`
	Clock   string  `json:"clock,omitempty"` // set-clock
	Time    *int64  `json:"time,omitempty"`  // set-clock: remaining ms, as the operator types it

	Outcome        string `json:"outcome,omitempty"`        // end-early
	ForfeitingTeam string `json:"forfeitingTeam,omitempty"` // end-early
	Text           string `json:"text,omitempty"`           // end-early: reason

	Score *replay.Score `json:"score,omitempty"` // official-score
	HR    string        `json:"hr,omitempty"`
	HNSO  string        `json:"hnso,omitempty"`

	Replace *Command `json:"replace,omitempty"` // replace: the command to do instead

	InjuryContinuation bool  `json:"injuryContinuation,omitempty"` // start-jam (JRDA)
	Value              *bool `json:"value,omitempty"`              // sudden-scoring
}

// Do runs a command. The time of the press is taken when the command
// arrives, before waiting for the game (another command, a screen reading
// it), so a busy moment doesn't make a jam start late.
func (e *Engine) Do(c Command, src *Source) error {
	at := e.time()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.readOnly != "" {
		return conflict("%s", e.readOnly)
	}
	if err := e.advanceTo(at); err != nil {
		return err
	}
	switch c.Name {
	case "five-seconds", "early-five", "auto-five":
		return e.fiveCommand(c, at, src)
	case "undo":
		return e.doUndo(src)
	case "replace":
		if c.Replace == nil {
			return fmt.Errorf("replace needs the command to do instead")
		}
		return e.doReplace(*c.Replace, src)
	}
	return e.run(c, at, src)
}

// run does a command as if at time at (earlier than now for replace).
func (e *Engine) run(c Command, at time.Time, src *Source) error {
	pc := func() int64 { return e.cl.c[Period].at(at) }
	jc := func() int64 { return e.cl.c[Jam].at(at) }
	phase := e.phase()
	s := e.rp.Summary()
	var batch []ev
	var creates []string
	started := ""
	label := ""
	undoable := true

	switch c.Name {
	case "start-jam":
		switch phase {
		case PhaseBefore, PhaseLineup, PhaseTimeout, PhaseIntermission, PhaseOvertime, PhaseEnded:
		case PhaseJam:
			return conflict("a jam is already running")
		default:
			return conflict("the game is over")
		}
		if phase == PhaseBefore {
			if u := unsupported(s); len(u) > 0 {
				return conflict("the engine doesn't handle these rules yet: %v", u)
			}
		}
		// There is always an upcoming jam to start, so its lineup can be
		// entered first and an undo of the start leaves it upcoming. A log
		// without one gets it now, outside the undoable step.
		if s.UpcomingJam == nil {
			n := 1
			if p, _ := e.lastJam(); p != nil && !(phase == PhaseIntermission || phase == PhaseBefore) {
				n = e.nextJamNumber(p)
			}
			if _, err := e.commit(at, src, []ev{{"type": "JamUpcoming", "jam": newID("j_"), "number": n}}); err != nil {
				return err
			}
			s = e.rp.Summary()
		}
		period, number := "", 0
		overtime := phase == PhaseOvertime
		p, _ := e.lastJam()
		newPeriod := phase == PhaseBefore
		// After a period has ended, a jam starts the next period, unless
		// less than half the intermission has gone or there is no next
		// period: then the period that ended goes on, as in Java
		// (GameImpl._endIntermission). That's how an operator recovers from
		// a period that ended too early. The period's end is undone.
		if (phase == PhaseIntermission || phase == PhaseEnded || phase == PhaseTimeout) && periodEnded(s) {
			in := &e.cl.c[Intermission]
			if p.Number < rulesOf(s).periods && in.max-in.at(at) < in.at(at) {
				newPeriod = true
			} else {
				seq := e.periodEndSeq(p.ID)
				if seq == 0 {
					return conflict("period %d has ended", p.Number)
				}
				batch = append(batch, ev{"type": "Undone", "reverts": []int{seq}})
				label = fmt.Sprintf(", continuing period %d", p.Number)
			}
		}
		switch {
		case newPeriod:
			period = newID("p_")
			n := len(s.Periods) + 1
			batch = append(batch, ev{"type": "PeriodStarted", "period": period, "number": n, "pc": 0})
			creates = append(creates, period)
			number = e.nextJamNumber(nil)
			if e.suddenScoringAt(s) {
				batch = append(batch, ev{"type": "SuddenScoringSet", "period": period, "value": true})
			}
		default:
			period = p.ID
			number = e.nextJamNumber(p)
		}
		if phase == PhaseTimeout && !rulesOf(s).jamDuringTO {
			to := e.openTimeout()
			batch = append(batch, ev{"type": "TimeoutEnded", "timeout": to.ID, "duration": e.cl.c[Timeout].at(at), "pc": pc()})
		}
		jam := s.UpcomingJam.ID
		startPC := int64(0)
		if !newPeriod {
			startPC = pc()
		}
		start := ev{"type": "JamStarted", "period": period, "jam": jam, "number": number, "pc": startPC}
		if overtime {
			start["overtime"] = true
		}
		// An injury continuation (JRDA) goes on with the jam that was called
		// for an injury: its time left, and its lead jammer still lead (Java
		// JamImpl.start, TeamJamImpl.setupInjuryContinuation).
		var leads []ev
		if c.InjuryContinuation {
			prev := e.lastStartedJam()
			switch {
			case !rulesOf(s).injuryContinuation:
				return conflict("injury continuation isn't in this game's rules (Jam.InjuryContinuation)")
			case newPeriod || prev == nil || prev.EndReason != "injury":
				return conflict("an injury continuation follows a jam that was called for an injury")
			}
			start["injuryContinuation"] = true
			if prev.Overtime {
				start["overtime"] = true
			}
			for _, tj := range prev.Teams {
				if tj.Lead {
					leads = append(leads, ev{"type": "JamFlagSet", "jam": jam, "team": tj.Team, "flag": "lead", "value": true, "jc": 0})
				}
			}
		}
		next := newID("j_")
		batch = append(batch, start)
		batch = append(batch, leads...)
		batch = append(batch, ev{"type": "JamUpcoming", "jam": next, "number": number + 1})
		creates = append(creates, next)
		started = jam
		label = fmt.Sprintf("Start jam %d", number) + label
	case "stop-jam", "call-off", "injury":
		if phase != PhaseJam {
			return conflict("no jam is running")
		}
		reason := map[string]string{"stop-jam": "unknown", "call-off": "calloff", "injury": "injury"}[c.Name]
		if c.Name == "stop-jam" && jc() >= e.cl.c[Jam].max {
			reason = "time" // the Jam Timer's whistle at the end of the jam clock
		}
		j := e.lastStartedJam()
		batch = append(batch, e.jamEnded(j, reason, at, jc(), true)...)
		label = map[string]string{"stop-jam": "Stop jam", "call-off": "Call off", "injury": "Stop jam for injury"}[c.Name] +
			fmt.Sprintf(" %d at %s", j.Number, derive.Clock(jc()))
	case "timeout":
		// As in Java: during a jam it ends the jam, during a timeout it ends
		// that one and starts a new one, and after a period it's a timeout
		// of that period (an official review after the last jam).
		switch phase {
		case PhaseJam, PhaseLineup, PhaseTimeout, PhaseIntermission, PhaseEnded:
		default:
			return conflict("a timeout can't start now (%s)", phase)
		}
		j := e.lastStartedJam()
		if j == nil {
			return conflict("no jam has been played yet, so there's no jam for the timeout to follow")
		}
		switch phase {
		case PhaseJam:
			batch = append(batch, e.jamEnded(j, "official", at, jc(), false)...)
		case PhaseTimeout:
			old := e.openTimeout()
			batch = append(batch, ev{"type": "TimeoutEnded", "timeout": old.ID, "duration": e.cl.c[Timeout].at(at), "pc": pc()})
		}
		to := newID("t_")
		batch = append(batch, ev{"type": "TimeoutStarted", "timeout": to, "afterJam": j.ID, "owner": "", "pc": pc()})
		creates = append(creates, to)
		label = "Timeout at " + derive.Clock(e.cl.remaining(Period, at))
	case "end-timeout":
		if phase != PhaseTimeout {
			return conflict("no timeout is running")
		}
		to := e.openTimeout()
		batch = append(batch, ev{"type": "TimeoutEnded", "timeout": to.ID, "duration": e.cl.c[Timeout].at(at), "pc": pc()})
		if e.periodOver(at) && !periodEnded(s) {
			batch = append(batch, e.periodEnded(at))
		}
		label = "End timeout"
	case "timeout-type":
		id := c.Timeout
		if id == "" {
			if to := e.openTimeout(); to != nil {
				id = to.ID
			} else if to := e.lastTimeout(); to != nil {
				id = to.ID
			} else {
				return conflict("there is no timeout")
			}
		}
		u := ev{"type": "TimeoutUpdated", "timeout": id}
		if c.Owner != nil {
			u["owner"] = *c.Owner
		}
		if c.Review != nil {
			u["review"] = *c.Review
		}
		if c.AsTO != nil {
			u["asTimeout"] = *c.AsTO
		}
		batch = append(batch, u)
		undoable = false
	case "jam-reason":
		id := c.Jam
		if id == "" {
			j := e.lastStartedJam()
			switch {
			case j == nil:
				return conflict("no jam has been played yet")
			case phase == PhaseJam:
				return conflict("the jam hasn't ended yet")
			}
			id = j.ID
		}
		u := ev{"type": "JamUpdated", "jam": id, "reason": c.Reason}
		if c.Detail != "" {
			u["detail"] = c.Detail
		}
		batch = append(batch, u)
		undoable = false
	case "sudden-scoring":
		p, _ := e.lastJam()
		if p == nil || c.Value == nil {
			return conflict("sudden scoring is set for a period that has started, with a value")
		}
		batch = append(batch, ev{"type": "SuddenScoringSet", "period": p.ID, "value": *c.Value})
		undoable = false
	case "start-overtime":
		if phase != PhaseEnded {
			return conflict("overtime can only start after the last period")
		}
		batch = append(batch, ev{"type": "OvertimeStarted"})
		label = "Start overtime"
	case "lineup":
		// Java's Stop button says "Lineup" when no clock runs: before the
		// first jam and in an intermission.
		switch {
		case phase != PhaseBefore && phase != PhaseIntermission:
			return conflict("the lineup clock starts by itself after a jam or timeout (%s)", phase)
		case e.cl.c[Lineup].running:
			return conflict("the lineup clock is running already")
		}
		batch = append(batch, ev{"type": "LineupStarted"})
		label = "Lineup"
	case "set-clock":
		i := clockIndex(c.Clock)
		if i < 0 || c.Time == nil {
			return fmt.Errorf("set-clock needs clock (%v) and time", ClockNames)
		}
		batch = append(batch, ev{"type": "ClockSet", "clock": c.Clock, "time": *c.Time})
		label = fmt.Sprintf("Set the %s clock to %s", c.Clock, derive.Clock(*c.Time))
	case "end-early":
		if phase == PhaseOver {
			return conflict("the game is over")
		}
		g := ev{"type": "GameEndedEarly", "outcome": c.Outcome, "reason": c.Text, "pc": pc()}
		if c.ForfeitingTeam != "" {
			g["forfeitingTeam"] = c.ForfeitingTeam
		}
		batch = append(batch, g)
		undoable = false
	case "official-score":
		if phase != PhaseEnded {
			return conflict("the official score can only be declared after the last period")
		}
		if c.Score == nil {
			return fmt.Errorf("official-score needs the score")
		}
		d := ev{"type": "ScoreDeclaredOfficial", "score": c.Score}
		if c.HR != "" {
			d["hr"] = c.HR
		}
		if c.HNSO != "" {
			d["hnso"] = c.HNSO
		}
		batch = append(batch, d)
		undoable = false
	default:
		return fmt.Errorf("unknown command %q", c.Name)
	}

	seqs, err := e.commit(at, src, batch)
	if err != nil {
		return err
	}
	if undoable {
		if c.Name == "start-jam" {
			e.undo = nil // undo goes back to the start of the current jam at most
		}
		e.undo = append(e.undo, &step{label: label, seqs: seqs, at: at, creates: creates, started: started, cmd: c.Name})
	}
	return nil
}

// periodEndSeq is the seq of the PeriodEnded that ended period id, 0 if none.
func (e *Engine) periodEndSeq(id string) int {
	skip, _ := replay.UndoneSeqs(e.events)
	for i := len(e.events) - 1; i >= 0; i-- {
		if x := e.events[i]; x.Type == "PeriodEnded" && x.Period == id && !skip[x.Seq] {
			return x.Seq
		}
	}
	return 0
}

// suddenScoringAt says whether a new period starts in sudden scoring (JRDA,
// Java GameImpl._preparePeriod): the score gap is at least
// Jam.SuddenScoringMinPointsDifference and the trailing team has at most
// Jam.SuddenScoringMaxTrailingPoints; once in sudden scoring, later periods
// stay in it.
func (e *Engine) suddenScoringAt(s *replay.Summary) bool {
	r := rulesOf(s)
	if !r.suddenScoring {
		return false
	}
	if n := len(s.Periods); n > 0 && s.Periods[n-1].SuddenScoring {
		return true
	}
	sc := derive.Scores(s)
	diff, trailing := sc[0]-sc[1], min(sc[0], sc[1])
	if diff < 0 {
		diff = -diff
	}
	return diff >= r.ssMinDiff && trailing <= r.ssMaxTrailing
}

func (e *Engine) nextJamNumber(p *replay.Period) int {
	s := e.rp.Summary()
	if rulesOf(s).resetJamNumbers {
		if p == nil {
			return 1
		}
		return len(p.Jams) + 1
	}
	n := 0
	for _, q := range s.Periods {
		n += len(q.Jams)
	}
	return n + 1
}

func (e *Engine) lastTimeout() *replay.Timeout {
	s := e.rp.Summary()
	for i := len(s.Periods) - 1; i >= 0; i-- {
		if ts := s.Periods[i].Timeouts; len(ts) > 0 {
			return ts[len(ts)-1]
		}
	}
	return nil
}

// --- undo and replace -----------------------------------------------------------

// blocked says why the top undo step can't be undone, or "". Data entered
// since blocks it if it would be lost or left somewhere it makes no sense:
// anything about what the step created (a lineup for the next jam, the
// timeout's type), or scores and flags in a jam that would no longer have
// started. A lineup entered for the jam itself doesn't: the jam becomes the
// upcoming jam again, with its lineup.
func (e *Engine) blocked(st *step) string {
	last := st.seqs[len(st.seqs)-1]
	skip, _ := replay.UndoneSeqs(e.events)
	for _, x := range e.events[last:] {
		if skip[x.Seq] || x.Type == "Undone" || x.Type == "Checkpoint" {
			continue
		}
		raw, _ := json.Marshal(x)
		uses := func(id string) bool { return id != "" && bytes.Contains(raw, []byte(`"`+id+`"`)) }
		for _, id := range st.creates {
			if uses(id) {
				return fmt.Sprintf("%s has been entered since (seq %d)", describe(x.Type), x.Seq)
			}
		}
		if uses(st.started) {
			switch x.Type {
			case "TripStarted", "TripPointsSet", "TripInserted", "JamFlagSet", "StarPassSet", "OsOffsetSet":
				return fmt.Sprintf("%s has been entered in that jam since (seq %d)", describe(x.Type), x.Seq)
			}
		}
	}
	// And the log must still replay without the step.
	events := append(append([]*replay.Event(nil), e.events...),
		&replay.Event{V: 1, Seq: len(e.events) + 1, Type: "Undone", Reverts: st.seqs})
	x := &Engine{events: events, times: append(append([]time.Time(nil), e.times...), e.time())}
	if err := x.rebuild(); err != nil {
		return "the game wouldn't be consistent without it: " + err.Error()
	}
	return ""
}

func describe(typ string) string {
	switch typ {
	case "FieldingSet", "LineupCopied":
		return "a lineup"
	case "TripStarted", "TripPointsSet", "TripRemoved", "TripInserted":
		return "a scoring trip"
	case "JamFlagSet", "StarPassSet":
		return "a lead, lost, call-off or star pass mark"
	case "PenaltyIssued", "PenaltyUpdated":
		return "a penalty"
	case "BoxTripStarted", "BoxTripEnded", "BoxTripMoved":
		return "a box trip"
	case "TimeoutUpdated", "OfficialReviewResolved":
		return "the timeout type"
	}
	return "an event (" + typ + ")"
}

func (e *Engine) undoView() *UndoView {
	if len(e.undo) == 0 {
		return nil
	}
	st := e.undo[len(e.undo)-1]
	v := &UndoView{Label: st.label, Blocked: e.blocked(st)}
	v.BackTo = e.preview(st)
	return v
}

// preview describes the game as it would be after undoing st.
func (e *Engine) preview(st *step) string {
	events := append([]*replay.Event(nil), e.events...)
	events = append(events, &replay.Event{V: 1, Seq: len(events) + 1, Type: "Undone", Reverts: st.seqs})
	times := append(append([]time.Time(nil), e.times...), e.time())
	x := &Engine{events: events, times: times, now: e.now, wall0: e.wall0, mono0: e.mono0, settings: e.settings}
	if err := x.rebuild(); err != nil {
		return "(can't tell: " + err.Error() + ")"
	}
	t := e.time()
	out := x.phase()
	var running []string
	for i := range x.cl.c {
		c := &x.cl.c[i]
		if c.running && i != Intermission {
			running = append(running, fmt.Sprintf("%s clock running at %s", ClockNames[i], derive.Clock(x.cl.remaining(i, t))))
		}
	}
	sort.Strings(running)
	for _, r := range running {
		out += ", " + r
	}
	return out
}

func (e *Engine) doUndo(src *Source) error {
	if len(e.undo) == 0 {
		return conflict("there is nothing to undo")
	}
	st := e.undo[len(e.undo)-1]
	if b := e.blocked(st); b != "" {
		return conflict("can't undo %s: %s", st.label, b)
	}
	if err := e.undoStep(st, src); err != nil {
		return err
	}
	e.undo = e.undo[:len(e.undo)-1]
	return nil
}

// undoStep writes the Undone for a step. It carries no clock values: with
// the step's events skipped, every clock follows from the events before it,
// running on from where it was, as if the step never happened. (Values
// written into an Undone would also be replayed after a later undo, when they
// no longer apply.)
func (e *Engine) undoStep(st *step, src *Source) error {
	_, err := e.commit(e.time(), src, []ev{{"type": "Undone", "reverts": st.seqs}})
	return err
}

// doReplace undoes the last step and does c instead, at the time of the
// step, so a jam stopped with Timeout by mistake ends when it really ended.
func (e *Engine) doReplace(c Command, src *Source) error {
	if len(e.undo) == 0 {
		return conflict("there is nothing to replace")
	}
	st := e.undo[len(e.undo)-1]
	if b := e.blocked(st); b != "" {
		return conflict("can't replace %s: %s", st.label, b)
	}
	saved := len(e.events)
	if err := e.undoStep(st, src); err != nil {
		return err
	}
	e.undo = e.undo[:len(e.undo)-1]
	if err := e.run(c, st.at, src); err != nil {
		// The undo is written; say so rather than pretend nothing happened.
		return fmt.Errorf("undid %s (seq %d), but %s failed: %w", st.label, saved+1, c.Name, err)
	}
	return nil
}

// --- data entry -------------------------------------------------------------------

// Post appends data events (trips, lineups, penalties, …) the way the review
// screen does. Clock fields an event type needs are filled in with the
// current clocks when the client leaves them out.
func (e *Engine) Post(events []map[string]any, src *Source) error {
	at := e.time()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.readOnly != "" {
		return conflict("%s", e.readOnly)
	}
	if err := e.advanceTo(at); err != nil {
		return err
	}
	if len(events) == 0 {
		return errors.New("no events")
	}
	batch := make([]ev, len(events))
	for i, fields := range events {
		typ, _ := fields["type"].(string)
		if !e.v.KnownEvent(typ) {
			return fmt.Errorf("unknown event type %q", typ)
		}
		switch typ {
		case "GameCreated", "Undone", "Checkpoint", "PeriodStarted", "JamStarted", "JamEnded", "TimeoutStarted",
			"TimeoutEnded", "PeriodEnded", "OvertimeStarted", "LineupStarted", "ClockSet", "JamUpcoming":
			return conflict("%s is written by the engine; use the matching command", typ)
		}
		b := ev{}
		for k, v := range fields {
			b[k] = v
		}
		if raw, ok := b["number"].(string); ok && (typ == "SkaterAdded" || typ == "SkaterUpdated") {
			n, notSkating, err := derive.SkaterNumber(raw)
			if err != nil {
				return err
			}
			b["number"] = n
			if notSkating {
				b["flags"] = "ALT"
			}
		}
		for _, name := range e.v.RequiredClocks(typ) {
			if _, ok := b[name]; !ok {
				b[name] = map[string]int64{"pc": e.cl.c[Period].at(at), "jc": e.cl.c[Jam].at(at)}[name]
			}
		}
		batch[i] = b
	}
	if e.skipTripEvents(batch, at) {
		return nil
	}
	before := e.tripPointsBefore(batch)
	if _, err := e.commit(at, src, batch); err != nil {
		return err
	}
	e.noteTrips(batch, before, at)
	return nil
}

// --- settings and checkpoints ---------------------------------------------------

// SetSettings changes the server settings. They take effect at once.
func (e *Engine) SetSettings(s Settings) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.settings = s
}

// SetReadOnly makes the game read-only (why says where it's changed
// instead), or writable again with "".
func (e *Engine) SetReadOnly(why string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.readOnly = why
}

// Checkpoint writes a Checkpoint with the hash of the game as it stands
// (README "Annotations and checkpoints") and calls f with that game, for
// writing the summary file while nothing can change it.
func (e *Engine) Checkpoint(f func(*replay.Summary) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.readOnly != "" {
		return nil // the track that runs the game writes its checkpoints
	}
	s := e.rp.Summary()
	h, err := replay.SummaryHash(s)
	if err != nil {
		return err
	}
	if f != nil {
		if err := f(s); err != nil {
			return err
		}
	}
	_, err = e.commit(e.time(), &Source{Role: "server"}, []ev{{"type": "Checkpoint", "summarySha256": h}})
	return err
}
