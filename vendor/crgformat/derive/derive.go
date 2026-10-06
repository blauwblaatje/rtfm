// Package derive computes the values the format doesn't store (README "Not
// stored"): jam order, NI, lineup box symbols, foul-outs. The statsbook
// exporter and the hints use it.
package derive

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"crgformat/replay"
)

// Positions in lineup order.
var Positions = []string{"Jammer", "Pivot", "Blocker1", "Blocker2", "Blocker3"}

// JamPos is a jam's place in the game.
type JamPos struct {
	Index    int // 0, 1, 2, ... over the whole game; the upcoming jam is last
	Period   int // period number; 0 for the upcoming jam
	Jam      *replay.Jam
	Upcoming bool
}

// Game gives lookups over a summary.
type Game struct {
	S          *replay.Summary
	Jams       []*JamPos          // in game order
	JamByID    map[string]*JamPos //
	SkaterByID map[string]*replay.Skater
	SkaterTeam map[string]string // skater id -> "1" or "2"
	Penalties  map[string]*replay.Penalty
}

// New indexes a summary.
func New(s *replay.Summary) *Game {
	g := &Game{S: s, JamByID: map[string]*JamPos{}, SkaterByID: map[string]*replay.Skater{},
		SkaterTeam: map[string]string{}, Penalties: map[string]*replay.Penalty{}}
	for _, p := range s.Periods {
		for _, j := range p.Jams {
			jp := &JamPos{Index: len(g.Jams), Period: p.Number, Jam: j}
			g.Jams = append(g.Jams, jp)
			g.JamByID[j.ID] = jp
		}
	}
	if s.UpcomingJam != nil {
		jp := &JamPos{Index: len(g.Jams), Jam: s.UpcomingJam, Upcoming: true}
		g.Jams = append(g.Jams, jp)
		g.JamByID[s.UpcomingJam.ID] = jp
	}
	for _, t := range s.Teams {
		for _, sk := range t.Skaters {
			g.SkaterByID[sk.ID] = sk
			g.SkaterTeam[sk.ID] = t.Team
		}
	}
	for _, p := range s.Penalties {
		g.Penalties[p.ID] = p
	}
	return g
}

// TeamIndex maps "1"/"2" to 0/1.
func TeamIndex(team string) int {
	if team == "2" {
		return 1
	}
	return 0
}

// Number returns a skater's roster number, or "" for an unknown id.
func (g *Game) Number(skaterID string) string {
	if sk := g.SkaterByID[skaterID]; sk != nil {
		return sk.Number
	}
	return ""
}

// NI reports the statsbook NI marks of a team jam (StatsBook Manual, Score):
// on the jammer's line if there is no trip 2 or the star was passed during the
// initial trip; on the SP line if the star was passed during the initial trip
// and the new jammer didn't complete it either.
func NI(tj *replay.TeamJam) (jammerLine, spLine bool) {
	if len(tj.Trips) == 0 {
		return true, false
	}
	initialAfterSP := tj.Trips[0].AfterStarPass
	jammerLine = len(tj.Trips) == 1 || initialAfterSP
	spLine = initialAfterSP && len(tj.Trips) == 1
	return
}

// FieldingKey identifies a lineup position in a jam.
func FieldingKey(jamID, team, position string) string { return jamID + "/" + team + "/" + position }

// Symbols are the lineup box symbols of one fielding.
type Symbols struct {
	Jam, BeforeSP, AfterSP []string
}

// subFor says whether the fielding in pos of jam jp sits in box trip bt for
// someone else (recorded by FieldingSet's boxTrip).
func subFor(jp *JamPos, bt *replay.BoxTrip, pos string) bool {
	f := jp.Jam.Teams[TeamIndex(bt.Team)].Lineup[pos]
	return f != nil && f.BoxTrip == bt.ID
}

// BoxSymbols derives the lineup sheet's box symbols for every fielding, from
// the box trips and sit-for-3 marks, the way the Java version does
// (FieldingImpl.updateBoxTripSymbols; injury continuation jams aren't
// handled). Symbols (StatsBook Manual, Lineups): "-" sat during the jam,
// "+" sat and released during it, "S" in the box at the start (or after
// sitting between jams), "$" in the box at the start and released during it,
// "3" sitting out after an injury.
func (g *Game) BoxSymbols() map[string]*Symbols {
	out := map[string]*Symbols{}
	get := func(k string) *Symbols {
		if out[k] == nil {
			out[k] = &Symbols{}
		}
		return out[k]
	}
	// position of a skater in a jam
	posOf := func(jp *JamPos, team, skater string) string {
		tj := jp.Jam.Teams[TeamIndex(team)]
		for _, pos := range Positions {
			if f := tj.Lineup[pos]; f != nil && f.Skater == skater {
				return pos
			}
		}
		return ""
	}

	type span struct {
		bt         *replay.BoxTrip
		start, end int // jam indexes; end -1 while open
	}
	var spans []span
	for _, bt := range g.S.BoxTrips {
		if bt.Skater == "" {
			continue
		}
		s := g.JamByID[bt.Start.Jam]
		if s == nil {
			continue
		}
		sp := span{bt: bt, start: s.Index, end: -1}
		if bt.End != nil {
			if e := g.JamByID[bt.End.Jam]; e != nil {
				sp.end = e.Index
			}
		}
		spans = append(spans, sp)
	}
	// Java's order (BoxTripImpl.compareTo): start jam, then end jam (open
	// trips last), then when the trip was recorded.
	endKey := func(sp span) int {
		if sp.end < 0 {
			return len(g.Jams)
		}
		return sp.end
	}
	sort.SliceStable(spans, func(i, j int) bool {
		a, b := spans[i], spans[j]
		if a.start != b.start {
			return a.start < b.start
		}
		if endKey(a) != endKey(b) {
			return endKey(a) < endKey(b)
		}
		return a.bt.Start.Time < b.bt.Start.Time
	})

	// 1 = started earlier, ends later; 2 = started during, ends later;
	// 3 = started with this jam, ends later; +3 = ends during this jam.
	symbols := []string{"S", "-", "S", "$", "+", "$"}
	for _, sp := range spans {
		last := sp.end
		if last < 0 {
			last = len(g.Jams) - 1
		}
		prevPos := ""
		for idx := sp.start; idx <= last; idx++ {
			jp := g.Jams[idx]
			pos := posOf(jp, sp.bt.Team, sp.bt.Skater)
			switch {
			case idx == sp.start && sp.bt.Start.Position != "":
				pos = sp.bt.Start.Position // a substitute may be sitting now; the trip started here
			case pos == "" && prevPos != "" && subFor(jp, sp.bt, prevPos):
				// A substitute sits in this box trip: recorded (FieldingSet
				// boxTrip), or, as Java's files show it, someone in the same
				// position while the trip goes on to a later jam.
				pos = prevPos
			case pos == "" && prevPos != "" && idx < sp.end && jp.Jam.Teams[TeamIndex(sp.bt.Team)].Lineup[prevPos] != nil:
				pos = prevPos
			}
			if pos == "" {
				continue // not fielded in this jam
			}
			prevPos = pos
			typeBefore, typeAfter, typeJam := 1, 1, 1
			if idx == sp.start {
				switch {
				case sp.bt.Start.BetweenJams:
					typeJam, typeBefore = 3, 3
				case sp.bt.Start.AfterStarPass:
					typeJam, typeBefore, typeAfter = 2, 0, 2
				default:
					typeJam, typeBefore = 2, 2
				}
			}
			if idx == sp.end {
				switch {
				case sp.bt.End.AfterStarPass && !sp.bt.End.BetweenJams:
					typeJam += 3
					typeAfter += 3
				case !sp.bt.End.BetweenJams:
					typeJam += 3
					typeBefore += 3
					typeAfter = 0
				}
			}
			sym := get(FieldingKey(jp.Jam.ID, sp.bt.Team, pos))
			if typeBefore > 0 {
				sym.BeforeSP = append(sym.BeforeSP, symbols[typeBefore-1])
			}
			if typeAfter > 0 {
				sym.AfterSP = append(sym.AfterSP, symbols[typeAfter-1])
			}
			if typeJam > 0 {
				sym.Jam = append(sym.Jam, symbols[typeJam-1])
			}
		}
	}
	for _, jp := range g.Jams {
		for _, tj := range jp.Jam.Teams {
			for _, pos := range Positions {
				f := tj.Lineup[pos]
				if f == nil || !f.SitFor3 {
					continue
				}
				sym := get(FieldingKey(jp.Jam.ID, tj.Team, pos))
				sym.Jam = append(sym.Jam, "3")
				if tj.StarPassTrip != "" {
					sym.AfterSP = append(sym.AfterSP, "3")
				} else {
					sym.BeforeSP = append(sym.BeforeSP, "3")
				}
			}
		}
	}
	return out
}

// FoulOutLimit is the ruleset's Penalties.NumberToFoulout (7 by default).
func (g *Game) FoulOutLimit() int {
	if n, err := strconv.Atoi(g.Rule("Penalties.NumberToFoulout")); err == nil {
		return n
	}
	return 7
}

// FoulOuts returns, per skater who fouled out, the penalty that reached the
// limit (by slot). The FO entry on the statsbook is in that penalty's jam.
func (g *Game) FoulOuts() map[string]*replay.Penalty {
	limit := g.FoulOutLimit()
	bySkater := map[string][]*replay.Penalty{}
	for _, p := range g.S.Penalties {
		if p.Skater != "" {
			bySkater[p.Skater] = append(bySkater[p.Skater], p)
		}
	}
	out := map[string]*replay.Penalty{}
	for sk, ps := range bySkater {
		if len(ps) < limit {
			continue
		}
		sort.Slice(ps, func(i, j int) bool { return ps[i].Slot < ps[j].Slot })
		out[sk] = ps[limit-1]
	}
	return out
}

// Rule returns a rule's effective value as a string.
func (g *Game) Rule(name string) string { return Rule(g.S, name) }

// Heads returns the head referee's and head NSO's official ids: as declared
// with the official score, else as set on the IGRF tab (game info
// HeadReferee and HeadNSO), else whoever has that role. "" if none.
func Heads(s *replay.Summary) (hr, hnso string) {
	known := map[string]bool{}
	for _, o := range s.Officials {
		known[o.ID] = true
	}
	pick := func(declared, info, role string) string {
		if known[declared] {
			return declared
		}
		if id := s.Info[info]; known[id] {
			return id
		}
		for _, o := range s.Officials {
			if o.Role == role {
				return o.ID
			}
		}
		return ""
	}
	var dHR, dHNSO string
	if r := s.Result; r != nil {
		dHR, dHNSO = r.HR, r.HNSO
	}
	return pick(dHR, "HeadReferee", "Head Referee"), pick(dHNSO, "HeadNSO", "Head Non-Skating Official")
}

// ClockRule returns a time rule ("30:00") in milliseconds.
func (g *Game) ClockRule(name string) int64 { return ClockRule(g.S, name) }

// Rule returns a rule's effective value in a game as a string.
func Rule(s *replay.Summary, name string) string {
	if v, ok := s.Ruleset.Overrides[name]; ok {
		switch v := v.(type) {
		case string:
			return v
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		case int64:
			return strconv.FormatInt(v, 10)
		case int:
			return strconv.Itoa(v)
		case bool:
			return strconv.FormatBool(v)
		}
	}
	return ruleDefaults[name]
}

// ClockRule returns a time rule ("30:00") of a game in milliseconds.
func ClockRule(s *replay.Summary, name string) int64 { return ParseClock(Rule(s, name)) }

// ParseClock reads a time as the rules write it ("30:00", "0:30", "1:02:03").
func ParseClock(v string) int64 {
	var seconds float64
	for _, part := range strings.Split(strings.TrimSpace(v), ":") {
		n, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0
		}
		seconds = seconds*60 + n
	}
	return int64(seconds*1000 + 0.5)
}

// Clock formats milliseconds as M:SS, the way the statsbook shows clocks.
func Clock(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	s := ms / 1000
	return strconv.FormatInt(s/60, 10) + ":" + fmt.Sprintf("%02d", s%60)
}

// Scores are the teams' scores: trip points and OS offsets, less
// Penalties.PointsDeduction for every penalty (Short Track; Java
// TeamImpl.SCORE).
func Scores(s *replay.Summary) [2]int {
	var out [2]int
	for _, p := range s.Periods {
		for _, j := range p.Jams {
			for i, tj := range j.Teams {
				if i > 1 {
					break
				}
				out[i] += tj.OsOffset
				for _, tr := range tj.Trips {
					out[i] += tr.Points
				}
			}
		}
	}
	if d, _ := strconv.Atoi(Rule(s, "Penalties.PointsDeduction")); d != 0 {
		for _, p := range s.Penalties {
			if i := TeamIndex(p.Team); i >= 0 {
				out[i] -= d
			}
		}
	}
	return out
}

// TimeoutsLeft are a team's team timeouts and official reviews left, counted
// as Java does (TeamImpl.recountTimeouts): per game or per period
// (Team.TimeoutsPer, Team.OfficialReviewsPer), retained reviews up to
// Team.MaxRetains not counting, and with Team.RDCLPerHalfRules per half
// (periods 1-2 and 3-4), losing one timeout if none was used in the other half.
func TimeoutsLeft(s *replay.Summary, team string) (timeouts, reviews int) {
	timeouts, _ = strconv.Atoi(Rule(s, "Team.Timeouts"))
	reviews, _ = strconv.Atoi(Rule(s, "Team.OfficialReviews"))
	retains, _ := strconv.Atoi(Rule(s, "Team.MaxRetains"))
	toPer, orPer := Rule(s, "Team.TimeoutsPer") == "true", Rule(s, "Team.OfficialReviewsPer") == "true"
	rdcl := Rule(s, "Team.RDCLPerHalfRules") == "true"
	cur := 0
	if n := len(s.Periods); n > 0 {
		cur = s.Periods[n-1].Number
	}
	otherHalfUnused := rdcl
	for _, p := range s.Periods {
		thisHalf := rdcl && (cur > 2) == (p.Number > 2)
		for _, to := range p.Timeouts {
			if to.Owner != team {
				continue
			}
			if to.Review && !to.AsTimeout {
				if !orPer || p.Number == cur || thisHalf {
					if retains > 0 && to.Retained != nil && *to.Retained {
						retains--
					} else if reviews > 0 {
						reviews--
					}
				}
				continue
			}
			if timeouts > 0 && (!toPer || p.Number == cur) {
				timeouts--
				otherHalfUnused = otherHalfUnused && !thisHalf
			}
		}
	}
	if otherHalfUnused && timeouts > 0 {
		timeouts--
	}
	return timeouts, reviews
}

// SkaterNumber checks a skater number as typed: letters and digits only. A
// "*" after it is the statsbook's mark for a skater who didn't skate (the
// StatsBook Manual), so "12*" is number 12, Not Skating.
func SkaterNumber(raw string) (number string, notSkating bool, err error) {
	n := strings.TrimSpace(raw)
	if strings.HasSuffix(n, "*") {
		n, notSkating = strings.TrimSpace(strings.TrimSuffix(n, "*")), true
	}
	if n == "" {
		return "", false, errors.New("a skater needs a number")
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return "", false, fmt.Errorf("skater number %q: only letters and digits (a * after it marks a skater as Not Skating)", raw)
		}
	}
	return n, notSkating, nil
}
