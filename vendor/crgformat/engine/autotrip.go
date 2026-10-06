package engine

import (
	"time"

	"crgformat/replay"
)

// Points entered on a team's current trip during a jam end that trip: the
// next one starts by itself Settings.AutoTrip seconds later, so the
// operator doesn't have to press Add Trip (Java TeamImpl, TRIP_SCORE, which
// waits 4 s). New points in the meantime start the wait again. As in Java:
//   - setting a trip that has 0 points to 0 counts as entering points (a
//     trip without points); going down to 0 with -1 doesn't;
//   - Remove Trip while the next trip is still to come only stops it coming;
//   - Add Trip within a second of an automatic one is the operator being a
//     moment late, and is ignored if nothing was entered on the new trip.

type tripAdvance struct {
	jam, team, trip string
	jc              int64 // the jam clock when the trip ended: the next one's start
	due             time.Time
}

type autoTrip struct {
	jam, team, trip string // the trip it started
	at              time.Time
}

// tripOf finds a trip of a jam that has started, with its team jam.
func tripOf(s *replay.Summary, id string) (*replay.Jam, *replay.TeamJam, *replay.Trip) {
	for _, p := range s.Periods {
		for _, j := range p.Jams {
			for _, tj := range j.Teams {
				for _, t := range tj.Trips {
					if t.ID == id {
						return j, tj, t
					}
				}
			}
		}
	}
	return nil, nil, nil
}

// tripPointsBefore is each trip's points before a batch, for noteTrips.
func (e *Engine) tripPointsBefore(batch []ev) map[string]int {
	out := map[string]int{}
	for _, b := range batch {
		if b["type"] != "TripPointsSet" {
			continue
		}
		id, _ := b["trip"].(string)
		if _, _, t := tripOf(e.rp.Summary(), id); t != nil {
			out[id] = t.Points
		}
	}
	return out
}

// skipTripEvents handles the two Java cases in which an operator's trip
// event does nothing; true means don't write it.
func (e *Engine) skipTripEvents(batch []ev, at time.Time) bool {
	if e.settings.AutoTrip <= 0 || len(batch) != 1 {
		return false
	}
	b := batch[0]
	switch b["type"] {
	case "TripRemoved":
		id, _ := b["trip"].(string)
		for k, a := range e.tripAdv {
			if a.trip == id {
				delete(e.tripAdv, k)
				return true
			}
		}
	case "TripStarted":
		jam, _ := b["jam"].(string)
		team, _ := b["team"].(string)
		a := e.lastAutoTrip[jam+"/"+team]
		if a == nil || at.Sub(a.at) >= time.Second {
			return false
		}
		_, tj, t := tripOf(e.rp.Summary(), a.trip)
		return t != nil && t.Points == 0 && tj.Trips[len(tj.Trips)-1] == t
	}
	return false
}

// noteTrips starts the wait for the next trip after points were entered.
func (e *Engine) noteTrips(batch []ev, before map[string]int, at time.Time) {
	if e.settings.AutoTrip <= 0 || e.phase() != PhaseJam {
		return
	}
	s := e.rp.Summary()
	running := e.lastStartedJam()
	for _, b := range batch {
		if b["type"] != "TripPointsSet" {
			continue
		}
		id, _ := b["trip"].(string)
		j, tj, t := tripOf(s, id)
		old, known := before[id]
		if t != nil {
			delete(e.tripAdv, j.ID+"/"+tj.Team) // any change starts the wait again, or stops it
		}
		switch {
		case t == nil || j != running || tj.Trips[len(tj.Trips)-1] != t:
			continue // not the current trip of the running jam
		case t.Points == 0 && !(known && old == 0):
			continue
		case len(tj.Trips) == 1 && !j.Overtime:
			continue // the initial trip: screens start trip 2 for the points
		}
		jc := e.cl.c[Jam].at(at)
		if t.JamClockEnd != nil {
			jc = *t.JamClockEnd
		}
		if e.tripAdv == nil {
			e.tripAdv = map[string]*tripAdvance{}
		}
		e.tripAdv[j.ID+"/"+tj.Team] = &tripAdvance{jam: j.ID, team: tj.Team, trip: id, jc: jc,
			due: at.Add(time.Duration(e.settings.AutoTrip) * time.Second)}
	}
}

// nextTrip is the trip advance due first.
func (e *Engine) nextTrip() (string, *tripAdvance) {
	key, first := "", (*tripAdvance)(nil)
	for k, a := range e.tripAdv {
		if first == nil || a.due.Before(first.due) {
			key, first = k, a
		}
	}
	return key, first
}

// advanceTrip starts the next trip, if the trip whose points were entered
// is still the current one of the running jam.
func (e *Engine) advanceTrip(at time.Time) error {
	key, a := e.nextTrip()
	delete(e.tripAdv, key)
	_, tj, t := tripOf(e.rp.Summary(), a.trip)
	if e.phase() != PhaseJam || t == nil || e.lastStartedJam().ID != a.jam || tj.Trips[len(tj.Trips)-1] != t {
		return nil
	}
	id := newID("t_")
	_, err := e.commit(at, &Source{Role: "auto"}, []ev{{"type": "TripStarted", "jam": a.jam, "team": a.team, "trip": id,
		"afterStarPass": tj.StarPassTrip != "", "jc": a.jc}})
	if err != nil {
		return err
	}
	if e.lastAutoTrip == nil {
		e.lastAutoTrip = map[string]*autoTrip{}
	}
	e.lastAutoTrip[key] = &autoTrip{jam: a.jam, team: a.team, trip: id, at: at}
	return nil
}
