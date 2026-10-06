package legacy

import (
	"fmt"

	"crgformat/replay"
)

// check compares the replayed summary with what the Java version stored and
// calculated. Each difference is one line; none means the conversion kept
// everything these checks cover.
func check(c *converter, s *replay.Summary) []string {
	var out []string
	diff := func(where, what string, java, got any) {
		if fmt.Sprint(java) != fmt.Sprint(got) {
			out = append(out, fmt.Sprintf("%s: %s is %v in the Java file, %v after replay", where, what, java, got))
		}
	}

	jams := map[string]*replay.Jam{}
	periods := map[string]*replay.Period{}
	for _, p := range s.Periods {
		periods[p.ID] = p
		for _, j := range p.Jams {
			jams[j.ID] = j
		}
	}

	var totals [2]int64
	for _, p := range c.periods {
		pid := p.str("Id")
		sp := periods[pid]
		if sp == nil {
			out = append(out, fmt.Sprintf("period %s: missing after replay", p.id))
			continue
		}
		javaJams := p.list("Jam")
		diff("period "+p.id, "jam count", len(javaJams), len(sp.Jams))
		timeouts := 0
		for _, to := range p.list("Timeout") {
			// noTimeout is a placeholder; a timeout without a preceding jam is
			// an empty entry the converter notes and leaves out.
			if to.id != "noTimeout" && to.str("PrecedingJam") != "" {
				timeouts++
			}
		}
		diff("period "+p.id, "timeout count", timeouts, len(sp.Timeouts))

		for _, jn := range javaJams {
			where := fmt.Sprintf("period %s jam %s", p.id, jn.id)
			sj := jams[jn.str("Id")]
			if sj == nil {
				out = append(out, where+": missing after replay")
				continue
			}
			diff(where, "number", jn.int("Number"), sj.Number)
			for i, tid := range []string{"1", "2"} {
				tj, st := jn.kid("TeamJam", tid), sj.Teams[i]
				if tj == nil {
					continue
				}
				w := where + " team " + tid
				points := int64(st.OsOffset)
				for _, tr := range st.Trips {
					points += int64(tr.Points)
				}
				totals[i] += points
				diff(w, "jam score", tj.int("JamScore")+tj.int("OsOffset"), points)
				diff(w, "lead", tj.bool("Lead"), st.Lead)
				diff(w, "lost", tj.bool("Lost"), st.Lost)
				diff(w, "calloff", tj.bool("Calloff"), st.Calloff)
				diff(w, "injury (jam ended for injury)", tj.bool("Injury"), sj.EndReason == "injury")
				diff(w, "no initial (only the initial trip)", tj.bool("NoInitial"), len(st.Trips) == 1)
				diff(w, "no pivot", tj.bool("NoPivot"), st.NoPivot)
				diff(w, "star pass", tj.bool("StarPass"), st.StarPassTrip != "")

				trips := tj.list("ScoringTrip")
				diff(w, "trip count", len(trips), len(st.Trips))
				for k, tr := range trips {
					if k >= len(st.Trips) {
						break
					}
					got := st.Trips[k]
					tw := fmt.Sprintf("%s trip %s", w, tr.id)
					diff(tw, "points", tr.int("Score"), got.Points)
					diff(tw, "after star pass", tr.bool("AfterSP"), got.AfterStarPass)
					diff(tw, "jam clock start", tr.int("JamClockStart"), deref(got.JamClockStart))
					// 0 means Java hadn't closed the trip yet: it does that when
					// the next jam starts, so the latest jam's last trips are open.
					if end := tr.int("JamClockEnd"); sj.End != "" && end != 0 {
						diff(tw, "jam clock end", end, deref(got.JamClockEnd))
					}
				}

				for _, f := range tj.list("Fielding") {
					got := st.Lineup[f.id]
					if got == nil {
						got = &replay.Fielding{}
					}
					fw := w + " " + f.id
					diff(fw, "skater", f.str("Skater"), got.Skater)
					diff(fw, "not fielded", f.bool("NotFielded"), got.NotFielded)
					diff(fw, "sit for 3", f.bool("SitFor3"), got.SitFor3)
				}
			}
		}
	}
	for i, tid := range []string{"1", "2"} {
		diff("team "+tid, "score", c.g.kid("Team", tid).int("Score"), totals[i])
	}

	// Penalties per skater, box trips per team.
	penalties := map[string]int{}
	for _, p := range s.Penalties {
		penalties[p.Skater]++
	}
	boxTrips := map[string]int{}
	for _, bt := range s.BoxTrips {
		boxTrips[bt.Team]++
	}
	for _, tid := range []string{"1", "2"} {
		t := c.g.kid("Team", tid)
		for _, sk := range t.list("Skater") {
			regular := 0 // Penalty(0) is the derived FO/EXP entry
			for _, p := range sk.list("Penalty") {
				if p.id != "0" {
					regular++
				}
			}
			diff("skater #"+sk.str("RosterNumber")+" of team "+tid, "penalty count", regular, penalties[sk.id])
		}
		placed := 0 // box trips with no fielding at all can't be placed; the converter notes them
		for _, bt := range t.list("BoxTrip") {
			if bt.str("StartFielding") != "" {
				placed++
			}
		}
		diff("team "+tid, "box trip count", placed, boxTrips[tid])
	}

	diff("game", "expulsion count", len(c.g.list("Expulsion")), len(s.Expulsions))

	if c.g.str("State") != "Finished" {
		up := c.upcoming()
		switch {
		case up == nil && s.UpcomingJam != nil:
			out = append(out, "upcoming jam: none in the Java file, but one after replay")
		case up != nil && (s.UpcomingJam == nil || s.UpcomingJam.ID != up.id):
			out = append(out, "upcoming jam: missing after replay")
		}
	}
	return out
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
