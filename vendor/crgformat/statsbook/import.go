package statsbook

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"crgformat"
	"crgformat/replay"
)

// ImportResult is a statsbook read into an event log and replayed.
type ImportResult struct {
	Log       []byte
	Summary   *replay.Summary
	Revision  string // template revision, e.g. "20250201"
	Jams      int
	Penalties int
	Notes     []string // what couldn't be read, or was guessed
}

// Import reads a filled-in statsbook into an event log (the inverse of
// Export). A statsbook has no wall-clock times, so event times are estimated
// from the start time and jam lengths; box trips are rebuilt from the lineup
// symbols, and penalties linked to them in order.
func Import(data []byte, name string, v *crgformat.Validators) (*ImportResult, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := &reader{f: f, name: name, skaters: map[string]string{}, jams: map[jamKey]*jamLine{}}
	if err := r.read(); err != nil {
		return nil, err
	}
	lines, err := r.render()
	if err != nil {
		return nil, err
	}
	events, errs := replay.ReadLog(bytes.NewReader(lines), v)
	if len(errs) > 0 {
		return nil, fmt.Errorf("the log built from the statsbook is invalid (bug in the reader): %v", errs[0])
	}
	out, err := replay.Replay(events)
	if err != nil {
		return nil, fmt.Errorf("the log built from the statsbook does not replay (bug in the reader): %w", err)
	}
	notes := append(r.notes, out.Warnings...)
	return &ImportResult{Log: lines, Summary: out.Summary, Revision: r.revision, Jams: len(r.order),
		Penalties: r.penaltyCount, Notes: notes}, nil
}

type m = map[string]any

type jamKey struct{ period, number int }

// jamLine is one jam as read from the Score and Lineups sheets.
type jamLine struct {
	key     jamKey
	id      string
	row     int // first row on Score / Lineups / OS Offset
	spLine  bool
	teams   [2]*teamLine
	seconds int64 // jam length, from the Game Clock sheet
	pcStart int64 // estimated
	t       time.Time
	events  []string // Game Clock events
	details []string
	injury  bool
}

type teamLine struct {
	jammer, pivotAfterSP                string // roster numbers
	lost, lead, call, inj, niBase, niSP bool
	trips                               []trip
	noPivot                             bool
	lineup                              map[string]string // position -> number ("" = none)
	notFielded                          map[string]bool
	syms, symsSP                        map[string][]string // position -> box symbols (SP line by SP-line position)
	spLinePos                           map[string]string   // SP-line position -> original position
	osOffset                            int
	osReason                            string
	skNote, ltNote                      string
}

type trip struct {
	points  int
	afterSP bool
	note    string
}

type reader struct {
	f        *excelize.File
	name     string
	revision string
	notes    []string

	info    map[string]string
	start   time.Time
	teams   [2]teamInfo
	skaters map[string]string // "team/number" -> skater id

	jams  map[jamKey]*jamLine
	order []*jamLine

	penaltyCount int
	events       []ev
	comments     map[string]map[string]string // sheet -> cell -> text
	seq          int
}

type teamInfo struct {
	league, team, color string
	roster              []rosterEntry
	captain             string
}

type rosterEntry struct {
	number, name string
	notInGame    bool
}

type ev struct {
	t      time.Time
	typ    string
	fields m
}

func (r *reader) notef(format string, a ...any) { r.notes = append(r.notes, fmt.Sprintf(format, a...)) }

// --- cell access ----------------------------------------------------------

// cell returns a cell's value as text; formula cells without a saved value
// are calculated.
func (r *reader) cell(sheet string, col, row int) string {
	ref := cellName(col, row)
	v, err := r.f.GetCellValue(sheet, ref, excelize.Options{RawCellValue: true})
	if err == nil && v == "" {
		if fm, _ := r.f.GetCellFormula(sheet, ref); fm != "" {
			v, _ = r.f.CalcCellValue(sheet, ref)
		}
	}
	return strings.TrimSpace(v)
}

func (r *reader) formula(sheet string, col, row int) string {
	fm, _ := r.f.GetCellFormula(sheet, cellName(col, row))
	return strings.TrimPrefix(strings.TrimSpace(fm), "=")
}

func (r *reader) comment(sheet string, col, row int) string {
	if r.comments == nil {
		r.comments = map[string]map[string]string{}
	}
	cs, ok := r.comments[sheet]
	if !ok {
		cs = map[string]string{}
		list, _ := r.f.GetComments(sheet)
		for _, c := range list {
			text := c.Text
			if text == "" {
				var sb strings.Builder
				for _, p := range c.Paragraph {
					sb.WriteString(p.Text)
				}
				text = sb.String()
			}
			cs[c.Cell] = strings.TrimSpace(text)
		}
		r.comments[sheet] = cs
	}
	return cs[cellName(col, row)]
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// safeID makes an id from names and numbers (see the schema's id pattern).
func safeID(s string) string {
	s = unsafeID.ReplaceAllString(s, "_")
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}

func isX(s string) bool { return strings.EqualFold(strings.TrimSpace(s), "x") }

// number normalises a roster number read from a cell ("22", "22.0", "007").
func number(s string) string {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "*"))
	if f, err := strconv.ParseFloat(s, 64); err == nil && !strings.HasPrefix(s, "0") && f == math.Trunc(f) {
		return strconv.FormatInt(int64(f), 10)
	}
	return s
}

// --- reading ------------------------------------------------------------

var revisionRe = regexp.MustCompile(`Rev\.?\s*(\d+)`)

func (r *reader) read() error {
	for _, sh := range []string{sheetIGRF, sheetScore, sheetPenalties, sheetLineups} {
		if idx, _ := r.f.GetSheetIndex(sh); idx < 0 {
			return fmt.Errorf("not a statsbook: no %q sheet", sh)
		}
	}
	rows, _ := r.f.GetRows(sheetIGRF)
	for _, row := range rows {
		for _, c := range row {
			if m := revisionRe.FindStringSubmatch(c); m != nil && strings.Contains(c, "IGRF") {
				r.revision = m[1]
			}
		}
	}
	r.readIGRF()
	r.readScore()
	r.readLineups()
	r.readClock()
	r.estimateTimes()
	r.build()
	return nil
}

func (r *reader) readIGRF() {
	get := func(p pos) string { return r.cell(sheetIGRF, p.col, p.row) }
	r.info = map[string]string{}
	for k, p := range map[string]pos{"Venue": igrfVenue, "City": igrfCity, "State": igrfState, "GameNo": igrfGameNo,
		"Tournament": igrfTournament, "HostLeague": igrfHost} {
		if v := get(p); v != "" {
			r.info[k] = number(v)
			if k != "GameNo" {
				r.info[k] = v
			}
		}
	}
	r.start = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if d, err := strconv.ParseFloat(get(igrfDate), 64); err == nil {
		r.start = excelTime(d)
		r.info["Date"] = r.start.Format("2006-01-02")
	}
	if st, err := strconv.ParseFloat(get(igrfStartTime), 64); err == nil {
		frac := st - math.Floor(st)
		r.start = r.start.Add(time.Duration(frac * 24 * float64(time.Hour))).Round(time.Minute)
		r.info["StartTime"] = r.start.Format("15:04:05")
	}
	for i := range r.teams {
		col := igrfTeamCol(i)
		t := &r.teams[i]
		t.league = r.cell(sheetIGRF, col, igrfLeague)
		t.team = r.cell(sheetIGRF, col, igrfTeam)
		t.color = r.cell(sheetIGRF, col, igrfColor)
		for row := igrfRosterFirst; row <= igrfRosterLast; row++ {
			raw := r.cell(sheetIGRF, col, row)
			if raw == "" {
				continue
			}
			t.roster = append(t.roster, rosterEntry{number: number(raw), name: r.cell(sheetIGRF, col+1, row),
				notInGame: strings.HasSuffix(raw, "*")})
		}
		t.captain = r.cell(sheetIGRF, col, igrfCaptains)
	}
}

func excelTime(serial float64) time.Time {
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	return base.Add(time.Duration(serial * 24 * float64(time.Hour))).Round(time.Second)
}

func (r *reader) readScore() {
	for period := 0; period < 2; period++ {
		first := jamRow(period)
		var cur *jamLine
		for row := first; row < first+periodRows; row++ {
			a := r.cell(sheetScore, scoreTeamCol1, row)
			b := r.cell(sheetScore, scoreTeamCol2, row)
			label := a
			if label == "" {
				label = b
			}
			up := strings.ToUpper(label)
			switch {
			case strings.HasPrefix(up, "SP") || up == "*":
				if cur == nil {
					r.notef("Score period %d row %d: star pass line without a jam", period+1, row+1)
					continue
				}
				cur.spLine = true
				continue
			case label == "":
				continue
			}
			n, err := strconv.Atoi(number(label))
			if err != nil {
				if strings.HasPrefix(up, "INJ") {
					r.notef("Score period %d row %d: injury continuation jams aren't read yet", period+1, row+1)
				} else {
					r.notef("Score period %d row %d: jam number %q not understood", period+1, row+1, label)
				}
				cur = nil
				continue
			}
			k := jamKey{period + 1, n}
			if r.jams[k] != nil {
				r.notef("Score: jam %d of period %d appears twice; the second is skipped", n, period+1)
				cur = nil
				continue
			}
			cur = &jamLine{key: k, id: fmt.Sprintf("p%dj%d", period+1, n), row: row}
			r.jams[k] = cur
			r.order = append(r.order, cur)
		}
	}
	for _, j := range r.order {
		for ti, base := range []int{scoreTeamCol1, scoreTeamCol2} {
			j.teams[ti] = r.readScoreTeam(j, base, ti)
		}
		j.injury = j.teams[0].inj || j.teams[1].inj
	}
}

func (r *reader) readScoreTeam(j *jamLine, base, team int) *teamLine {
	row, sp := j.row, j.row+1
	tl := &teamLine{lineup: map[string]string{}, notFielded: map[string]bool{}, syms: map[string][]string{},
		symsSP: map[string][]string{}, spLinePos: map[string]string{}}
	tl.jammer = number(r.cell(sheetScore, base+1, row))
	tl.skNote = r.comment(sheetScore, base, row)
	tl.lost = isX(r.cell(sheetScore, base+2, row))
	tl.lead = isX(r.cell(sheetScore, base+3, row))
	tl.call = isX(r.cell(sheetScore, base+4, row))
	tl.inj = isX(r.cell(sheetScore, base+5, row))
	tl.niBase = isX(r.cell(sheetScore, base+6, row))
	hasSP := j.spLine && strings.EqualFold(r.cell(sheetScore, base, sp), "SP")
	if hasSP {
		tl.pivotAfterSP = number(r.cell(sheetScore, base+1, sp))
		tl.niSP = isX(r.cell(sheetScore, base+6, sp))
	}
	// Trip 2 to Trip 10 columns; on the SP line after the star pass.
	for col := base + 7; col <= base+15; col++ {
		for _, line := range []int{row, sp} {
			if line == sp && !hasSP {
				continue
			}
			pts, ok := r.tripPoints(sheetScore, col, line)
			if !ok {
				continue
			}
			note := r.comment(sheetScore, col, line)
			for i, p := range pts {
				t := trip{points: p, afterSP: line == sp}
				if i == 0 {
					t.note = note
				}
				tl.trips = append(tl.trips, t)
			}
		}
	}
	if tl.niBase && !hasSP && len(tl.trips) > 0 {
		sum := 0
		for _, t := range tl.trips {
			sum += t.points
		}
		r.notef("jam %d/%d team %d: NI marked with trips entered; the trips are kept", j.key.period, j.key.number, team+1)
	}
	return tl
}

// tripPoints reads a trip cell: a number, a formula like "4+2" (several
// trips), or "4 + NI" / "4 + SP" (points on the initial trip).
func (r *reader) tripPoints(sheet string, col, row int) ([]int, bool) {
	if fm := r.formula(sheet, col, row); fm != "" && regexp.MustCompile(`^[\d\s+]+$`).MatchString(fm) {
		var out []int
		for _, p := range strings.Split(fm, "+") {
			if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
				out = append(out, n)
			}
		}
		return out, len(out) > 0
	}
	v := r.cell(sheet, col, row)
	if v == "" || v == "-" || v == "–" {
		return nil, false
	}
	if m := regexp.MustCompile(`^(\d+)\s*\+\s*(NI|SP)$`).FindStringSubmatch(strings.ToUpper(v)); m != nil {
		n, _ := strconv.Atoi(m[1])
		return []int{n}, true
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		r.notef("%s %s: trip points %q not understood", sheet, cellName(col, row), v)
		return nil, false
	}
	return []int{int(f)}, true
}

var lineupPositions = []string{"Jammer", "Pivot", "Blocker1", "Blocker2", "Blocker3"}

func (r *reader) readLineups() {
	for _, j := range r.order {
		for ti, c := range []int{0, lineupsTeamCol2} {
			tl := j.teams[ti]
			tl.ltNote = r.comment(sheetLineups, c, j.row)
			tl.noPivot = isX(r.cell(sheetLineups, c+1, j.row))
			for i, p := range lineupPositions {
				col := c + 2 + 4*i
				num := number(r.cell(sheetLineups, col, j.row))
				if len(num) > 8 {
					// Not a roster number: the 2019 template has its legend and
					// totals inside the jam area.
					num = ""
				}
				if p == "Jammer" {
					num = tl.jammer // the Lineups sheet copies it from Score
				}
				note := r.comment(sheetLineups, col, j.row)
				if num == "" && strings.Contains(strings.ToLower(note), "skated short") || strings.EqualFold(num, "n/a") {
					tl.notFielded[p] = true
					num = ""
				}
				if num == "?" {
					num = ""
				}
				tl.lineup[p] = num
				for k := 1; k <= 3; k++ {
					if s := strings.ToUpper(r.cell(sheetLineups, col+k, j.row)); s != "" && len(s) <= 4 {
						tl.syms[p] = append(tl.syms[p], s)
					}
				}
			}
			if j.spLine && tl.pivotAfterSP != "" {
				// SP line: jammer column is the former pivot, pivot column the
				// former jammer; the blockers stay (StatsBook Manual, Lineups).
				spOrder := []string{"Pivot", "Jammer", "Blocker1", "Blocker2", "Blocker3"}
				for i, orig := range spOrder {
					col := c + 2 + 4*i
					for k := 1; k <= 3; k++ {
						if s := strings.ToUpper(r.cell(sheetLineups, col+k, j.row+1)); s != "" {
							tl.symsSP[orig] = append(tl.symsSP[orig], s)
						}
					}
				}
			}
		}
		for ti, c := range []int{0, osOffsetCol2} {
			if v := r.cell(sheetOSOffset, c+1, j.row); v != "" {
				if n, err := strconv.ParseFloat(v, 64); err == nil {
					j.teams[ti].osOffset = int(n)
					j.teams[ti].osReason = r.cell(sheetOSOffset, c+2, j.row)
				}
			}
		}
	}
}

func (r *reader) readClock() {
	if idx, _ := r.f.GetSheetIndex(sheetClock); idx < 0 {
		return
	}
	for period := 0; period < 2; period++ {
		index := 0
		for _, j := range r.order {
			if j.key.period != period+1 {
				continue
			}
			row := period*clockPeriodRows + index + 10
			index++
			if v, err := strconv.ParseFloat(r.cell(sheetClock, 1, row), 64); err == nil {
				j.seconds = int64(v)
			}
			if e := r.cell(sheetClock, 3, row); e != "" {
				for _, x := range strings.FieldsFunc(e, func(c rune) bool { return c == ';' || c == ',' }) {
					j.events = append(j.events, strings.ToUpper(strings.TrimSpace(x)))
				}
				j.details = strings.Split(r.cell(sheetClock, 4, row), ";")
			}
		}
	}
}

// estimateTimes gives each jam a wall time and period clock: jams follow
// each other with 30 seconds of lineup, plus a minute per timeout.
func (r *reader) estimateTimes() {
	t := r.start
	var pc int64
	period := 0
	for _, j := range r.order {
		if j.key.period != period {
			period = j.key.period
			pc = 0
			if period > 1 {
				t = t.Add(15 * time.Minute) // halftime
			}
		}
		j.t, j.pcStart = t, pc
		d := j.seconds * 1000
		if d == 0 {
			d = 120000
		}
		gap := int64(30000)
		for _, e := range j.events {
			if e == "TO" || e == "OR" || e == "OFF" {
				gap += 60000
			}
		}
		t = t.Add(time.Duration(d+gap) * time.Millisecond)
		pc += d + 30000
	}
}

// --- building the event log ---------------------------------------------

func (r *reader) add(t time.Time, typ string, fields m) {
	r.events = append(r.events, ev{t, typ, fields})
}

func (r *reader) skaterID(team int, num string) string {
	if num == "" {
		return ""
	}
	return r.skaters[fmt.Sprintf("%d/%s", team, num)]
}

func (r *reader) build() {
	t0 := r.start
	if len(r.order) == 0 {
		// An empty statsbook, only the IGRF filled in: a game still to be
		// played. Its log starts now (live events come after it); the
		// planned date and time stay in the info.
		t0 = now().UTC().Truncate(time.Millisecond)
		r.start = t0
	}
	info := map[string]any{"importedFrom": r.name, "importedStatsbook": r.revision}
	for k, v := range r.info {
		info[k] = v
	}
	created := m{"game": safeID("g-" + strings.TrimSuffix(r.name, ".xlsx")), "info": info,
		"ruleset": m{"base": "WFTDARuleset", "baseVersion": "v2025.10", "overrides": m{},
			"name": "unknown: statsbooks don't record the ruleset"}}
	var names []string
	for _, t := range r.teams {
		if n := cmp.Or(t.team, t.league); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 2 {
		created["name"] = names[0] + " vs " + names[1]
	}
	r.add(t0, "GameCreated", created)
	r.notef("the ruleset isn't in a statsbook; WFTDA defaults assumed")

	for ti, t := range r.teams {
		team := strconv.Itoa(ti + 1)
		name := t.team
		if name == "" {
			name = t.league
		}
		f := m{"team": team, "name": name}
		if t.league != "" {
			f["league"] = t.league
		}
		if t.team != "" {
			f["teamName"] = t.team
		}
		if t.color != "" {
			f["uniformColor"] = t.color
		}
		r.add(t0, "TeamSet", f)
		for _, e := range t.roster {
			id := safeID(fmt.Sprintf("t%ds%s", ti+1, e.number))
			if r.skaters[fmt.Sprintf("%d/%s", ti, e.number)] != "" {
				r.notef("team %d: roster number %s appears twice", ti+1, e.number)
				continue
			}
			r.skaters[fmt.Sprintf("%d/%s", ti, e.number)] = id
			sf := m{"team": team, "skater": id, "number": e.number}
			if e.name != "" {
				sf["name"] = e.name
			}
			// "12*" didn't skate: Not Skating, as the operator screens set it.
			var flags []string
			if t.captain != "" && e.name == t.captain {
				flags = append(flags, "C")
			}
			if e.notInGame {
				flags = append(flags, "ALT")
			}
			if len(flags) > 0 {
				sf["flags"] = strings.Join(flags, " ")
			}
			r.add(t0, "SkaterAdded", sf)
		}
	}
	r.officials(t0)

	penalties := r.readPenalties()
	boxTrips := r.boxTrips(penalties)

	// Game flow, jam by jam.
	period := 0
	var prev *jamLine
	for _, j := range r.order {
		if j.key.period != period {
			if prev != nil {
				r.add(prev.t.Add(time.Duration(prev.seconds)*time.Second), "PeriodEnded",
					m{"period": fmt.Sprintf("p%d", period), "pc": prev.pcStart + prev.seconds*1000})
			}
			period = j.key.period
			r.add(j.t, "PeriodStarted", m{"period": fmt.Sprintf("p%d", period), "number": period, "pc": 0})
		}
		r.jam(j, penalties, boxTrips)
		prev = j
	}
	if prev != nil {
		r.add(prev.t.Add(time.Duration(prev.seconds)*time.Second), "PeriodEnded",
			m{"period": fmt.Sprintf("p%d", period), "pc": prev.pcStart + prev.seconds*1000})
	}
	for _, b := range boxTrips {
		for _, pid := range b.penalties {
			r.add(r.lastT(), "BoxTripPenaltyLinked", m{"boxTrip": b.id, "penalty": pid, "value": true})
		}
	}
	r.expulsions(penalties)
}

func (r *reader) lastT() time.Time {
	if len(r.events) == 0 {
		return r.start
	}
	return r.events[len(r.events)-1].t
}

func (r *reader) officials(t0 time.Time) {
	n := 0
	for row := igrfHNSO; row <= igrfRefLast; row++ {
		name := r.cell(sheetIGRF, 2, row)
		if name == "" {
			continue
		}
		role := r.cell(sheetIGRF, 0, row)
		switch row {
		case igrfHNSO:
			role = roleHNSO
		case igrfHR:
			role = roleHR
		}
		if role == "" {
			role = "Official"
		}
		n++
		f := m{"official": fmt.Sprintf("o%d", n), "name": name, "role": role}
		if v := r.cell(sheetIGRF, 7, row); v != "" {
			f["league"] = v
		}
		if v := r.cell(sheetIGRF, 10, row); v != "" {
			f["cert"] = v
		}
		r.add(t0, "OfficialAssigned", f)
	}
}

type penaltyEntry struct {
	id, skater, code string
	team, slot       int
	jam              *jamLine
	used             bool
}

// readPenalties reads the Penalties sheet: per skater a row of codes and a
// row of jam numbers, 9 slots per period, and the FO/EXP column.
func (r *reader) readPenalties() []*penaltyEntry {
	var out []*penaltyEntry
	usedSlot := map[string]map[int]bool{} // skater -> slots taken
	for ti, t := range r.teams {
		for n, e := range t.roster {
			codeRow := penaltiesFirstRow + 2*n
			for period := 0; period < 2; period++ {
				for slot := 1; slot <= 9; slot++ {
					col := slot + ti*penaltiesTeam2 + period*penaltiesPeriod2
					code := strings.ToUpper(r.cell(sheetPenalties, col, codeRow))
					if code == "" {
						continue
					}
					jn, err := strconv.Atoi(number(r.cell(sheetPenalties, col, codeRow+1)))
					j := r.jams[jamKey{period + 1, jn}]
					if err != nil || j == nil {
						r.notef("penalty %s of team %d #%s in period %d: jam %q isn't on the Score sheet; skipped",
							code, ti+1, e.number, period+1, r.cell(sheetPenalties, col, codeRow+1))
						continue
					}
					skaterKey := fmt.Sprintf("%d/%s", ti, e.number)
					if usedSlot[skaterKey] == nil {
						usedSlot[skaterKey] = map[int]bool{}
					}
					useSlot := slot
					if usedSlot[skaterKey][slot] {
						// Period 2 should continue after period 1's penalties
						// (StatsBook Manual, Penalties); some sheets start over.
						for useSlot = 1; useSlot <= 9 && usedSlot[skaterKey][useSlot]; useSlot++ {
						}
						r.notef("team %d #%s: penalty slot %d used twice; moved to slot %d", ti+1, e.number, slot, useSlot)
						if useSlot > 9 {
							r.notef("team %d #%s: more than 9 penalties; the extra one is skipped", ti+1, e.number)
							continue
						}
					}
					usedSlot[skaterKey][useSlot] = true
					out = append(out, &penaltyEntry{id: safeID(fmt.Sprintf("t%ds%sp%dp%d", ti+1, e.number, period+1, slot)),
						skater: r.skaterID(ti, e.number), code: code, team: ti, slot: useSlot, jam: j})
				}
			}
		}
	}
	r.penaltyCount = len(out)
	return out
}

// expulsions reads the FO/EXP column: FO is derived, an expulsion code is
// recorded against the regular penalty with that code and jam.
func (r *reader) expulsions(penalties []*penaltyEntry) {
	infos := map[int]string{}
	for i, row := range igrfExpulsionRows {
		infos[i] = r.cell(sheetIGRF, 0, row)
	}
	k := 0
	for ti, t := range r.teams {
		for n, e := range t.roster {
			codeRow := penaltiesFirstRow + 2*n
			for period := 0; period < 2; period++ {
				col := penaltiesFOEXP + ti*penaltiesTeam2 + period*penaltiesPeriod2
				code := strings.ToUpper(r.cell(sheetPenalties, col, codeRow))
				if code == "" || code == "FO" {
					continue
				}
				jn, _ := strconv.Atoi(number(r.cell(sheetPenalties, col, codeRow+1)))
				var match *penaltyEntry
				for _, p := range penalties {
					if p.team == ti && p.skater == r.skaterID(ti, e.number) && p.code == code && p.jam.key == (jamKey{period + 1, jn}) {
						match = p
					}
				}
				if match == nil {
					r.notef("expulsion %s of team %d #%s: no matching penalty; skipped", code, ti+1, e.number)
					continue
				}
				info := infos[k]
				if info == "" || strings.HasPrefix(info, "Expulsion") {
					info = fmt.Sprintf("#%s period %d jam %d, %s", e.number, period+1, jn, code)
				}
				r.add(r.lastT(), "ExpulsionRecorded", m{"penalty": match.id, "info": info,
					"suspension": strings.EqualFold(r.cell(sheetIGRF, 11, igrfExpulsionRows[min(k, len(igrfExpulsionRows)-1)]), "YES")})
				k++
			}
		}
	}
}

type boxTrip struct {
	id, skater, team, position string
	jammer                     bool
	start, end                 *jamLine
	startBetween, startAfterSP bool
	endBetween, endAfterSP     bool
	penalties                  []string
}

// boxTrips rebuilds box trips from the lineup symbols (StatsBook Manual,
// Lineups). Each symbol stands for one trip overlapping that part of the jam,
// in order: "S" in the box from the start (or from before the star pass),
// "$" the same but released during it, "-" sat during it, "+" sat and
// released. Trips a skater is still in at the end of a jam continue into
// their next jam only as far as that jam's line starts with S or $; the
// others ended between jams.
func (r *reader) boxTrips(penalties []*penaltyEntry) []*boxTrip {
	var out []*boxTrip
	for ti := range r.teams {
		team := strconv.Itoa(ti + 1)
		open := map[string][]*boxTrip{} // skater number -> trips still open, oldest first
		newTrip := func(num, pos string, j *jamLine) *boxTrip {
			b := &boxTrip{id: fmt.Sprintf("t%dbt%d", ti+1, len(out)+1), skater: num, team: team,
				position: pos, jammer: pos == "Jammer", start: j}
			out = append(out, b)
			return b
		}
		var lastJam *jamLine
		for _, j := range r.order {
			tl := j.teams[ti]
			posOf := map[string]string{}
			for _, pos := range lineupPositions {
				if n := tl.lineup[pos]; n != "" {
					posOf[n] = pos
				}
			}
			for num, trips := range open {
				continuing := 0
				if pos, ok := posOf[num]; ok {
					for _, s := range withoutThree(tl.syms[pos]) {
						if n := oldSymbols[s]; n != "S" && n != "$" && n != "X" {
							break
						}
						continuing++
					}
				}
				for _, b := range trips[min(continuing, len(trips)):] {
					b.end, b.endBetween = lastJam, true
				}
				open[num] = trips[:min(continuing, len(trips))]
			}
			for _, pos := range lineupPositions {
				num := tl.lineup[pos]
				if num == "" {
					continue
				}
				// Before the star pass (or the whole jam).
				carried := open[num]
				var alive []*boxTrip
				where := fmt.Sprintf("jam %d/%d team %d #%s", j.key.period, j.key.number, ti+1, num)
				for _, s := range withoutThree(r.normaliseSymbols(where, tl.syms[pos])) {
					if s == "X" { // old notation: released during this jam
						if len(carried) > 0 {
							s = "$"
						} else {
							s = "+"
						}
					}
					switch s {
					case "S", "$":
						var b *boxTrip
						if len(carried) > 0 {
							b, carried = carried[0], carried[1:]
						} else {
							b = newTrip(num, pos, j)
							b.startBetween = true
						}
						if s == "$" {
							b.end = j
						} else {
							alive = append(alive, b)
						}
					case "-", "+":
						b := newTrip(num, pos, j)
						if s == "+" {
							b.end = j
						} else {
							alive = append(alive, b)
						}
					default:
						r.notef("jam %d/%d team %d #%s: box symbol %q not understood", j.key.period, j.key.number, ti+1, num, s)
					}
				}
				// After the star pass: first the trips still open, then new ones.
				if syms := withoutThree(r.normaliseSymbols(where, tl.symsSP[pos])); len(syms) > 0 {
					var after []*boxTrip
					for _, s := range syms {
						if s == "X" {
							if len(alive) > 0 {
								s = "$"
							} else {
								s = "+"
							}
						}
						switch s {
						case "S", "$":
							var b *boxTrip
							if len(alive) > 0 {
								b, alive = alive[0], alive[1:]
							} else {
								b = newTrip(num, pos, j)
								b.startAfterSP = true
							}
							if s == "$" {
								b.end, b.endAfterSP = j, true
							} else {
								after = append(after, b)
							}
						case "-", "+":
							b := newTrip(num, pos, j)
							b.startAfterSP = true
							if s == "+" {
								b.end, b.endAfterSP = j, true
							} else {
								after = append(after, b)
							}
						default:
							r.notef("jam %d/%d team %d #%s: box symbol %q not understood", j.key.period, j.key.number, ti+1, num, s)
						}
					}
					// Trips open before the pass but not on the SP line ended with it.
					for _, b := range alive {
						b.end = j
					}
					alive = after
				}
				open[num] = alive
			}
			lastJam = j
		}
	}
	// Link penalties to trips in order: each trip takes the skater's earliest
	// unlinked penalty from its start jam or before.
	idx := map[*jamLine]int{}
	for i, j := range r.order {
		idx[j] = i
	}
	sort.SliceStable(out, func(a, b int) bool { return idx[out[a].start] < idx[out[b].start] })
	for _, b := range out {
		for _, p := range penalties {
			if p.used || p.team+1 != mustAtoi(b.team) || p.skater != r.skaterID(p.team, b.skater) {
				continue
			}
			if idx[p.jam] <= idx[b.start] {
				p.used = true
				b.penalties = append(b.penalties, p.id)
				break
			}
		}
	}
	return out
}

// oldSymbols maps notation from older statsbooks (2015-2018: "|" in the box,
// "/" sat during the jam, "X" released) and common variants to today's
// symbols. "X" is resolved by the reader: "+" or "$" depending on whether the
// trip started in that jam.
var oldSymbols = map[string]string{
	"|": "S", "I": "S", "/": "-", "_": "-", "–": "-", "—": "-",
	"/+": "+", ".+": "+", "X": "X", "+": "+", "-": "-", "S": "S", "$": "$", "3": "3",
}

// normaliseSymbols translates a cell's box symbols; unknown ones are noted.
func (r *reader) normaliseSymbols(where string, syms []string) []string {
	var out []string
	for _, s := range syms {
		if n, ok := oldSymbols[s]; ok {
			if n != s && n != "X" {
				r.notef("%s: box symbol %q read as %q", where, s, n)
			}
			out = append(out, n)
			continue
		}
		r.notef("%s: box symbol %q not understood", where, s)
	}
	return out
}

func withoutThree(syms []string) []string {
	var out []string
	for _, s := range syms {
		if s != "3" {
			out = append(out, s)
		}
	}
	return out
}

func mustAtoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// jam emits one jam: lineups, trips, flags, penalties, box trips, its end
// and the timeouts after it.
func (r *reader) jam(j *jamLine, penalties []*penaltyEntry, boxTrips []*boxTrip) {
	r.add(j.t, "JamUpcoming", m{"jam": j.id, "number": j.key.number})
	for ti, tl := range j.teams {
		team := strconv.Itoa(ti + 1)
		for _, pos := range lineupPositions {
			f := m{"jam": j.id, "team": team, "position": pos}
			if id := r.skaterID(ti, tl.lineup[pos]); id != "" {
				f["skater"] = id
			} else if tl.lineup[pos] != "" {
				r.notef("jam %d/%d team %d %s: #%s isn't on the roster", j.key.period, j.key.number, ti+1, pos, tl.lineup[pos])
			}
			if tl.notFielded[pos] {
				f["notFielded"] = true
			}
			for _, s := range append(append([]string{}, tl.syms[pos]...), tl.symsSP[pos]...) {
				if s == "3" {
					f["sitFor3"] = true
				}
			}
			if len(f) > 3 {
				r.add(j.t, "FieldingSet", f)
			}
		}
	}
	for _, b := range boxTrips {
		if b.start == j && b.startBetween {
			r.boxStart(b, j)
		}
	}
	r.add(j.t, "JamStarted", m{"period": fmt.Sprintf("p%d", j.key.period), "jam": j.id, "number": j.key.number, "pc": j.pcStart})
	dur := j.seconds * 1000
	end := j.t.Add(time.Duration(dur) * time.Millisecond)

	for ti, tl := range j.teams {
		team := strconv.Itoa(ti + 1)
		// Trips: trip 1 is implicit; the initial trip is after the star pass
		// when NI is marked on the jammer line with an SP line.
		spTrip := ""
		if tl.pivotAfterSP != "" && tl.niBase {
			spTrip = fmt.Sprintf("%s/%s/t1", j.id, team)
		}
		for i, t := range tl.trips {
			id := fmt.Sprintf("%s/%s/t%d", j.id, team, i+2)
			r.add(j.t, "TripStarted", m{"jam": j.id, "team": team, "trip": id, "afterStarPass": t.afterSP, "jc": int64(0)})
			if t.points != 0 {
				r.add(j.t, "TripPointsSet", m{"trip": id, "points": t.points})
			}
			if t.afterSP && spTrip == "" {
				spTrip = id
			}
			if t.note != "" {
				r.add(j.t, "Annotated", m{"target": m{"kind": "trip", "id": id}, "text": t.note})
			}
		}
		if tl.pivotAfterSP != "" {
			if spTrip == "" {
				// Star passed, but no trip on the SP line: the last trip.
				spTrip = fmt.Sprintf("%s/%s/t%d", j.id, team, len(tl.trips)+1)
				r.notef("jam %d/%d team %d: star pass with no trip on the SP line; put on the last trip", j.key.period, j.key.number, ti+1)
			}
			r.add(j.t, "StarPassSet", m{"jam": j.id, "team": team, "trip": spTrip, "jc": int64(0)})
		}
		for _, fl := range []struct {
			name string
			on   bool
		}{{"lead", tl.lead}, {"lost", tl.lost}, {"calloff", tl.call}, {"noPivot", tl.noPivot}} {
			if fl.on {
				r.add(j.t, "JamFlagSet", m{"jam": j.id, "team": team, "flag": fl.name, "value": true, "jc": dur})
			}
		}
		if tl.osOffset != 0 {
			r.add(j.t, "OsOffsetSet", m{"jam": j.id, "team": team, "offset": tl.osOffset, "reason": tl.osReason})
		}
		if tl.skNote != "" {
			r.add(j.t, "Annotated", m{"target": m{"kind": "teamJamSk", "id": j.id + "/" + team}, "text": tl.skNote})
		}
		if tl.ltNote != "" {
			r.add(j.t, "Annotated", m{"target": m{"kind": "teamJamLt", "id": j.id + "/" + team}, "text": tl.ltNote})
		}
	}
	for _, p := range penalties {
		if p.jam != j {
			continue
		}
		f := m{"penalty": p.id, "code": p.code, "jam": j.id, "slot": p.slot, "pc": j.pcStart, "jc": int64(0)}
		if p.skater == "" {
			r.notef("penalty %s in jam %d/%d: skater not on the roster; skipped", p.code, j.key.period, j.key.number)
			continue
		}
		f["skater"] = p.skater
		r.add(j.t, "PenaltyIssued", f)
	}
	for _, b := range boxTrips {
		if b.start == j && !b.startBetween {
			r.boxStart(b, j)
		}
		if b.end == j && !b.endBetween {
			r.boxEnd(b, j)
		}
	}
	reason := "unknown"
	switch {
	case j.injury:
		reason = "injury"
	case j.teams[0].call || j.teams[1].call:
		reason = "calloff"
	case dur >= 120000:
		reason = "time"
	}
	r.add(end, "JamEnded", m{"jam": j.id, "reason": reason, "pc": j.pcStart + dur, "jc": dur})
	for _, b := range boxTrips {
		if b.end == j && b.endBetween {
			r.boxEnd(b, j)
		}
	}
	for k, e := range j.events {
		owner := ""
		switch e {
		case "TO", "OR":
			if k < len(j.details) {
				owner = r.teamByColor(j.details[k])
			}
		case "OFF":
			owner = "O"
		default:
			continue
		}
		id := fmt.Sprintf("%sto%d", j.id, k+1)
		r.add(end, "TimeoutStarted", m{"timeout": id, "afterJam": j.id, "owner": owner, "review": e == "OR", "pc": j.pcStart + dur})
		r.add(end.Add(time.Minute), "TimeoutEnded", m{"timeout": id, "duration": int64(60000), "pc": j.pcStart + dur})
	}
}

func (r *reader) teamByColor(detail string) string {
	d := strings.ToLower(strings.TrimSpace(detail))
	for i, t := range r.teams {
		if t.color != "" && strings.HasPrefix(d, strings.ToLower(t.color)) {
			return strconv.Itoa(i + 1)
		}
	}
	return ""
}

func (r *reader) boxStart(b *boxTrip, j *jamLine) {
	f := m{"boxTrip": b.id, "team": b.team, "jammer": b.jammer, "position": b.position, "jam": j.id,
		"betweenJams": b.startBetween, "afterStarPass": b.startAfterSP, "pc": j.pcStart, "jc": int64(0)}
	if id := r.skaterID(mustAtoi(b.team)-1, b.skater); id != "" {
		f["skater"] = id
	}
	r.add(j.t, "BoxTripStarted", f)
}

func (r *reader) boxEnd(b *boxTrip, j *jamLine) {
	r.add(j.t, "BoxTripEnded", m{"boxTrip": b.id, "jam": j.id, "betweenJams": b.endBetween,
		"afterStarPass": b.endAfterSP, "pc": j.pcStart, "jc": j.seconds * 1000})
}

func (r *reader) render() ([]byte, error) {
	var buf bytes.Buffer
	for i, e := range r.events {
		line := m{"v": 1, "seq": i + 1, "t": e.t.UTC().Format("2006-01-02T15:04:05.000Z"), "type": e.typ}
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

// now is the time an empty statsbook's game is created; tests set it.
var now = time.Now
