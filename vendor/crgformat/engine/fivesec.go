package engine

import (
	"fmt"
	"time"
)

// The electronic jam timer's lineup (Java's eJT, GameImpl._possiblyAutostart):
//
//   - "5 Seconds" is the Jam Timer's five-second call: screens show 5, 4, 3,
//     2, 1 instead of the lineup clock. Pressed during a timeout it ends the
//     timeout, as in Java.
//   - Settings.Auto5 calls it by itself 5 s before the lineup time is up.
//   - "Jam in N" (early-five) calls it after Settings.Early5Delay seconds and
//     starts the jam 5 s later.
//   - Settings.AutoStart and AutoStart5 start a jam or a timeout when the
//     lineup time is up: AutoStart5 when five seconds was called,
//     AutoStart when it wasn't.
//
// All are off by default: the log records what happens on track, and a jam
// starts when someone presses Start Jam. The five-second call isn't a game
// event (it's not on the paperwork), so it lives here, per lineup, and is
// gone after a restart; an automatic start is an ordinary Start Jam or
// Timeout (source role "clock"), which Undo takes back like any other.
type lineupPlan struct {
	anchor   time.Time // the lineup clock's start: a new one is a new lineup
	five     bool      // five seconds called
	autoFive bool      // call it by itself at fiveAt
	forceJam bool      // "Jam in N": start the jam, whatever AutoStart5 says
	fiveAt   int64     // lineup clock ms
	startAt  int64     // lineup clock ms; 0 for no automatic start
	done     bool      // the automatic start happened (or was refused)
}

// AutoStart values.
const (
	AutoStartOff     = ""
	AutoStartJam     = "jam"
	AutoStartTimeout = "timeout"
)

// FiveView is the five-second call as screens show it.
type FiveView struct {
	On       bool  `json:"on"`
	StartsIn int64 `json:"startsInMs"` // until the automatic start, or the end of the lineup time
	Auto     bool  `json:"auto"`       // Auto 5s is on for this lineup
}

// syncPlan starts a new plan when a new lineup has started. Call it after
// the clocks change, with the time of the change. A lineup that is already
// past its points (back after an undo of an automatic start, or after a
// restart) doesn't do them again.
func (e *Engine) syncPlan(at time.Time) {
	l := &e.cl.c[Lineup]
	if !l.running {
		e.plan = lineupPlan{}
		return
	}
	if !e.plan.anchor.IsZero() && e.plan.anchor.Equal(l.anchor) {
		return
	}
	keepFive := e.plan.five && e.plan.anchor.IsZero() // five seconds called in the timeout just ended
	e.plan = lineupPlan{anchor: l.anchor, autoFive: e.settings.Auto5, startAt: l.max, fiveAt: l.max - 5000}
	e.plan.five = keepFive
	if e.plan.fiveAt < 0 {
		e.plan.fiveAt = 0
	}
	now := l.at(at)
	if now > e.plan.fiveAt && e.plan.autoFive {
		e.plan.five = true
	}
	if now > e.plan.startAt {
		e.plan.done = true
	}
}

// lineupAt is the moment the lineup clock shows ms.
func (e *Engine) lineupAt(ms int64) time.Time {
	l := &e.cl.c[Lineup]
	return l.anchor.Add(time.Duration(ms-l.base) * time.Millisecond)
}

// autoStartKind is what the automatic start does now: "", jam or timeout.
func (e *Engine) autoStartKind() string {
	switch {
	case e.plan.forceJam:
		return AutoStartJam
	case e.plan.five:
		return e.settings.AutoStart5
	}
	return e.settings.AutoStart
}

func (e *Engine) fiveView(t time.Time) *FiveView {
	l := &e.cl.c[Lineup]
	if !l.running || !(e.plan.five || e.plan.autoFive) {
		return nil
	}
	v := &FiveView{On: e.plan.five, Auto: e.plan.autoFive}
	if e.plan.startAt > 0 {
		v.StartsIn = max(0, e.plan.startAt-l.at(t))
	}
	return v
}

// fiveCommand handles five-seconds, early-five and auto-five.
func (e *Engine) fiveCommand(c Command, at time.Time, src *Source) error {
	phase := e.phase()
	switch c.Name {
	case "five-seconds":
		on := !e.plan.five
		if c.Value != nil {
			on = *c.Value
		}
		switch {
		case phase == PhaseJam:
			return conflict("five seconds is called between jams")
		case phase == PhaseTimeout && on:
			// Java: five seconds during a timeout ends the timeout.
			e.plan = lineupPlan{five: true}
			if err := e.run(Command{Name: "end-timeout"}, at, src); err != nil {
				e.plan = lineupPlan{}
				return err
			}
			e.syncPlan(at)
			return nil
		case !e.cl.c[Lineup].running:
			return conflict("five seconds is called in a lineup")
		}
		e.plan.five = on
		return nil
	case "early-five":
		if !e.cl.c[Lineup].running {
			return conflict("\"Jam in\" is for a lineup")
		}
		now := e.cl.c[Lineup].at(at)
		e.plan.five, e.plan.autoFive, e.plan.forceJam, e.plan.done = false, true, true, false
		e.plan.fiveAt = now + int64(e.settings.Early5Delay)*1000
		e.plan.startAt = e.plan.fiveAt + 5000
		return nil
	case "auto-five":
		if !e.cl.c[Lineup].running {
			return conflict("Auto 5 is for a lineup")
		}
		e.plan.autoFive = c.Value == nil || *c.Value
		return nil
	}
	return fmt.Errorf("unknown command %q", c.Name)
}

// syncClocks moves the running jam, lineup and timeout clocks as shown by
// less than half a second, so they turn over their seconds with the period
// clock (Java ClockImpl.UpdateClockTimerTask.addClock).
func syncClocks(cs []ClockView) {
	var master *ClockView
	for i := range cs {
		if cs[i].Name == "period" && cs[i].Running {
			master = &cs[i]
		}
	}
	if master == nil {
		return
	}
	for i := range cs {
		c := &cs[i]
		if !c.Running || c.Name == "period" || c.Name == "intermission" {
			continue
		}
		d := (master.Elapsed - c.Elapsed) % 1000
		if d < 0 {
			d += 1000
		}
		if d >= 500 {
			d -= 1000
		}
		c.Elapsed = max(0, c.Elapsed+d)
		if c.Max > 0 {
			c.Elapsed = min(c.Elapsed, c.Max)
			c.Remaining = c.Max - c.Elapsed
		} else {
			c.Remaining = c.Elapsed
		}
	}
}
