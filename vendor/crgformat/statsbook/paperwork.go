package statsbook

import (
	"fmt"
	"sort"
	"strings"

	"crgformat/derive"
	"crgformat/replay"
)

// Paperwork lists what a game's statsbook shows, one fact per line, keyed so
// that two games can be compared independently of ids and event history:
// "P1 J3 T1 lead", "P1 J3 T1 trip 2: 4", "P2 J5 T2 Blocker1: 23 S $", "penalty
// T1 #23 P1 J4: B". Used to test the exporter and reader against each
// other, and to find what a corrected statsbook changed.
func Paperwork(s *replay.Summary) []string {
	g := derive.New(s)
	syms := g.BoxSymbols()
	var out []string
	add := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }
	for _, jp := range g.Jams {
		if jp.Upcoming {
			continue
		}
		j := jp.Jam
		key := fmt.Sprintf("P%d J%d", jp.Period, j.Number)
		if j.EndReason == "injury" {
			add("%s injury", key)
		}
		for _, tj := range j.Teams {
			tk := fmt.Sprintf("%s T%s", key, tj.Team)
			for _, f := range []struct {
				name string
				on   bool
			}{{"lead", tj.Lead}, {"lost", tj.Lost}, {"call", tj.Calloff}, {"no pivot", tj.NoPivot}, {"star pass", tj.StarPassTrip != ""}} {
				if f.on {
					add("%s %s", tk, f.name)
				}
			}
			ni, niSP := derive.NI(tj)
			if ni {
				add("%s NI", tk)
			}
			if niSP {
				add("%s NI after star pass", tk)
			}
			for i, t := range tj.Trips {
				side := ""
				if t.AfterStarPass {
					side = " (after SP)"
				}
				if i == 0 && t.Points == 0 {
					continue // the initial trip has no column
				}
				add("%s trip %d%s: %d", tk, i+1, side, t.Points)
			}
			if tj.OsOffset != 0 {
				add("%s OS offset %d", tk, tj.OsOffset)
			}
			for _, pos := range derive.Positions {
				f := tj.Lineup[pos]
				num := ""
				switch {
				case f == nil:
				case f.NotFielded:
					num = "n/a"
				default:
					num = g.Number(f.Skater)
				}
				var sy []string
				if sym := syms[derive.FieldingKey(j.ID, tj.Team, pos)]; sym != nil {
					sy = append(append(sy, sym.BeforeSP...), prefix("SP:", sym.AfterSP)...)
				}
				if num != "" || len(sy) > 0 {
					add("%s %s: %s %s", tk, pos, num, strings.Join(sy, " "))
				}
			}
		}
	}
	for _, p := range s.Penalties {
		jp := g.JamByID[p.Jam]
		where := "unplayed jam"
		if jp != nil && !jp.Upcoming {
			where = fmt.Sprintf("P%d J%d", jp.Period, jp.Jam.Number)
		}
		// Without the slot: the order of penalties within a jam means nothing
		// (the foul-out follows from the count), and statsbooks differ in it.
		add("penalty T%s #%s %s: %s", p.Team, g.Number(p.Skater), where, p.Code)
	}
	for _, e := range s.Expulsions {
		if p := g.Penalties[e.Penalty]; p != nil {
			add("expulsion T%s #%s: %s", p.Team, g.Number(p.Skater), p.Code)
		}
	}
	sort.Strings(out)
	return out
}

func prefix(p string, s []string) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = p + x
	}
	return out
}

// DiffPaperwork returns the facts only in a and only in b.
func DiffPaperwork(a, b []string) (onlyA, onlyB []string) {
	count := map[string]int{}
	for _, x := range a {
		count[x]++
	}
	for _, x := range b {
		count[x]--
	}
	for x, n := range count {
		for ; n > 0; n-- {
			onlyA = append(onlyA, x)
		}
		for ; n < 0; n++ {
			onlyB = append(onlyB, x)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	return
}
