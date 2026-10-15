package engine

import (
	"crgformat/derive"
	"crgformat/replay"
)

// Commands for what happens on track that every screen records the same
// way: a skater going to or leaving the box, and points. The screens used
// to build these events themselves, each its own way (PLT, PBT and the SBO
// differed on linking penalties, and none marked a star pass).
//
//   - box-start (team, skater and/or position, or neither with jammer for
//     "Jammer/Blocker Sat Down"): a box trip starting now, in
//     the running jam or else between jams before the next one, after the
//     team's star pass if there was one; the skater's penalties not served
//     yet are served on it.
//   - box-end (boxTrip): the box trip ending now, likewise.
//   - box-assign (boxTrip, position and/or skater): who sat in a trip
//     started without knowing; their unserved penalties are served on it.
//   - trip-points (team, points or set): points added to (or taken off)
//     the team's current trip of the last jam, or its points set; points on
//     the initial trip start trip 2 instead, except in overtime (as Java).

func (e *Engine) trackCommand(c Command) ([]ev, error) {
	s := e.rp.Summary()
	switch c.Name {
	case "box-start":
		if teamOf(s, c.Team) == nil {
			return nil, conflict("box-start needs the team (\"1\" or \"2\")")
		}
		jam := e.lineupJam()
		if jam == nil {
			return nil, conflict("there is no jam to sit out")
		}
		skater, position := c.Skater, c.Position
		if tj := teamJamOf(jam, c.Team); tj != nil {
			if f := tj.Lineup[position]; skater == "" && position != "" && f != nil {
				skater = f.Skater
			}
			if position == "" && skater != "" {
				for _, p := range derive.Positions {
					if f := tj.Lineup[p]; f != nil && f.Skater == skater {
						position = p
					}
				}
			}
		}
		// Neither skater nor position: Java's "Blocker Sat Down" / "Jammer
		// Sat Down", before the box timer knows who (BoxTripAssigned later).
		if skater != "" {
			for _, bt := range s.BoxTrips {
				if bt.End == nil && bt.Skater == skater {
					return nil, conflict("that skater is in the box already")
				}
			}
		}
		id := newID("b_")
		b := e.boxPoint(true, c.Team)
		b["type"], b["boxTrip"], b["team"], b["jammer"] = "BoxTripStarted", id, c.Team, position == "Jammer" || (position == "" && c.Jammer)
		if skater != "" {
			b["skater"] = skater
		}
		if position != "" {
			b["position"] = position
		}
		batch := []ev{b}
		// The box timer named who sat at a position the lineup has someone
		// else (or no one) at: the lineup gets them (Java's PBT Pre-Select).
		if c.Skater != "" && c.Position != "" {
			if tj := teamJamOf(jam, c.Team); tj != nil && (tj.Lineup[c.Position] == nil || tj.Lineup[c.Position].Skater != c.Skater) {
				batch = append([]ev{{"type": "FieldingSet", "jam": jam.ID, "team": c.Team, "position": c.Position, "skater": c.Skater}}, batch...)
			}
		}
		for _, p := range unserved(s, skater) {
			batch = append(batch, ev{"type": "BoxTripPenaltyLinked", "boxTrip": id, "penalty": p, "value": true})
		}
		return batch, nil

	case "box-end":
		for _, bt := range s.BoxTrips {
			if bt.ID != c.BoxTrip {
				continue
			}
			if bt.End != nil {
				return nil, conflict("that box trip has ended already")
			}
			b := e.boxPoint(false, bt.Team)
			b["type"], b["boxTrip"] = "BoxTripEnded", bt.ID
			return []ev{b}, nil
		}
		return nil, conflict("there is no box trip %q", c.BoxTrip)

	case "box-assign":
		// Who sat in a trip started without knowing: its position (as
		// Java's trip row) and/or its skater, who goes into the lineup at
		// that position (the trip takes the lineup's skater, README "Box
		// trips"); their penalties not served yet are served on it.
		var bt *replay.BoxTrip
		for _, x := range s.BoxTrips {
			if x.ID == c.BoxTrip {
				bt = x
			}
		}
		if bt == nil {
			return nil, conflict("there is no box trip %q", c.BoxTrip)
		}
		position := c.Position
		if position == "" {
			position = bt.Start.Position
		}
		if position == "" {
			return nil, conflict("box-assign needs the position the skater sat from")
		}
		var batch []ev
		if position != bt.Start.Position {
			batch = append(batch, ev{"type": "BoxTripAssigned", "boxTrip": bt.ID, "position": position})
		}
		skater := c.Skater
		if skater != "" {
			batch = append(batch, ev{"type": "FieldingSet", "jam": bt.Start.Jam, "team": bt.Team, "position": position, "skater": skater})
		} else if tj := teamJamOf(summaryJam(s, bt.Start.Jam), bt.Team); tj != nil && tj.Lineup[position] != nil {
			skater = tj.Lineup[position].Skater
		}
		if skater != "" && bt.Skater == "" {
			for _, p := range unserved(s, skater) {
				batch = append(batch, ev{"type": "BoxTripPenaltyLinked", "boxTrip": bt.ID, "penalty": p, "value": true})
			}
		}
		if len(batch) == 0 {
			return nil, conflict("nothing to change")
		}
		return batch, nil

	case "trip-points":
		if teamOf(s, c.Team) == nil || (c.Points == 0 && c.Set == nil) {
			return nil, conflict("trip-points needs the team (\"1\" or \"2\") and the points to add or set")
		}
		points := func(old int) int {
			if c.Set != nil {
				return max(0, *c.Set)
			}
			return max(0, old+c.Points)
		}
		j := e.lastStartedJam()
		if j == nil {
			return nil, conflict("no jam has been played yet")
		}
		tj := teamJamOf(j, c.Team)
		if tj == nil || len(tj.Trips) == 0 {
			return nil, conflict("the jam has no trips for team %s", c.Team)
		}
		cur := tj.Trips[len(tj.Trips)-1]
		if len(tj.Trips) == 1 && !j.Overtime {
			trip := newID("t_")
			return []ev{
				{"type": "TripStarted", "jam": j.ID, "team": c.Team, "trip": trip, "afterStarPass": tj.StarPassTrip != ""},
				{"type": "TripPointsSet", "trip": trip, "points": points(0)},
			}, nil
		}
		return []ev{{"type": "TripPointsSet", "trip": cur.ID, "points": points(cur.Points)}}, nil
	}
	return nil, conflict("unknown command %q", c.Name)
}

// boxPoint is where a box trip starts or ends now: in the running jam
// (after the team's star pass if there was one), else between jams: a start
// before the next jam, an end after the last one.
func (e *Engine) boxPoint(start bool, team string) ev {
	if e.phase() == PhaseJam {
		j := e.lastStartedJam()
		tj := teamJamOf(j, team)
		return ev{"jam": j.ID, "betweenJams": false, "afterStarPass": tj != nil && tj.StarPassTrip != ""}
	}
	j := e.lastStartedJam()
	if start || j == nil {
		j = e.lineupJam()
	}
	return ev{"jam": j.ID, "betweenJams": true, "afterStarPass": false}
}

// lineupJam is the jam whose lineup the screens set: the running jam, else
// the next one, else the last.
func (e *Engine) lineupJam() *replay.Jam {
	if e.phase() != PhaseJam {
		if u := e.rp.Summary().UpcomingJam; u != nil {
			return u
		}
	}
	return e.lastStartedJam()
}

// summaryJam is a started or the upcoming jam.
func summaryJam(s *replay.Summary, id string) *replay.Jam {
	if s.UpcomingJam != nil && s.UpcomingJam.ID == id {
		return s.UpcomingJam
	}
	for _, p := range s.Periods {
		for _, j := range p.Jams {
			if j.ID == id {
				return j
			}
		}
	}
	return nil
}

func teamOf(s *replay.Summary, team string) *replay.Team {
	for _, t := range s.Teams {
		if t.Team == team {
			return t
		}
	}
	return nil
}

func teamJamOf(j *replay.Jam, team string) *replay.TeamJam {
	if j == nil {
		return nil
	}
	for _, tj := range j.Teams {
		if tj.Team == team {
			return tj
		}
	}
	return nil
}

// unserved are a skater's penalties not on a box trip yet (and not served
// otherwise).
func unserved(s *replay.Summary, skater string) []string {
	if skater == "" {
		return nil
	}
	on := map[string]bool{}
	for _, bt := range s.BoxTrips {
		for _, p := range bt.Penalties {
			on[p] = true
		}
	}
	var out []string
	for _, p := range s.Penalties {
		if p.Skater == skater && !on[p.ID] && !p.ForceServed {
			out = append(out, p.ID)
		}
	}
	return out
}
