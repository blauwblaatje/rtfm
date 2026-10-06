package statsbook

import (
	"bytes"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"crgformat/derive"
	"crgformat/replay"
)

// Export fills a blank WFTDA statsbook (the template's bytes) with a game.
// It follows the Java version's exporter (StatsbookExporter), but works from
// the summary and derived values, and goes through jams in order rather than
// by number. Warnings list what couldn't be put on the statsbook.
func Export(s *replay.Summary, template []byte) ([]byte, []string, error) {
	template, err := alignCommentParts(template)
	if err != nil {
		return nil, nil, fmt.Errorf("preparing the blank statsbook: %w", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(template))
	if err != nil {
		return nil, nil, fmt.Errorf("reading the blank statsbook: %w", err)
	}
	defer f.Close()
	for _, sh := range []string{sheetIGRF, sheetScore, sheetPenalties, sheetLineups, sheetOSOffset, sheetClock, sheetReviews} {
		if idx, _ := f.GetSheetIndex(sh); idx < 0 {
			return nil, nil, fmt.Errorf("the blank statsbook has no %q sheet", sh)
		}
	}
	x := &exporter{f: f, g: derive.New(s), s: s}
	x.syms = x.g.BoxSymbols()
	x.officialsByRole()
	x.igrf()
	x.penalties()
	if err := x.jams(); err != nil {
		return nil, x.warnings, err
	}
	x.writeComments()
	for _, e := range x.errs {
		return nil, x.warnings, e
	}
	full := true
	if err := f.SetCalcProps(&excelize.CalcPropsOptions{FullCalcOnLoad: &full}); err != nil {
		return nil, x.warnings, err
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, x.warnings, err
	}
	return buf.Bytes(), x.warnings, nil
}

type exporter struct {
	f        *excelize.File
	g        *derive.Game
	s        *replay.Summary
	syms     map[string]*derive.Symbols
	warnings []string
	errs     []error

	comments     map[[2]string][]string
	commentOrder [][2]string

	// Official names for sheet headers, per period and team.
	sk, jr, lt [2][2]string
	pt         string
}

func (x *exporter) warnf(format string, a ...any) {
	x.warnings = append(x.warnings, fmt.Sprintf(format, a...))
}

// --- cell helpers ------------------------------------------------------

func (x *exporter) str(sheet string, col, row int, v string) {
	if v == "" {
		return
	}
	if err := x.f.SetCellStr(sheet, cellName(col, row), v); err != nil {
		x.errs = append(x.errs, err)
	}
}

func (x *exporter) num(sheet string, col, row int, v float64) {
	if err := x.f.SetCellFloat(sheet, cellName(col, row), v, -1, 64); err != nil {
		x.errs = append(x.errs, err)
	}
}

func (x *exporter) formula(sheet string, col, row int, v string) {
	if err := x.f.SetCellFormula(sheet, cellName(col, row), v); err != nil {
		x.errs = append(x.errs, err)
	}
}

// comment adds a cell comment; several on one cell are joined.
func (x *exporter) comment(sheet string, col, row int, text string) {
	if text == "" {
		return
	}
	if x.comments == nil {
		x.comments = map[[2]string][]string{}
	}
	k := [2]string{sheet, cellName(col, row)}
	if len(x.comments[k]) == 0 {
		x.commentOrder = append(x.commentOrder, k)
	}
	x.comments[k] = append(x.comments[k], text)
}

// writeComments adds the comments. Where the blank statsbook has a help
// comment on the same cell, the game's comment replaces it.
func (x *exporter) writeComments() {
	existing := map[string]map[string]bool{}
	for _, k := range x.commentOrder {
		if existing[k[0]] == nil {
			existing[k[0]] = map[string]bool{}
			list, _ := x.f.GetComments(k[0])
			for _, c := range list {
				existing[k[0]][c.Cell] = true
			}
		}
		if existing[k[0]][k[1]] {
			if err := x.f.DeleteComment(k[0], k[1]); err != nil {
				x.errs = append(x.errs, err)
			}
		}
		c := excelize.Comment{Author: "CRG", Cell: k[1], Text: strings.Join(x.comments[k], "; ")}
		if err := x.f.AddComment(k[0], c); err != nil {
			x.errs = append(x.errs, err)
		}
	}
}

// excelDate converts a date (and time) to an Excel serial number.
func excelDate(t time.Time) float64 {
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	return t.Sub(base).Hours() / 24
}

// --- officials ---------------------------------------------------------

// Role names as the Java version and the IGRF use them.
const (
	roleHNSO = "Head Non-Skating Official"
	roleHR   = "Head Referee"
	rolePLT  = "Penalty Lineup Tracker"
	rolePT   = "Penalty Tracker"
	roleSK   = "Scorekeeper"
	roleLT   = "Lineup Tracker"
	roleJR   = "Jammer Referee"
)

var nsoRoleOrder = []string{roleHNSO, rolePLT, rolePT, "Penalty Wrangler", "Inside Whiteboard Operator", "Jam Timer",
	roleSK, "ScoreBoard Operator", "Penalty Box Manager", "Penalty Box Timer", roleLT, "Non-Skating Official Alternate"}
var refRoleOrder = []string{roleHR, "Inside Pack Referee", roleJR, "Outside Pack Referee", "Referee Alternate"}

func roleRank(order []string, role string) int {
	for i, r := range order {
		if r == role {
			return i
		}
	}
	return len(order)
}

func (x *exporter) isRef(o *replay.Official) bool {
	return roleRank(refRoleOrder, o.Role) < len(refRoleOrder) || strings.Contains(strings.ToLower(o.Role), "referee")
}

// officialsByRole collects the names that go into sheet headers: who kept
// score, tracked lineups and was jammer ref for each team in each period.
func (x *exporter) officialsByRole() {
	for _, o := range x.s.Officials {
		t := -1
		if o.P1Team != "" {
			t = derive.TeamIndex(o.P1Team)
		}
		set := func(slot *[2][2]string) {
			if t < 0 {
				return
			}
			slot[0][t] = o.Name
			if o.Swap {
				slot[1][1-t] = o.Name
			} else {
				slot[1][t] = o.Name
			}
		}
		switch o.Role {
		case roleSK:
			set(&x.sk)
		case roleJR:
			set(&x.jr)
		case roleLT:
			set(&x.lt)
		case rolePLT:
			set(&x.lt)
			x.pt = joinNames(x.pt, o.Name)
		case rolePT:
			x.pt = joinNames(x.pt, o.Name)
		}
	}
}

func joinNames(a, b string) string {
	if a == "" {
		return b
	}
	return a + " / " + b
}

// --- IGRF --------------------------------------------------------------

func (x *exporter) igrf() {
	info := x.s.Info
	for _, c := range []struct {
		p   pos
		key string
	}{{igrfVenue, "Venue"}, {igrfCity, "City"}, {igrfState, "State"}, {igrfGameNo, "GameNo"},
		{igrfTournament, "Tournament"}, {igrfHost, "HostLeague"}} {
		x.str(sheetIGRF, c.p.col, c.p.row, info[c.key])
	}
	if d, err := time.Parse("2006-01-02", info["Date"]); err == nil {
		x.num(sheetIGRF, igrfDate.col, igrfDate.row, excelDate(d))
		for _, layout := range []string{"15:04:05", "15:04"} {
			if st, err := time.Parse(layout, info["StartTime"]); err == nil {
				x.num(sheetIGRF, igrfStartTime.col, igrfStartTime.row,
					excelDate(d.Add(time.Duration(st.Hour())*time.Hour+time.Duration(st.Minute())*time.Minute)))
				break
			}
		}
	}

	for i, t := range x.s.Teams {
		col := igrfTeamCol(i)
		x.str(sheetIGRF, col, igrfLeague, t.League)
		x.str(sheetIGRF, col, igrfTeam, t.TeamName)
		color := t.UniformColor
		if x.s.CueMode == "name" && t.NameCue != "" { // Team Name Cues Test Procedures: "Yellow; Cue: Street Cats"
			color = strings.TrimPrefix(color+"; Cue: "+t.NameCue, "; ")
		}
		x.str(sheetIGRF, col, igrfColor, color)
		for n, sk := range x.roster(t) {
			row := igrfRosterFirst + n
			if row > igrfRosterLast {
				x.warnf("team %s has more than 20 skaters; the rest are not on the IGRF", t.Team)
				break
			}
			num := sk.Number
			if sk.Status == "notInGame" || slices.Contains(strings.Fields(sk.Flags), "ALT") {
				num += "*" // StatsBook Manual: an asterisk marks a skater who didn't skate
			}
			x.str(sheetIGRF, col, row, num)
			x.str(sheetIGRF, col+1, row, sk.Name)
		}
		for _, sk := range t.Skaters {
			if hasFlag(sk.Flags, "C") {
				x.str(sheetIGRF, col, igrfCaptains, sk.Name)
				x.str(sheetClock, 1, 4+2*i, sk.Name)
				x.str(sheetClock, 1, clockPeriodRows+4+2*i, sk.Name)
			}
			if hasFlag(sk.Flags, "A") {
				x.str(sheetClock, 1, 5+2*i, sk.Name)
				x.str(sheetClock, 1, clockPeriodRows+5+2*i, sk.Name)
			}
		}
		for _, st := range t.Staff {
			if hasFlag(st.Flags, "A") {
				x.str(sheetClock, 1, 5+2*i, st.Name)
				x.str(sheetClock, 1, clockPeriodRows+5+2*i, st.Name)
			}
		}
	}

	// Expulsions and suspensions (Section 2).
	suspension := false
	r := x.s.Result
	if r != nil && len(r.SuspensionsServed) > 0 {
		var names []string
		for _, id := range r.SuspensionsServed {
			if sk := x.g.SkaterByID[id]; sk != nil {
				names = append(names, sk.Name+" #"+sk.Number)
			}
		}
		x.str(sheetIGRF, igrfSuspensionServed.col, igrfSuspensionServed.row, strings.Join(names, ", "))
		suspension = true
	}
	for i, e := range x.s.Expulsions {
		suspension = suspension || e.Suspension
		if i >= len(igrfExpulsionRows) {
			x.warnf("more than %d expulsions; the rest are not in IGRF section 2", len(igrfExpulsionRows))
			break
		}
		row := igrfExpulsionRows[i]
		x.str(sheetIGRF, 0, row, strings.TrimSpace(e.Info+" "+e.ExtraInfo))
		x.str(sheetIGRF, 11, row, yesNo(e.Suspension))
	}
	if len(x.s.Expulsions) > 0 || suspension {
		x.str(sheetIGRF, igrfSuspension.col, igrfSuspension.row, yesNo(suspension))
	}
	// The 2025 template says "Official Reviews", the 2019 one "Offical Reviews".
	if label, _ := x.f.GetCellValue(sheetIGRF, igrfReviewsLabel.cell()); strings.HasPrefix(label, "Offic") {
		reviews := false
		for _, p := range x.s.Periods {
			for _, to := range p.Timeouts {
				reviews = reviews || to.Review
			}
		}
		x.str(sheetIGRF, igrfReviewsYesNo.col, igrfReviewsYesNo.row, yesNo(reviews))
		x.str(sheetIGRF, igrfExpulsionsYesNo.col, igrfExpulsionsYesNo.row, yesNo(len(x.s.Expulsions) > 0))
	}

	// Officials (Section 4).
	nsos, refs := []*replay.Official{}, []*replay.Official{}
	for _, o := range x.s.Officials {
		if x.isRef(o) {
			refs = append(refs, o)
		} else {
			nsos = append(nsos, o)
		}
	}
	sortOfficials(nsos, nsoRoleOrder)
	sortOfficials(refs, refRoleOrder)
	hr, hnso := derive.Heads(x.s)
	row := igrfNSOFirst
	for _, o := range nsos {
		if (o.ID == hnso || (hnso == "" && o.Role == roleHNSO)) && x.officialRow(igrfHNSO, o, false) {
			continue
		}
		if row > igrfNSOLast {
			x.warnf("more NSOs than rows on the IGRF")
			break
		}
		x.officialRow(row, o, true)
		row++
	}
	row = igrfRefFirst
	for _, o := range refs {
		if (o.ID == hr || (hr == "" && o.Role == roleHR)) && x.officialRow(igrfHR, o, false) {
			continue
		}
		if row > igrfRefLast {
			x.warnf("more referees than rows on the IGRF")
			break
		}
		x.officialRow(row, o, true)
		row++
	}
}

// officialRow fills a Section 4 row; the head official rows are used once.
func (x *exporter) officialRow(row int, o *replay.Official, withRole bool) bool {
	if !withRole {
		if v, _ := x.f.GetCellValue(sheetIGRF, cellName(2, row)); v != "" {
			return false // head official row already taken
		}
	}
	if withRole {
		x.str(sheetIGRF, 0, row, o.Role)
	}
	x.str(sheetIGRF, 2, row, o.Name)
	x.str(sheetIGRF, 7, row, o.League)
	x.str(sheetIGRF, 10, row, o.Cert)
	return true
}

func sortOfficials(os []*replay.Official, order []string) {
	sort.SliceStable(os, func(i, j int) bool {
		ri, rj := roleRank(order, os[i].Role), roleRank(order, os[j].Role)
		if ri != rj {
			return ri < rj
		}
		return os[i].Name < os[j].Name
	})
}

// roster returns a team's skaters in statsbook order: roster numbers sorted
// character by character, like words (StatsBook Manual: 3000 before 4).
func (x *exporter) roster(t *replay.Team) []*replay.Skater {
	out := append([]*replay.Skater(nil), t.Skaters...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

func hasFlag(flags, f string) bool {
	for _, x := range strings.Fields(flags) {
		if x == f {
			return true
		}
	}
	return false
}

func yesNo(b bool) string {
	if b {
		return "YES"
	}
	return "NO"
}

// --- Penalties ---------------------------------------------------------

func (x *exporter) penalties() {
	x.str(sheetPenalties, 13, 0, x.pt)
	x.str(sheetPenalties, 13+penaltiesPeriod2, 0, x.pt)
	bySkater := map[string][]*replay.Penalty{}
	for _, p := range x.s.Penalties {
		if p.Staff != "" {
			x.warnf("penalty %s for team staff is not on the statsbook (Non-Skater Expulsions isn't filled in yet)", p.Code)
			continue
		}
		bySkater[p.Skater] = append(bySkater[p.Skater], p)
	}
	foulOuts := x.g.FoulOuts()
	expelled := map[string]*replay.Penalty{}
	for _, e := range x.s.Expulsions {
		if p := x.g.Penalties[e.Penalty]; p != nil && p.Skater != "" {
			expelled[p.Skater] = p
		}
	}
	for ti, t := range x.s.Teams {
		for n, sk := range x.roster(t) {
			if n >= igrfRosterLast-igrfRosterFirst+1 {
				break
			}
			codeRow := penaltiesFirstRow + 2*n
			for _, p := range bySkater[sk.ID] {
				x.penaltyCell(ti, codeRow, p.Slot, p.Code, p.Jam, p.Annotation, sk.Number)
			}
			// FO/EXP (StatsBook Manual, Penalties): an expulsion shows its
			// penalty's code; a foul-out FO in the jam of the limit-th penalty.
			if p := expelled[sk.ID]; p != nil {
				x.penaltyCell(ti, codeRow, penaltiesFOEXP, p.Code, p.Jam, "", sk.Number)
			} else if p := foulOuts[sk.ID]; p != nil {
				x.penaltyCell(ti, codeRow, penaltiesFOEXP, "FO", p.Jam, "", sk.Number)
			}
		}
	}
}

func (x *exporter) penaltyCell(team, codeRow, col int, code, jamID, annotation, number string) {
	jp := x.g.JamByID[jamID]
	if jp == nil || jp.Upcoming || jp.Period < 1 || jp.Period > 2 {
		x.warnf("penalty %s of #%s is not in a jam of period 1 or 2; not on the statsbook", code, number)
		return
	}
	col += team * penaltiesTeam2
	if jp.Period == 2 {
		col += penaltiesPeriod2
	}
	x.str(sheetPenalties, col, codeRow, code)
	x.comment(sheetPenalties, col, codeRow, annotation)
	x.num(sheetPenalties, col, codeRow+1, float64(jp.Jam.Number))
}

// --- Score, Lineups, OS Offset, Game Clock, Official Reviews -----------

func (x *exporter) jams() error {
	toCol := [2]int{3, 3}
	var osReasons []string
	for pi, p := range x.s.Periods {
		if pi > 1 {
			x.warnf("period %d is not on the statsbook (it has two periods)", p.Number)
			continue
		}
		x.str(sheetScore, 11, headRow(pi), x.sk[pi][0])
		x.str(sheetScore, 11+scoreTeamCol2, headRow(pi), x.sk[pi][1])
		x.str(sheetScore, 14, headRow(pi), x.jr[pi][0])
		x.str(sheetScore, 14+scoreTeamCol2, headRow(pi), x.jr[pi][1])
		x.str(sheetLineups, 15, headRow(pi), x.lt[pi][0])
		x.str(sheetLineups, 15+lineupsTeamCol2, headRow(pi), x.lt[pi][1])
		toCol = x.timeouts(pi, p, toCol)

		row := jamRow(pi)
		for ji, j := range p.Jams {
			lines := 1
			if j.Teams[0].StarPassTrip != "" || j.Teams[1].StarPassTrip != "" {
				lines = 2
			}
			if row+lines-jamRow(pi) > periodRows {
				need := 0
				for _, jj := range p.Jams {
					need++
					if jj.Teams[0].StarPassTrip != "" || jj.Teams[1].StarPassTrip != "" {
						need++
					}
				}
				return fmt.Errorf("the statsbook needs %d more row(s) in period %d", need-periodRows, pi+1)
			}
			for ti, tj := range j.Teams {
				x.scoreTeamJam(row, lines == 2, []int{scoreTeamCol1, scoreTeamCol2}[ti], j, tj)
				x.lineupsTeamJam(row, []int{0, lineupsTeamCol2}[ti], j, tj)
				if tj.OsOffset != 0 {
					c := []int{0, osOffsetCol2}[ti]
					x.num(sheetOSOffset, c+1, row, float64(tj.OsOffset))
					x.str(sheetOSOffset, c+2, row, tj.OsOffsetReason)
					osReasons = append(osReasons, tj.OsOffsetReason)
				}
			}
			x.clockJam(pi, ji, j)
			row += lines
		}
	}
	if len(osReasons) > 0 {
		x.str(sheetIGRF, igrfOSAdjusted.col, igrfOSAdjusted.row, "yes")
		x.str(sheetIGRF, igrfOSReason.col, igrfOSReason.row, strings.Join(osReasons, ", "))
	}
	return nil
}

func (x *exporter) number(skaterID string) string {
	if n := x.g.Number(skaterID); n != "" {
		return n
	}
	return "?"
}

// scoreTeamJam fills one team's jam line(s) on the Score sheet, following the
// StatsBook Manual (Score) and the Java exporter.
func (x *exporter) scoreTeamJam(row int, hasSPLine bool, base int, j *replay.Jam, tj *replay.TeamJam) {
	sp := row + 1
	jamNote := tj.SkAnnotation
	if j.Overtime {
		jamNote = strings.TrimSpace(jamNote + " Overtime Jam")
	}
	if j.InjuryContinuation {
		label := "INJ"
		if tj.Lead {
			label += "*"
		}
		x.str(sheetScore, base, row, label)
	} else {
		x.num(sheetScore, base, row, float64(j.Number))
	}
	x.comment(sheetScore, base, row, jamNote)
	if hasSPLine {
		if tj.StarPassTrip != "" {
			x.str(sheetScore, base, sp, "SP")
		} else {
			x.str(sheetScore, base, sp, "SP*")
		}
	}
	x.jammerNumber(base+1, row, tj, "Jammer")
	if tj.StarPassTrip != "" {
		x.jammerNumber(base+1, sp, tj, "Pivot")
	}
	mark := func(col int, on bool) {
		if on {
			x.str(sheetScore, base+col, row, "X")
		}
	}
	mark(2, tj.Lost)
	mark(3, tj.Lead)
	mark(4, tj.Calloff)
	mark(5, j.EndReason == "injury")

	trips := tj.Trips
	if len(trips) == 0 {
		return
	}
	lineOf := func(t *replay.Trip) int {
		if t.AfterStarPass {
			return sp
		}
		return row
	}
	niCol := base + 6
	t1 := trips[0]
	initialRow := row
	if t1.AfterStarPass {
		x.str(sheetScore, niCol, row, "X")
		initialRow = sp
	}
	if len(trips) == 1 {
		x.str(sheetScore, niCol, initialRow, "X")
	}
	x.comment(sheetScore, niCol, initialRow, t1.Annotation)
	done := 0 // index of the last trip written
	switch {
	case t1.Points == 0:
	case len(trips) == 1:
		// Points on the initial trip (overtime) with no trip after it.
		x.str(sheetScore, niCol+1, initialRow, strconv.Itoa(t1.Points)+" + NI")
		x.num(sheetScore, niCol+10, initialRow, float64(t1.Points)) // the Jam Total formula can't add that up
	case t1.AfterStarPass != trips[1].AfterStarPass:
		x.str(sheetScore, niCol+1, initialRow, strconv.Itoa(t1.Points)+" + SP")
		x.num(sheetScore, niCol+10, initialRow, float64(t1.Points))
	default:
		// StatsBook Manual: overtime initial trip points go in Trip 2 as "=4+2".
		x.formula(sheetScore, niCol+1, initialRow, fmt.Sprintf("%d+%d", t1.Points, trips[1].Points))
		x.comment(sheetScore, niCol+1, initialRow, trips[1].Annotation)
		done = 1
	}
	// Trips 2-9 get a column each; trip 10 and later share the Trip 10 column.
	for done+1 < len(trips) && done+1 < 9 {
		done++
		t := trips[done]
		col := base + 6 + done
		x.num(sheetScore, col, lineOf(t), float64(t.Points))
		x.comment(sheetScore, col, lineOf(t), t.Annotation)
	}
	if done+1 < len(trips) {
		for _, afterSP := range []bool{false, true} {
			var parts []string
			var notes []string
			for _, t := range trips[done+1:] {
				if t.AfterStarPass != afterSP {
					continue
				}
				parts = append(parts, strconv.Itoa(t.Points))
				if t.Annotation != "" {
					notes = append(notes, t.Annotation)
				}
			}
			line := row
			if afterSP {
				line = sp
			}
			switch len(parts) {
			case 0:
				continue
			case 1:
				n, _ := strconv.Atoi(parts[0])
				x.num(sheetScore, base+15, line, float64(n))
			default:
				x.formula(sheetScore, base+15, line, strings.Join(parts, "+"))
			}
			x.comment(sheetScore, base+15, line, strings.Join(notes, "; "))
		}
	}
}

// jammerNumber writes the jammer's number on the Score sheet. An unrecorded
// number is left blank with a comment (StatsBook Manual, Lineups data entry),
// since the Lineups sheet copies it from here.
func (x *exporter) jammerNumber(col, row int, tj *replay.TeamJam, pos string) {
	switch n := x.fieldingNumber(tj, pos); n {
	case "?":
		x.comment(sheetScore, col, row, "Missed Skater number")
	case "n/a":
		x.comment(sheetScore, col, row, "Skated short")
	default:
		x.str(sheetScore, col, row, n)
	}
}

// fieldingNumber is the number for a position: the skater's, "n/a" when the
// team skated short, "?" when nobody was recorded (StatsBook Manual, Lineups).
func (x *exporter) fieldingNumber(tj *replay.TeamJam, pos string) string {
	f := tj.Lineup[pos]
	switch {
	case f == nil:
		return "?"
	case f.NotFielded:
		return "n/a"
	case f.Skater == "":
		return "?"
	}
	return x.number(f.Skater)
}

// lineupsTeamJam fills one team's jam line(s) on the Lineups sheet. The
// jammer's number comes from the Score sheet by formula.
func (x *exporter) lineupsTeamJam(row, c int, j *replay.Jam, tj *replay.TeamJam) {
	x.comment(sheetLineups, c, row, tj.LtAnnotation)
	if !(j.InjuryContinuation && tj.Lead) {
		if tj.NoPivot {
			x.str(sheetLineups, c+1, row, "X")
		}
		for i, pos := range derive.Positions {
			x.fielding(row, c+2+4*i, j, tj, pos, false, pos == "Jammer")
		}
	}
	if tj.StarPassTrip != "" {
		// SP line: the pivot is now the jammer, the old jammer a blocker;
		// No Pivot is marked (StatsBook Manual, Lineups).
		x.str(sheetLineups, c+1, row+1, "X")
		order := []string{"Pivot", "Jammer", "Blocker1", "Blocker2", "Blocker3"}
		for i, pos := range order {
			x.fielding(row+1, c+2+4*i, j, tj, pos, true, i == 0)
		}
	}
}

func (x *exporter) fielding(row, col int, j *replay.Jam, tj *replay.TeamJam, pos string, afterSP, skipNumber bool) {
	f := tj.Lineup[pos]
	if !skipNumber {
		var note string
		if f != nil {
			note = f.Annotation
		}
		number := x.fieldingNumber(tj, pos)
		switch number {
		case "n/a":
			number, note = "", strings.Trim("Skated short; "+note, "; ")
		case "?":
			number, note = "", strings.Trim("Missed Skater number; "+note, "; ")
		}
		x.str(sheetLineups, col, row, number)
		x.comment(sheetLineups, col, row, note)
	}
	sym := x.syms[derive.FieldingKey(j.ID, tj.Team, pos)]
	if sym == nil {
		return
	}
	list := sym.BeforeSP
	if afterSP {
		list = sym.AfterSP
	}
	for i, s := range list {
		if i >= 3 {
			x.warnf("jam %d team %s %s: more than three box symbols; the rest are not on the Lineups sheet", j.Number, tj.Team, pos)
			break
		}
		x.str(sheetLineups, col+1+i, row, s)
	}
}

// clockJam fills a jam's line on the Game Clock sheet: its length in seconds,
// and events (StatsBook Manual, Game Clock).
func (x *exporter) clockJam(period, index int, j *replay.Jam) {
	row := period*clockPeriodRows + index + 10
	if j.Duration != nil {
		x.num(sheetClock, 1, row, float64(*j.Duration/1000))
	}
	var events, details []string
	for _, e := range x.s.Expulsions {
		if p := x.g.Penalties[e.Penalty]; p != nil && p.Jam == j.ID {
			events = append(events, "EXP")
			details = append(details, x.teamColor(p.Team)+" "+x.number(p.Skater))
		}
	}
	if j.EndReason == "injury" {
		var injured []string
		for _, tj := range j.Teams {
			for _, pos := range derive.Positions {
				if f := tj.Lineup[pos]; f != nil && f.SitFor3 {
					injured = append(injured, x.teamColor(tj.Team)+" "+x.number(f.Skater))
				}
			}
		}
		events = append(events, "INJ")
		details = append(details, strings.Join(injured, ", "))
	}
	withTime := false
	var endTime string
	for _, p := range x.s.Periods {
		for _, to := range p.Timeouts {
			if to.AfterJam != j.ID {
				continue
			}
			switch to.Owner {
			case "1", "2":
				if to.Review {
					events = append(events, "OR")
				} else {
					events = append(events, "TO")
				}
				details = append(details, x.teamColor(to.Owner))
			default:
				events = append(events, "OFF")
			}
			withTime = true
			endTime = x.periodClockShown(to)
		}
	}
	if withTime {
		details = append(details, endTime)
	}
	if len(events) > 0 {
		x.str(sheetClock, 3, row, strings.Join(events, "; "))
		x.str(sheetClock, 4, row, strings.Join(details, "; "))
	}
}

// periodClockShown is the period clock as displayed for a timeout (it counts
// down): the value at its end, which includes corrections made during the
// timeout, such as setting it back to when the timeout was requested.
func (x *exporter) periodClockShown(to *replay.Timeout) string {
	pc := to.PeriodClockEnd
	if pc == nil {
		pc = to.PeriodClock
	}
	if pc == nil {
		return ""
	}
	return derive.Clock(x.g.ClockRule("Period.Duration") - *pc)
}

func (x *exporter) teamColor(team string) string {
	t := x.s.Teams[derive.TeamIndex(team)]
	if t.UniformColor != "" {
		return t.UniformColor
	}
	return t.Name
}

// timeouts fills the timeouts and reviews of a period on the Game Clock and
// Official Reviews sheets. toCol is where each team's next team timeout goes
// (they are counted over the game).
func (x *exporter) timeouts(period int, p *replay.Period, toCol [2]int) [2]int {
	orCol := [2]int{6, 6}
	base := period * clockPeriodRows
	reviewRow := []int{3, 20}[period]
	var total int64
	totalKnown := true
	for _, to := range p.Timeouts {
		if to.Owner != "1" && to.Owner != "2" {
			continue
		}
		i := derive.TeamIndex(to.Owner)
		row := base + 4 + 2*i
		when := x.periodClockShown(to)
		if to.Review { // also when used as a timeout (Rules 1.3.2): it's still the team's review
			if orCol[i] <= 7 {
				x.str(sheetClock, orCol[i], row, when)
				if orCol[i] == 6 && (to.Retained == nil || !*to.Retained) {
					orCol[i]++
					x.str(sheetClock, orCol[i], row, "X")
				}
				orCol[i]++
			}
			retained := to.Retained != nil && *to.Retained && orCol[i] <= 7
			x.str(sheetReviews, 1, reviewRow, x.s.Teams[i].FullName)
			if jp := x.g.JamByID[to.AfterJam]; jp != nil {
				x.num(sheetReviews, 4, reviewRow, float64(jp.Jam.Number))
			}
			x.str(sheetReviews, 6, reviewRow, when)
			if to.Duration != nil && *to.Duration > 0 {
				x.str(sheetReviews, 8, reviewRow, derive.Clock(*to.Duration))
				total += *to.Duration
			} else {
				x.str(sheetReviews, 8, reviewRow, "unknown")
				totalKnown = false
			}
			x.str(sheetReviews, 10, reviewRow, yesNo(retained))
			request := to.ReviewRequest
			if to.AsTimeout && request == "" {
				request = "Taken as Team Timeout"
			}
			x.str(sheetReviews, 1, reviewRow+1, request)
			x.str(sheetReviews, 1, reviewRow+2, to.ReviewResult)
			reviewRow += 3
		} else if toCol[i] <= 5 {
			x.str(sheetClock, toCol[i], row, when)
			toCol[i]++
		}
	}
	if totalKnown {
		x.str(sheetReviews, 9, []int{15, 32}[period], derive.Clock(total))
	} else {
		x.str(sheetReviews, 9, []int{15, 32}[period], "unknown")
	}
	if period == 0 { // cross off the timeouts already used on the period 2 sheet
		for i := 0; i < 2; i++ {
			for col := 3; col < toCol[i]; col++ {
				x.str(sheetClock, col, clockPeriodRows+4+2*i, "X")
			}
		}
	}
	return toCol
}
