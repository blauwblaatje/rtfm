package tournament

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// ErrNotSanctioning is returned for a spreadsheet that isn't a tournament
// sanctioning application.
var ErrNotSanctioning = errors.New("not a WFTDA tournament sanctioning application")

// Read reads a WFTDA tournament sanctioning application (downloaded from
// Google Sheets as .xlsx): the tournament's name, dates, host league and
// venue from Basic Info, the teams with their charter links, and the
// Schedule. Schedule names are matched to the teams; matches that aren't
// exact are marked as guesses to check. The Contacts tab and the contact
// names, emails and signatures are never read: they are personal details.
//
// The application has changed over the years (2024: "League: Team" names
// and charter links as text; 2026: "League - Team" and hyperlinks), so
// fields are found by their labels and columns by their headers.
func Read(data []byte) (*Tournament, []string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	t := &Tournament{Teams: []Team{}, Games: []Game{}}
	var notes []string
	var scheduleRows, scheduleRaw [][]string
	for _, sheet := range f.GetSheetList() {
		// Dates and times are read from the cells' values (Excel dates,
		// whatever format they show), everything else as shown.
		rows, err := f.GetRows(sheet)
		raw, err2 := f.GetRows(sheet, excelize.Options{RawCellValue: true})
		if err != nil || err2 != nil || isContacts(rows) {
			continue
		}
		readLabels(t, rows, raw)
		if teams := readTeams(f, sheet, rows); len(teams) > 0 && len(t.Teams) == 0 {
			t.Teams = teams
		}
		if hasScheduleHeader(rows) {
			scheduleRows, scheduleRaw = rows, raw
		}
	}
	if t.Name == "" || len(t.Teams) == 0 && scheduleRows == nil {
		return nil, nil, ErrNotSanctioning
	}
	if scheduleRows != nil {
		notes = append(notes, readSchedule(t, scheduleRows, scheduleRaw)...)
	}
	return t, notes, nil
}

func isContacts(rows [][]string) bool {
	for _, r := range rows {
		for _, c := range r {
			if strings.EqualFold(strings.TrimSpace(c), "Contact Information") {
				return true
			}
		}
	}
	return false
}

// label is a label cell's text without its colon and a "(…)" hint:
// "Tournament Date (Use YYYY/MM/DD):" is "tournament date".
func label(c string) string {
	c = regexp.MustCompile(`\([^)]*\)`).ReplaceAllString(c, "")
	return strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(c), ":")))
}

func readLabels(t *Tournament, rows, raw [][]string) {
	fields := map[string]*string{
		"tournament name": &t.Name, "tournament date": &t.Dates, "host league": &t.HostLeague,
		"venue name": &t.Venue.Name, "street address": &t.Venue.Address, "city": &t.Venue.City,
		"zip code/postal code": &t.Venue.PostalCode, "state or province": &t.Venue.State, "country": &t.Venue.Country,
	}
	for i, r := range rows {
		for j, c := range r {
			dst := fields[label(c)]
			if dst == nil || *dst != "" {
				continue
			}
			for k := j + 1; k < len(r); k++ {
				v := strings.TrimSpace(r[k])
				if strings.HasSuffix(v, ":") {
					break
				}
				if v != "" {
					if dst == &t.Dates && i < len(raw) && k < len(raw[i]) {
						v = dateText(strings.TrimSpace(raw[i][k]))
					}
					*dst = v
					break
				}
			}
		}
	}
}

// columns finds header cells: the first column whose header contains each
// key.
func columns(header []string, keys ...string) map[string]int {
	out := map[string]int{}
	for j, c := range header {
		h := strings.ToLower(strings.TrimSpace(c))
		for _, k := range keys {
			match := strings.Contains(h, k)
			if k == "no." { // the number column; instructions contain "no." too
				match = h == "no." || h == "no"
			}
			if _, ok := out[k]; !ok && match {
				out[k] = j
			}
		}
	}
	return out
}

func cell(r []string, j int, ok bool) string {
	if !ok || j >= len(r) {
		return ""
	}
	return strings.TrimSpace(r[j])
}

var sheetLink = regexp.MustCompile(`https://docs\.google\.com/spreadsheets/\S+`)

// readTeams reads the team list: the header row has "No." and a charter
// column.
func readTeams(f *excelize.File, sheet string, rows [][]string) []Team {
	var teams []Team
	for i, r := range rows {
		cols := columns(r, "no.", "league", "charter")
		_, hasNo := cols["no."]
		_, hasName := cols["league"]
		_, hasCharter := cols["charter"]
		if !hasNo || !hasName || !hasCharter {
			continue
		}
		for k := i + 1; k < len(rows); k++ {
			row := rows[k]
			noText, name := cell(row, cols["no."], true), cell(row, cols["league"], true)
			no, err := strconv.Atoi(noText)
			if noText == "" && name != "" {
				break // the next part of the sheet
			}
			if err != nil || name == "" {
				continue // "Example", or an empty numbered line
			}
			tm := Team{No: no, Name: strings.Join(strings.Fields(name), " ")}
			ref, _ := excelize.CoordinatesToCellName(cols["charter"]+1, k+1)
			if ok, link, _ := f.GetCellHyperLink(sheet, ref); ok && sheetLink.MatchString(link) {
				tm.CharterURL = link
			} else if m := sheetLink.FindString(cell(row, cols["charter"], true)); m != "" {
				tm.CharterURL = m
			}
			teams = append(teams, tm)
		}
		return teams
	}
	return nil
}

func hasScheduleHeader(rows [][]string) bool {
	for _, r := range rows {
		c := columns(r, "home team", "visiting team")
		if len(c) == 2 {
			return true
		}
	}
	return false
}

var slotRef = regexp.MustCompile(`(?i)^(winner|loser)\s+(?:of\s+)?game\s*#?\s*(\d+)$`)

// readSchedule reads the games and matches their teams.
func readSchedule(t *Tournament, rows, raw [][]string) []string {
	var notes []string
	m := newMatcher(t.Teams)
	for i, r := range rows {
		cols := columns(r, "no.", "date", "time", "home team", "visiting team", "game type", "track", "notes")
		if _, ok := cols["home team"]; !ok {
			continue
		}
		if _, ok := cols["visiting team"]; !ok {
			continue
		}
		// The colours are the column right of each team, "Colors" in the
		// line under the header.
		colorOf := map[string]int{}
		if i+1 < len(rows) {
			for _, side := range []string{"home team", "visiting team"} {
				j := cols[side] + 1
				if strings.Contains(strings.ToLower(cell(rows[i+1], j, true)), "color") {
					colorOf[side] = j
				}
			}
		}
		get := func(r []string, key string) string { j, ok := cols[key]; return cell(r, j, ok) }
		for k := i + 1; k < len(rows); k++ {
			row := rows[k]
			var rawRow []string
			if k < len(raw) {
				rawRow = raw[k]
			}
			no, err := strconv.Atoi(get(row, "no."))
			home, away := get(row, "home team"), get(row, "visiting team")
			if err != nil || home == "" && away == "" {
				continue
			}
			g := Game{No: no, Date: dateText(get(rawRow, "date")), Time: timeText(get(rawRow, "time")),
				Type: get(row, "game type"), Track: get(row, "track"), Notes: get(row, "notes")}
			for _, side := range []struct {
				key string
				dst *Slot
			}{{"home team", &g.Home}, {"visiting team", &g.Away}} {
				text := strings.Join(strings.Fields(get(row, side.key)), " ")
				s := Slot{Text: text}
				if j, ok := colorOf[side.key]; ok {
					s.Color = cell(row, j, true)
				}
				if mm := slotRef.FindStringSubmatch(text); mm != nil {
					n, _ := strconv.Atoi(mm[2])
					s.From = &Ref{Game: n, Winner: strings.EqualFold(mm[1], "winner")}
				} else if no, exact := m.match(text); no > 0 {
					s.Team, s.Guess = no, !exact
				} else {
					notes = append(notes, fmt.Sprintf("game %d: %q isn't one of the teams; pick it", g.No, text))
				}
				*side.dst = s
			}
			if g.Date == "" && get(row, "date") != "" {
				notes = append(notes, fmt.Sprintf("game %d: can't read the date %q", g.No, get(row, "date")))
			}
			t.Games = append(t.Games, g)
		}
		break
	}
	notes = append(notes, fixDayMonth(t)...)
	// A reference to a game that isn't on the schedule can't be resolved.
	for i := range t.Games {
		for _, s := range []*Slot{&t.Games[i].Home, &t.Games[i].Away} {
			if s.From != nil && t.GameByNo(s.From.Game) == nil {
				notes = append(notes, fmt.Sprintf("game %d: %q refers to a game that isn't on the schedule", t.Games[i].No, s.Text))
				s.From = nil
			}
		}
	}
	return notes
}

// fixDayMonth fixes game dates typed day first into a sheet that reads
// month first ("09-11-24" for 9 November became 11 September): a date
// outside the tournament's dates that falls inside them with day and month
// swapped is swapped.
func fixDayMonth(t *Tournament) []string {
	start, end, ok := dateRange(t.Dates)
	if !ok {
		return nil
	}
	in := func(d time.Time) bool { return !d.Before(start.AddDate(0, 0, -1)) && !d.After(end.AddDate(0, 0, 1)) }
	fixed := 0
	for i := range t.Games {
		d, err := time.Parse("2006-01-02", t.Games[i].Date)
		if err != nil || in(d) || d.Day() > 12 {
			continue
		}
		sw := time.Date(d.Year(), time.Month(d.Day()), int(d.Month()), 0, 0, 0, 0, time.UTC)
		if in(sw) {
			t.Games[i].Date = sw.Format("2006-01-02")
			fixed++
		}
	}
	if fixed > 0 {
		return []string{fmt.Sprintf("%d game dates were typed day first; read as %s to match the tournament dates (%s)", fixed, "day-month", t.Dates)}
	}
	return nil
}

// dateRange reads the tournament dates: "2024/11/09 - 2024/11/10",
// "2026/6/12-14", "2026-10-15 to 10-18", or one date.
func dateRange(s string) (time.Time, time.Time, bool) {
	m := regexp.MustCompile(`(\d{4})[/-](\d{1,2})[/-](\d{1,2})(?:\s*(?:-|to|–)\s*(?:(?:(\d{4})[/-])?(\d{1,2})[/-])?(\d{1,2}))?`).FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, time.Time{}, false
	}
	n := func(x string, def int) int {
		if v, err := strconv.Atoi(x); err == nil {
			return v
		}
		return def
	}
	y, mo, d := n(m[1], 0), n(m[2], 0), n(m[3], 0)
	start := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	end := start
	if m[6] != "" {
		end = time.Date(n(m[4], y), time.Month(n(m[5], mo)), n(m[6], d), 0, 0, 0, 0, time.UTC)
	}
	if start.Month() != time.Month(mo) || end.Before(start) {
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

// dateText is a date cell as YYYY-MM-DD: an Excel date (the cells hold
// dates, whatever they show), or text that can only be read one way. Other
// text is kept as written.
func dateText(v string) string {
	if f, err := strconv.ParseFloat(v, 64); err == nil && f > 20000 && f < 80000 {
		if d, err := excelize.ExcelDateToTime(f, false); err == nil {
			return d.Format("2006-01-02")
		}
	}
	for _, layout := range []string{"2006/01/02", "2006-01-02", "2006/1/2", "2006-1-2"} {
		if d, err := time.Parse(layout, strings.TrimSpace(v)); err == nil {
			return d.Format("2006-01-02")
		}
	}
	return v
}

// timeText is a time cell as HH:MM: an Excel time (a fraction of a day) or
// text such as "10:00 am" or "17:30".
func timeText(v string) string {
	if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f < 1 {
		m := int(math.Round(f * 24 * 60))
		return fmt.Sprintf("%02d:%02d", m/60, m%60)
	}
	s := strings.ToUpper(strings.Join(strings.Fields(v), ""))
	for _, layout := range []string{"3:04PM", "15:04", "3PM"} {
		if d, err := time.Parse(layout, s); err == nil {
			return d.Format("15:04")
		}
	}
	return ""
}

// --- matching schedule names to teams ----------------------------------------------

// matcher finds the team a schedule name means. Schedules shorten names
// ("VRDL All-Stars", "Lomme", "Crime City A-Team"), so words are weighed
// by how rare they are among the teams (a league's name counts more than
// "All Stars"), and a league's initials count as a word.
type matcher struct {
	teams  []Team
	tokens []map[string]bool
	norm   []string
	weight map[string]float64
}

var allStars = regexp.MustCompile(`\ball\s+stars?\b`)

func tokens(s string) []string {
	s = strings.ToLower(strings.ReplaceAll(s, "×", "x"))
	s = regexp.MustCompile(`[^\p{L}\p{N}]+`).ReplaceAllString(s, " ")
	s = allStars.ReplaceAllString(s, "allstars")
	return strings.Fields(s)
}

func newMatcher(teams []Team) *matcher {
	m := &matcher{teams: teams, weight: map[string]float64{}}
	df := map[string]int{}
	for _, t := range teams {
		set := map[string]bool{}
		for _, w := range tokens(t.Name) {
			set[w] = true
		}
		// The league's initials: "Victorian Roller Derby League" is "vrdl".
		league := regexp.MustCompile(`\s+-\s+|:\s*`).Split(t.Name, 2)[0]
		if ws := tokens(league); len(ws) > 1 {
			ini := ""
			for _, w := range ws {
				ini += string([]rune(w)[0])
			}
			set[ini] = true
		}
		for w := range set {
			df[w]++
		}
		m.tokens = append(m.tokens, set)
		m.norm = append(m.norm, strings.Join(tokens(t.Name), " "))
	}
	for w, n := range df {
		m.weight[w] = math.Log(1 + float64(len(teams))/float64(n))
	}
	return m
}

// match is the team number the text means, and whether it's exact; 0 when
// no team fits clearly.
func (m *matcher) match(text string) (int, bool) {
	ws := tokens(text)
	if len(ws) == 0 {
		return 0, false
	}
	norm := strings.Join(ws, " ")
	for i, n := range m.norm {
		if n == norm {
			return m.teams[i].No, true
		}
	}
	type scored struct {
		no    int
		score float64
		cover float64
	}
	var all []scored
	for i, set := range m.tokens {
		var got, total float64
		for _, w := range ws {
			wt := m.weight[w]
			if wt == 0 {
				wt = math.Log(1 + float64(len(m.teams))) // a word no team has
			}
			total += wt
			if set[w] {
				got += wt
			}
		}
		// How much of the team's name the text covers breaks ties
		// ("Rose City Axles" against both Rose City teams).
		hit := 0
		for _, w := range ws {
			if set[w] {
				hit++
			}
		}
		all = append(all, scored{m.teams[i].No, got / total, float64(hit) / float64(len(set))})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].cover > all[j].cover
	})
	best := all[0]
	if best.score < 0.6 {
		return 0, false
	}
	if len(all) > 1 && all[1].score >= best.score-0.15 && all[1].cover >= best.cover {
		return 0, false // two teams fit as well
	}
	return best.no, false
}
