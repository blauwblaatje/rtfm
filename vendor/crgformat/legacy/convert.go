package legacy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"crgformat"
	"crgformat/replay"
)

// Result of converting one game.
type Result struct {
	GameID   string          // the Java game id
	Name     string          // the Java game name
	Log      []byte          // synthesized event log (JSONL), ending in a Checkpoint
	Summary  *replay.Summary // the log replayed
	Version  string          // scoreboard version that wrote the file
	State    string          // Prepared, Running or Finished
	Notes    []string        // what the conversion dropped or estimated
	Warnings []string        // from the replay
	Mismatch []string        // differences between the replay and the Java file
	Err      error           // set if this game couldn't be converted
}

// ConvertFile converts every game in a file written by the Java version: a
// single game file, or a data export with several games. Each game becomes an
// event log, is replayed, and is checked against the values the Java version
// calculated. A game that fails has Err set; the others are still converted.
// Other exported data (prepared teams, settings) is not converted.
func ConvertFile(data []byte, sourceName string, v *crgformat.Validators) ([]*Result, error) {
	root, err := parseState(data)
	if err != nil {
		return nil, err
	}
	sb := root.kid("ScoreBoard", "")
	if sb == nil {
		return nil, fmt.Errorf("no ScoreBoard state in file")
	}
	version := ""
	if n := sb.kid("Version", "release"); n != nil {
		version, _ = n.value.(string)
	}
	var out []*Result
	for _, g := range sb.list("Game") {
		res, err := convertGame(g, version, sourceName, v)
		if err != nil {
			res = &Result{GameID: g.id, Name: g.str("Name"), Version: version, State: g.str("State"), Err: err}
		}
		out = append(out, res)
	}
	return out, nil
}

// Convert converts a file that holds exactly one game.
func Convert(data []byte, sourceName string, v *crgformat.Validators) (*Result, error) {
	res, err := ConvertFile(data, sourceName, v)
	if err != nil {
		return nil, err
	}
	if len(res) != 1 {
		return nil, fmt.Errorf("file has %d games; expected 1", len(res))
	}
	return res[0], res[0].Err
}

func convertGame(g *node, version, sourceName string, v *crgformat.Validators) (*Result, error) {
	c := &converter{g: g, version: version, source: sourceName,
		pre: map[string][]ev{}, during: map[string][]ev{}, after: map[string][]ev{},
		jams: map[string]*jamInfo{}, trips: map[string]string{}, emitted: map[string]bool{},
		expelled: map[string]string{}, beforeFirst: map[string]bool{}, order: map[string]int{}}
	if err := c.run(); err != nil {
		return nil, err
	}

	res := &Result{GameID: g.id, Name: g.str("Name"), Version: version, State: c.g.str("State"), Notes: c.notes}
	lines, err := c.render()
	if err != nil {
		return nil, err
	}
	events, errs := replay.ReadLog(bytes.NewReader(lines), v)
	if len(errs) > 0 {
		return nil, fmt.Errorf("synthesized log is invalid (bug in the converter): %v", errs[0])
	}
	out, err := replay.Replay(events)
	if err != nil {
		return nil, fmt.Errorf("synthesized log does not replay (bug in the converter): %w", err)
	}
	hash, err := replay.SummaryHash(out.Summary)
	if err != nil {
		return nil, err
	}
	c.add(c.lastT, "Checkpoint", m{"summarySha256": hash})
	if lines, err = c.render(); err != nil {
		return nil, err
	}
	res.Log, res.Summary, res.Warnings = lines, out.Summary, out.Warnings
	res.Mismatch = check(c, out.Summary)
	return res, nil
}

type m = map[string]any

// ev is one synthesized event before it gets its seq.
type ev struct {
	t      int64 // wall time, ms since 1970
	typ    string
	fields m
}

type jamInfo struct {
	n        *node
	id       string
	periodID string
	start    int64 // wall time
	end      int64 // 0 while running or upcoming
	pcStart  int64
	pcEnd    int64
	duration int64
	upcoming bool
}

type converter struct {
	g       *node
	gid     string
	version string
	source  string

	events []ev
	lastT  int64
	notes  []string

	// Events anchored to a jam: before it starts (lineups, box trips started
	// between jams), during it, and after it ends (timeouts, box trips ended
	// between jams, penalties entered after the jam).
	pre, during, after map[string][]ev
	tail               []ev // paperwork: links, annotations, expulsions

	jams        map[string]*jamInfo // legacy jam id -> info
	periodLen   int64
	jamLen      int64
	trips       map[string]string // legacy trip id -> log trip id
	officials   map[string]bool
	periods     []*node
	emitted     map[string]bool   // penalty and box trip ids that made it into the log
	beforeFirst map[string]bool   // Java jam 0 ids, mapped to the first jam
	order       map[string]int    // jam id -> position in the game
	expelled    map[string]string // Java FO/EXP penalty id -> the regular penalty it repeats
}

func (c *converter) notef(format string, a ...any) {
	c.notes = append(c.notes, fmt.Sprintf(format, a...))
}

func (c *converter) add(t int64, typ string, fields m) {
	// Keep the recorded time even when it's earlier than the previous event's:
	// seq is the order, and derived values (e.g. the order of box trips on
	// the lineup sheet) depend on when things were recorded. Events without a
	// time of their own (t = 0) get the latest time so far.
	if t == 0 {
		t = c.lastT
	}
	c.lastT = max(c.lastT, t)
	c.events = append(c.events, ev{t, typ, fields})
	switch typ {
	case "PenaltyIssued":
		c.emitted[fields["penalty"].(string)] = true
	case "BoxTripStarted":
		c.emitted[fields["boxTrip"].(string)] = true
	}
}

// flush emits block events sorted by time (stable, so an event added right
// after another with the same time stays after it).
func (c *converter) flush(block []ev) {
	sort.SliceStable(block, func(i, j int) bool { return block[i].t < block[j].t })
	for _, e := range block {
		c.add(e.t, e.typ, e.fields)
	}
}

var nameSuffix = regexp.MustCompile(`\s*\((Prepared|Running|Finished)[^()]*\)$`)

func (c *converter) run() error {
	g := c.g
	c.gid = g.id
	rules := g.values("Rule")
	ov, err := overrides(c.version, rules)
	if err != nil {
		return err
	}
	if c.periodLen, err = parseClock(ruleValue(c.version, rules, "Period.Duration")); err != nil {
		return err
	}
	if c.jamLen, err = parseClock(ruleValue(c.version, rules, "Jam.Duration")); err != nil {
		return err
	}

	for _, p := range g.list("Period") {
		if p.id != "0" { // period 0 is a placeholder before the game
			c.periods = append(c.periods, p)
		}
	}
	c.indexJams()
	t0 := c.startTime()

	// --- setup -----------------------------------------------------------
	info := g.values("EventInfo")
	info["importedFrom"] = c.source
	info["importedVersion"] = c.version
	// Java points at the head officials; the IGRF tab keeps them in the
	// game info (README "Game info").
	if id := g.str("HR"); id != "" {
		info["HeadReferee"] = id
	}
	if id := g.str("HNSO"); id != "" {
		info["HeadNSO"] = id
	}
	if u := g.str("LastFileUpdate"); u != "" && u != "Never" {
		info["importedLastUpdate"] = u // when the Java version last saved this game (local time)
	}
	ruleset := m{"base": "WFTDARuleset", "baseVersion": c.version, "overrides": ov}
	if name := g.str("RulesetName"); name != "" {
		ruleset["name"] = name
	}
	created := m{"game": c.gid, "ruleset": ruleset, "info": info}
	if name := nameSuffix.ReplaceAllString(g.str("Name"), ""); name != "" {
		created["name"] = name
	}
	if tz := c.timezone(); tz != "" {
		created["timezone"] = tz
	}
	c.add(t0, "GameCreated", created)

	for _, tid := range []string{"1", "2"} {
		t := g.kid("Team", tid)
		if t == nil {
			return fmt.Errorf("team %s missing", tid)
		}
		f := m{"team": tid, "name": t.str("Name")}
		optStr(f, "fullName", t.str("FullName"))
		optStr(f, "league", t.str("LeagueName"))
		optStr(f, "teamName", t.str("TeamName"))
		optStr(f, "initials", t.str("Initials"))
		optStr(f, "uniformColor", t.str("UniformColor"))
		optStr(f, "logo", t.str("Logo"))
		optStr(f, "preparedTeam", t.str("PreparedTeam"))
		if colors := t.values("Color"); len(colors) > 0 {
			f["colors"] = colors
		}
		if names := t.values("AlternateName"); len(names) > 0 {
			f["alternateNames"] = names
		}
		c.add(t0, "TeamSet", f)

		skaters := t.list("Skater")
		sort.SliceStable(skaters, func(i, j int) bool {
			return skaters[i].str("RosterNumber") < skaters[j].str("RosterNumber")
		})
		for _, sk := range skaters {
			f := m{"team": tid, "skater": sk.id, "number": sk.str("RosterNumber")}
			optStr(f, "name", sk.str("Name"))
			optStr(f, "pronouns", sk.str("Pronouns"))
			optStr(f, "flags", sk.str("Flags"))
			switch sk.str("BaseRole") {
			case "Ineligible":
				f["status"] = "ineligible"
			case "NotInGame":
				f["status"] = "notInGame"
			}
			c.add(t0, "SkaterAdded", f)
		}
	}

	c.officials = map[string]bool{}
	for _, kind := range []string{"Ref", "Nso"} {
		list := g.list(kind)
		sort.SliceStable(list, func(i, j int) bool { return list[i].str("Role") < list[j].str("Role") })
		for _, o := range list {
			f := m{"official": o.id, "name": o.str("Name"), "role": o.str("Role")}
			optStr(f, "league", o.str("League"))
			optStr(f, "cert", o.str("Cert"))
			optStr(f, "p1Team", c.teamOf(o.str("P1Team")))
			if o.bool("Swap") {
				f["swap"] = true
			}
			c.add(t0, "OfficialAssigned", f)
			c.officials[o.id] = true
		}
	}

	// --- anchored events -------------------------------------------------
	c.placePenalties()
	c.placeBoxTrips()
	c.placeTimeouts()

	// --- game flow -------------------------------------------------------
	for _, p := range c.periods {
		pid := p.str("Id")
		c.add(p.int("WalltimeStart"), "PeriodStarted", m{"period": pid, "number": p.int("Number"), "pc": int64(0)})
		if p.bool("SuddenScoring") {
			c.add(p.int("WalltimeStart"), "SuddenScoringSet", m{"period": pid, "value": true})
		}
		for _, jn := range p.list("Jam") {
			c.emitJam(c.jams[jn.str("Id")], pid)
		}
		if p.int("WalltimeEnd") != 0 && !p.bool("Running") {
			c.add(p.int("WalltimeEnd"), "PeriodEnded", m{"period": pid, "pc": c.periodLen})
		}
	}
	// The Java version always has a next jam. A finished game keeps it only
	// if something refers to it, like a skater still in the box at the end.
	if up := c.upcoming(); up != nil && (g.str("State") != "Finished" || len(c.pre[up.id]) > 0 || hasLineup(up.n)) {
		c.emitPre(up)
	}

	// Paperwork refers to penalties and box trips; skip it for any that were dropped.
	var tail []ev
	for _, e := range c.tail {
		keep := true
		for _, k := range []string{"penalty", "boxTrip"} {
			if id, ok := e.fields[k].(string); ok && !c.emitted[id] {
				keep = false
			}
		}
		if keep {
			tail = append(tail, e)
		} else {
			c.notef("dropped %s for a penalty or box trip that isn't in the log", e.typ)
		}
	}
	c.flush(tail)
	if g.bool("OfficialScore") {
		f := m{"score": m{"1": g.kid("Team", "1").int("Score"), "2": g.kid("Team", "2").int("Score")}}
		if id := g.str("HR"); c.officials[id] {
			f["hr"] = id
		}
		if id := g.str("HNSO"); c.officials[id] {
			f["hnso"] = id
		}
		c.add(c.lastT, "ScoreDeclaredOfficial", f)
	}
	if r := g.str("AbortReason"); r != "" {
		// Java only keeps a reason, not whether it was a forfeit or by whom.
		// Period.Duration is wall time, so take the last jam's period clock.
		pc := int64(0)
		for _, j := range c.jams {
			if !j.upcoming && j.end != 0 && j.pcEnd > pc && j.periodID == c.periods[len(c.periods)-1].str("Id") {
				pc = j.pcEnd
			}
		}
		c.add(c.lastT, "GameEndedEarly", m{"outcome": "cancelled", "reason": r, "pc": pc})
		c.notef("game ended early (%q); Java doesn't record whether it was a forfeit, converted as cancelled", r)
	}
	if s := g.str("SuspensionsServed"); s != "" {
		c.notef("SuspensionsServed %q not converted (free text, no skater ids)", s)
	}
	return nil
}

func (c *converter) indexJams() {
	for _, p := range c.periods {
		for _, j := range p.list("Jam") {
			c.order[j.str("Id")] = len(c.order)
			c.jams[j.str("Id")] = &jamInfo{n: j, id: j.str("Id"), periodID: p.str("Id"),
				start: j.int("WalltimeStart"), end: j.int("WalltimeEnd"),
				pcStart: j.int("PeriodClockElapsedStart"), pcEnd: j.int("PeriodClockElapsedEnd"),
				duration: j.int("Duration")}
		}
	}
	for _, j := range c.g.list("Jam") {
		c.jams[j.str("Id")] = &jamInfo{n: j, id: j.str("Id"), upcoming: true}
	}
	// Java's period 0 holds a placeholder jam 0 for "before the first jam".
	// Penalties and box trips attached to it belong to the jam that was
	// upcoming then: the first jam of the game (StatsBook Manual: a penalty
	// between jams goes on the upcoming jam), or the upcoming jam if the game
	// hasn't started.
	var first *jamInfo
	for _, p := range c.periods {
		if jams := p.list("Jam"); len(jams) > 0 {
			first = c.jams[jams[0].str("Id")]
			break
		}
	}
	if first == nil {
		first = c.upcoming()
	}
	if p0 := c.g.kid("Period", "0"); p0 != nil && first != nil {
		for _, j := range p0.list("Jam") {
			c.jams[j.str("Id")] = first
			c.beforeFirst[j.str("Id")] = true
		}
	}
}

func (c *converter) upcoming() *jamInfo {
	for _, j := range c.jams {
		if j.upcoming {
			return j
		}
	}
	return nil
}

// startTime is when setup events are dated: the first wall time in the
// file, or the event date, or 1970 for a game with neither.
func (c *converter) startTime() int64 {
	if len(c.periods) > 0 && c.periods[0].int("WalltimeStart") > 0 {
		return c.periods[0].int("WalltimeStart")
	}
	if d, err := time.Parse("2006-01-02", c.g.values("EventInfo")["Date"]); err == nil {
		return d.UnixMilli()
	}
	return 0
}

// timezone takes the zone from a period's LocalTimeStart, e.g.
// "2026-06-14T12:12:17.868+02:00[Europe/Paris]".
func (c *converter) timezone() string {
	for _, p := range c.periods {
		s := p.str("LocalTimeStart")
		if i := strings.IndexByte(s, '['); i >= 0 && strings.HasSuffix(s, "]") {
			return s[i+1 : len(s)-1]
		}
	}
	return ""
}

// teamOf maps a Java team id ("<game>_1") to "1" or "2".
func (c *converter) teamOf(id string) string {
	switch id {
	case c.gid + "_1":
		return "1"
	case c.gid + "_2":
		return "2"
	}
	return ""
}

// fielding splits a Java fielding id "<jam>_<team>_<position>".
func (c *converter) fielding(id string) (jam, team, pos string, ok bool) {
	parts := strings.Split(id, "_")
	if len(parts) < 3 {
		return "", "", "", false
	}
	n := len(parts)
	return strings.Join(parts[:n-2], "_"), parts[n-2], parts[n-1], true
}

func (c *converter) emitPre(j *jamInfo) {
	c.add(firstNonZero(j.start, c.lastT), "JamUpcoming", m{"jam": j.id, "number": j.n.int("Number")})
	var block []ev
	for _, tid := range []string{"1", "2"} {
		tj := j.n.kid("TeamJam", tid)
		if tj == nil {
			continue
		}
		for _, f := range tj.list("Fielding") {
			fs := m{"jam": j.id, "team": tid, "position": f.id}
			if sk := f.str("Skater"); sk != "" {
				fs["skater"] = sk
			}
			if f.bool("NotFielded") {
				fs["notFielded"] = true
			}
			if f.bool("SitFor3") {
				fs["sitFor3"] = true
			}
			if len(fs) > 3 {
				block = append(block, ev{firstNonZero(j.start, c.lastT), "FieldingSet", fs})
			}
			if a := f.str("Annotation"); a != "" {
				c.tail = append(c.tail, ev{0, "Annotated", m{"target": m{"kind": "fielding", "id": j.id + "/" + tid + "/" + f.id}, "text": a}})
			}
		}
	}
	c.flush(append(block, c.pre[j.id]...))
}

func (c *converter) emitJam(j *jamInfo, periodID string) {
	c.emitPre(j)
	n := j.n
	c.add(j.start, "JamStarted", m{"period": periodID, "jam": j.id, "number": n.int("Number"),
		"overtime": n.bool("Overtime"), "injuryContinuation": n.bool("InjuryContinuation"), "pc": j.pcStart})

	block := c.during[j.id]
	ended := j.end != 0
	endT, endJC := j.end, j.duration
	if !ended {
		endT = j.start
	}
	for _, tid := range []string{"1", "2"} {
		tj := n.kid("TeamJam", tid)
		if tj == nil {
			continue
		}
		trips := tj.list("ScoringTrip")
		tripT := j.start // trips keep their order even if an edited trip's clock times don't
		for k, tr := range trips {
			id := tr.str("Id")
			jc := tr.int("JamClockStart")
			tripT = max(tripT, j.start+jc)
			if tr.id == "1" {
				c.trips[id] = j.id + "/" + tid + "/t1"
			} else {
				c.trips[id] = id
				block = append(block, ev{tripT, "TripStarted",
					m{"jam": j.id, "team": tid, "trip": id, "afterStarPass": tr.bool("AfterSP"), "jc": jc}})
			}
			// A last trip that ended before the jam did ended when its points
			// were entered; say when, or the replay ends it with the jam.
			end := tr.int("JamClockEnd")
			if k == len(trips)-1 && ended && end > 0 && end != j.duration {
				block = append(block, ev{tripT, "TripPointsSet", m{"trip": c.trips[id], "points": tr.int("Score"), "jc": end}})
			} else if pts := tr.int("Score"); pts != 0 {
				block = append(block, ev{tripT, "TripPointsSet", m{"trip": c.trips[id], "points": pts}})
			}
			if a := tr.str("Annotation"); a != "" {
				c.tail = append(c.tail, ev{0, "Annotated", m{"target": m{"kind": "trip", "id": c.trips[id]}, "text": a}})
			}
		}
		flags := []struct {
			name string
			on   bool
		}{
			{"lead", tj.bool("Lead")}, {"lost", tj.bool("Lost")},
			{"calloff", tj.bool("Calloff")}, {"noPivot", tj.bool("NoPivot")},
		}
		for _, f := range flags {
			if f.on {
				block = append(block, ev{endT, "JamFlagSet", m{"jam": j.id, "team": tid, "flag": f.name, "value": true, "jc": endJC}})
			}
		}
		if tj.bool("StarPass") {
			if trip, ok := c.trips[tj.str("StarPassTrip")]; ok {
				block = append(block, ev{endT, "StarPassSet", m{"jam": j.id, "team": tid, "trip": trip, "jc": endJC}})
			} else {
				c.notef("jam %s team %s: star pass without a known trip", n.str("Number"), tid)
			}
		}
		if off := tj.int("OsOffset"); off != 0 {
			block = append(block, ev{endT, "OsOffsetSet", m{"jam": j.id, "team": tid, "offset": off, "reason": tj.str("OsOffsetReason")}})
		}
	}
	c.flush(block)

	if ended {
		c.add(j.end, "JamEnded", m{"jam": j.id, "reason": c.endReason(j), "pc": j.pcEnd, "jc": j.duration})
	}
	c.flush(c.after[j.id])
}

// endReason works out why a jam ended. Java records injury (on both teams)
// and call-offs, but not other official stoppages.
func (c *converter) endReason(j *jamInfo) string {
	var injury, calloff bool
	for _, tj := range j.n.list("TeamJam") {
		injury = injury || tj.bool("Injury")
		calloff = calloff || tj.bool("Calloff")
	}
	switch {
	case injury:
		return "injury"
	case calloff:
		return "calloff"
	case j.duration >= c.jamLen:
		return "time"
	}
	return "unknown"
}

func (c *converter) placePenalties() {
	for _, tid := range []string{"1", "2"} {
		for _, sk := range c.g.kid("Team", tid).list("Skater") {
			for _, p := range sk.list("Penalty") {
				if p.id == "0" {
					// Penalty(0) is the FO/EXP entry: derived (see README
					// "Penalties"). An expulsion's penalty is found below.
					if code := p.str("Code"); code != "FO" {
						c.expelled[p.str("Id")] = c.regularPenalty(sk, p)
					}
					continue
				}
				jam := c.jams[p.str("Jam")]
				if jam == nil {
					c.notef("penalty %s of skater #%s: unknown jam, dropped", p.str("Code"), sk.str("RosterNumber"))
					continue
				}
				t := p.int("Time")
				pc, jc := c.clockAt(t)
				e := ev{t, "PenaltyIssued", m{"penalty": p.str("Id"), "skater": sk.id, "code": p.str("Code"),
					"jam": jam.id, "slot": p.int("Number"), "pc": pc, "jc": jc}}
				if c.beforeFirst[p.str("Jam")] {
					c.notef("penalty %s of skater #%s was before the first jam; put on the first jam", p.str("Code"), sk.str("RosterNumber"))
				}
				switch {
				case jam.upcoming, jam.start != 0 && t != 0 && t < jam.start:
					c.pre[jam.id] = append(c.pre[jam.id], e)
				case jam.end != 0 && t > jam.end:
					c.after[jam.id] = append(c.after[jam.id], e)
				default:
					c.during[jam.id] = append(c.during[jam.id], e)
				}
				if p.bool("ForceServed") {
					c.tail = append(c.tail, ev{0, "PenaltyForceServed", m{"penalty": p.str("Id"), "value": true}})
				}
			}
		}
	}
	for _, x := range c.g.list("Expulsion") {
		penalty := c.expelled[x.id]
		if penalty == "" {
			c.notef("expulsion %s: no regular penalty with the same code and jam, dropped", x.id)
			continue
		}
		f := m{"penalty": penalty, "info": x.str("Info"), "suspension": x.bool("Suspension")}
		optStr(f, "extraInfo", x.str("ExtraInfo"))
		c.tail = append(c.tail, ev{0, "ExpulsionRecorded", f})
	}
}

// staleEnd returns the last jam an open box trip covers, if its skater was
// fielded in a later jam; nil if the trip may really still be open.
func (c *converter) staleEnd(bt *node, team string) *jamInfo {
	skater := bt.str("CurrentSkater")
	if skater == "" {
		return nil
	}
	var last *jamInfo
	for _, f := range bt.list("Fielding") {
		id, _ := f.value.(string)
		jamID, _, _, ok := c.fielding(id)
		if j := c.jams[jamID]; ok && j != nil && !j.upcoming && (last == nil || c.order[j.id] > c.order[last.id]) {
			last = j
		}
	}
	if last == nil || last.end == 0 {
		return nil
	}
	for _, p := range c.periods {
		for _, jn := range p.list("Jam") {
			j := c.jams[jn.str("Id")]
			if c.order[j.id] <= c.order[last.id] {
				continue
			}
			if tj := jn.kid("TeamJam", team); tj != nil {
				for _, f := range tj.list("Fielding") {
					if f.str("Skater") == skater {
						return last
					}
				}
			}
		}
	}
	return nil
}

// regularPenalty finds the penalty in slots 1-9 that an FO/EXP entry repeats:
// same code and jam.
func (c *converter) regularPenalty(sk, exp *node) string {
	for _, p := range sk.list("Penalty") {
		if p.id != "0" && p.str("Code") == exp.str("Code") && p.str("Jam") == exp.str("Jam") {
			return p.str("Id")
		}
	}
	return ""
}

func (c *converter) placeBoxTrips() {
	estimated := 0
	for _, tid := range []string{"1", "2"} {
		for _, bt := range c.g.kid("Team", tid).list("BoxTrip") {
			if bt.str("StartFielding") == "" {
				c.notef("box trip %s (%s) has no skater or fielding in the Java file, dropped", bt.id, bt.str("PenaltyCodes"))
				continue
			}
			jamID, _, pos, ok := c.fielding(bt.str("StartFielding"))
			jam := c.jams[jamID]
			if !ok || jam == nil {
				c.notef("box trip %s: start fielding %q is not in a known jam, dropped", bt.id, bt.str("StartFielding"))
				continue
			}
			t := bt.int("WalltimeStart")
			pc, _ := c.clockAt(t)
			estimated++
			start := m{"boxTrip": bt.id, "team": tid, "jammer": pos == "Jammer", "position": pos, "jam": jam.id,
				"betweenJams": bt.bool("StartBetweenJams") || c.beforeFirst[jamID], "afterStarPass": bt.bool("StartAfterSP"),
				"pc": pc, "jc": bt.int("JamClockStart")}
			optStr(start, "skater", bt.str("CurrentSkater"))
			e := ev{t, "BoxTripStarted", start}
			if bt.bool("StartBetweenJams") || jam.upcoming || c.beforeFirst[jamID] {
				c.pre[jam.id] = append(c.pre[jam.id], e)
			} else {
				c.during[jam.id] = append(c.during[jam.id], e)
			}

			// A trip with an end fielding has ended as far as the paperwork goes,
			// even when Java still marks it current (7 such trips in the test data).
			if endID := bt.str("EndFielding"); endID != "" {
				endJamID, _, _, ok := c.fielding(endID)
				endJam := c.jams[endJamID]
				if !ok || endJam == nil {
					c.notef("box trip %s: end fielding %q is not in a known jam, left open", bt.id, endID)
				} else {
					t := bt.int("WalltimeEnd")
					pc, _ := c.clockAt(t)
					// Java sets the jam clock to 0 for a trip ending between jams;
					// the jam clock then shows the ended jam's duration. Its
					// Duration is left out: see README "Penalty box".
					jc := bt.int("JamClockEnd")
					if bt.bool("EndBetweenJams") {
						jc = endJam.duration
					}
					e := ev{t, "BoxTripEnded", m{"boxTrip": bt.id, "jam": endJam.id,
						"betweenJams": bt.bool("EndBetweenJams"), "afterStarPass": bt.bool("EndAfterSP"),
						"pc": pc, "jc": jc}}
					if bt.bool("EndBetweenJams") {
						c.after[endJam.id] = append(c.after[endJam.id], e)
					} else if endJam.upcoming {
						c.pre[endJam.id] = append(c.pre[endJam.id], e)
					} else {
						c.during[endJam.id] = append(c.during[endJam.id], e)
					}
				}
			} else if last := c.staleEnd(bt, tid); last != nil {
				// Java still has the trip open, but it only covers fieldings up
				// to `last` and the skater was fielded again afterwards: the
				// release was never recorded. End it after that jam, which gives
				// the same lineup symbols Java shows.
				c.notef("box trip %s (#%s) was never ended; ended after the last jam it covers", bt.id,
					c.g.kid("Team", tid).kid("Skater", bt.str("CurrentSkater")).str("RosterNumber"))
				c.after[last.id] = append(c.after[last.id], ev{last.end, "BoxTripEnded", m{"boxTrip": bt.id,
					"jam": last.id, "betweenJams": true, "afterStarPass": false, "pc": last.pcEnd, "jc": last.duration}})
			}
			if n := bt.int("Shortened"); n != 0 {
				c.tail = append(c.tail, ev{0, "BoxTripShortened", m{"boxTrip": bt.id, "amount": n}})
			}
			for _, p := range bt.list("Penalty") {
				if id, _ := p.value.(string); id != "" {
					c.tail = append(c.tail, ev{0, "BoxTripPenaltyLinked", m{"boxTrip": bt.id, "penalty": id, "value": true}})
				}
			}
		}
	}
	if estimated > 0 {
		c.notef("period clock of %d box trip(s) and all penalties estimated from wall time", estimated)
	}
}

func (c *converter) placeTimeouts() {
	for _, p := range c.periods {
		for _, to := range p.list("Timeout") {
			if to.id == "noTimeout" {
				continue
			}
			jam := c.jams[to.str("PrecedingJam")]
			if jam == nil || jam.upcoming {
				c.notef("timeout %s: no preceding jam in the game, dropped", to.id)
				continue
			}
			// Java writes an Official Review used as a timeout as this request text.
			asTimeout := to.bool("Review") && to.str("OrRequest") == "Taken as Team Timeout"
			owner := to.str("Owner")
			if t := c.teamOf(owner); t != "" {
				owner = t
			}
			block := []ev{{to.int("WalltimeStart"), "TimeoutStarted", m{"timeout": to.id, "afterJam": jam.id,
				"owner": owner, "review": to.bool("Review"), "asTimeout": asTimeout, "pc": to.int("PeriodClockElapsedStart")}}}
			end := firstNonZero(to.int("WalltimeEnd"), to.int("WalltimeStart"))
			if to.bool("Review") {
				f := m{"timeout": to.id, "retained": to.bool("RetainedReview")}
				if !asTimeout {
					optStr(f, "request", to.str("OrRequest"))
				}
				optStr(f, "result", to.str("OrResult"))
				block = append(block, ev{end, "OfficialReviewResolved", f})
			}
			if !to.bool("Running") && to.int("WalltimeEnd") != 0 {
				block = append(block, ev{end, "TimeoutEnded", m{"timeout": to.id, "duration": to.int("Duration"),
					"pc": c.timeoutEndClock(to)}})
			}
			c.after[jam.id] = append(c.after[jam.id], block...)
		}
	}
}

// timeoutEndClock is the period clock (elapsed) when a timeout ended,
// including corrections made during it. Java keeps the displayed value in
// PeriodClockEnd (it counts down); PeriodClockElapsedEnd is often 0.
func (c *converter) timeoutEndClock(to *node) int64 {
	if shown := to.int("PeriodClockEnd"); shown > 0 && shown <= c.periodLen {
		return c.periodLen - shown
	}
	if e := to.int("PeriodClockElapsedEnd"); e > 0 {
		return e
	}
	return to.int("PeriodClockElapsedStart")
}

// clockAt estimates the period and jam clock (elapsed) at a wall time, from
// where it falls among the jams. Between jams the period clock runs on from
// the last jam's end, but never past the next jam's start.
func (c *converter) clockAt(t int64) (pc, jc int64) {
	if t == 0 {
		return 0, 0
	}
	var prev *jamInfo
	for _, p := range c.periods {
		if p.int("WalltimeStart") > t {
			break
		}
		prev = nil
		for _, jn := range p.list("Jam") {
			j := c.jams[jn.str("Id")]
			if j.start > t {
				if prev == nil {
					return 0, 0
				}
				return clamp(prev.pcEnd+(t-prev.end), prev.pcEnd, j.pcStart), prev.duration
			}
			if j.end == 0 || t <= j.end {
				jc = clamp(t-j.start, 0, max(j.duration, t-j.start))
				return j.pcStart + jc, jc
			}
			prev = j
		}
	}
	if prev == nil {
		return 0, 0
	}
	return clamp(prev.pcEnd+(t-prev.end), prev.pcEnd, c.periodLen), prev.duration
}

// render numbers the events and writes them as JSONL.
func (c *converter) render() ([]byte, error) {
	var buf bytes.Buffer
	for i, e := range c.events {
		line := m{"v": 1, "seq": i + 1, "t": iso(e.t), "type": e.typ}
		for k, v := range e.fields {
			line[k] = v
		}
		b, err := json.Marshal(line)
		if err != nil {
			return nil, err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

func iso(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z") }

func optStr(f m, key, val string) {
	if val != "" {
		f[key] = val
	}
}

func hasLineup(jam *node) bool {
	for _, tj := range jam.list("TeamJam") {
		for _, f := range tj.list("Fielding") {
			if f.str("Skater") != "" || f.bool("NotFielded") || f.bool("SitFor3") {
				return true
			}
		}
	}
	return false
}

func firstNonZero(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

func clamp(v, lo, hi int64) int64 {
	if hi < lo {
		hi = lo
	}
	return min(max(v, lo), hi)
}
