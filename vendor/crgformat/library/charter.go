package library

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/xuri/excelize/v2"
)

// Charter is where a team's roster came from when it was read from a WFTDA
// charter.
type Charter struct {
	ID        string `json:"id"`                  // e.g. 2026-ZURICH-5
	Effective string `json:"effective,omitempty"` // Roster Effective Date, as written
	Approved  string `json:"approved,omitempty"`  // Approval Date, as written
}

// ErrNotCharter is returned for a spreadsheet that isn't a charter roster.
var ErrNotCharter = errors.New("not a WFTDA charter roster")

var charterID = regexp.MustCompile(`^\d{4}-[A-Z0-9]+-\d+$`)

// ReadCharter reads a WFTDA charter roster (the "Charter Roster" sheet, as
// downloaded from Google Sheets as .xlsx) into a team: league, team name,
// uniform colours, and per skater the number, skater name, pronouns, name
// pronunciation and WUID. Legal names and the submitter's email are never
// read. Notes say what was skipped.
//
// Fields are found by their labels and skater columns by their headers, not
// by cell positions, so a moved column still reads.
func ReadCharter(data []byte) (*Team, []string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			continue
		}
		if t, notes, ok := readCharterRows(rows); ok {
			return t, notes, nil
		}
	}
	return nil, nil, ErrNotCharter
}

func readCharterRows(rows [][]string) (*Team, []string, bool) {
	var notes []string
	t := &Team{Skaters: []Skater{}, Charter: &Charter{}}
	header := -1
	for i, r := range rows {
		if isSkaterHeader(r) {
			header = i
			break
		}
		for j, c := range r {
			c = strings.TrimSpace(c)
			if charterID.MatchString(c) {
				t.Charter.ID = c
			}
			label := strings.ToLower(strings.TrimSuffix(c, ":"))
			if !strings.HasSuffix(c, ":") {
				continue
			}
			value := valueRight(r, j)
			switch label {
			case "league name":
				t.League = value
			case "team name":
				t.TeamName = value
			case "uniform colors", "uniform colours":
				t.UniformColors = splitColors(value)
			case "roster effective date":
				t.Charter.Effective = value
			case "approval date":
				t.Charter.Approved = value
			}
		}
	}
	if header < 0 || t.League == "" && t.TeamName == "" {
		return nil, nil, false
	}
	cols := map[string]int{}
	for j, c := range rows[header] {
		h := strings.ToLower(strings.TrimSpace(c))
		switch {
		case strings.Contains(h, "legal"):
			// Only compared with the pronunciation (see below), never kept.
			cols["legal"] = j
		case strings.Contains(h, "pronunciation"):
			cols["pronunciation"] = j
		case strings.Contains(h, "pronoun"):
			cols["pronouns"] = j
		case strings.Contains(h, "wuid"):
			cols["wuid"] = j
		case strings.Contains(h, "number"):
			cols["number"] = j
		case strings.Contains(h, "skater name"):
			cols["name"] = j
		}
	}
	if _, ok := cols["number"]; !ok {
		return nil, nil, false
	}
	cell := func(r []string, key string) string {
		j, ok := cols[key]
		if !ok || j >= len(r) {
			return ""
		}
		return strings.TrimSpace(r[j])
	}
	seen := map[string]bool{}
	dropped := 0
	for i, r := range rows[header+1:] {
		number, name := cell(r, "number"), cell(r, "name")
		if number == "" && name == "" {
			continue
		}
		if number == "" {
			notes = append(notes, fmt.Sprintf("row %d: %s has no number; left out", header+i+2, name))
			continue
		}
		// A number is letters and digits; a charter sometimes has "95." or "6 7".
		if clean := alnum(number); clean != number {
			notes = append(notes, fmt.Sprintf("row %d: number %q read as %s", header+i+2, number, clean))
			number = clean
			if number == "" {
				continue
			}
		}
		if seen[number] {
			notes = append(notes, fmt.Sprintf("row %d: a second #%s; left out", header+i+2, number))
			continue
		}
		seen[number] = true
		s := Skater{Number: number, Name: name, Pronouns: pronouns(cell(r, "pronouns")), WUID: cell(r, "wuid")}
		if p := cell(r, "pronunciation"); p != "" && p != "-" && !strings.EqualFold(p, name) {
			// Some skaters write their legal name here; that isn't imported.
			if sharesName(p, cell(r, "legal"), name) {
				dropped++
			} else {
				s.Pronunciation = p
			}
		}
		t.Skaters = append(t.Skaters, s)
	}
	if dropped > 0 {
		notes = append(notes, fmt.Sprintf("%d name pronunciation(s) left out: they looked like the skater's legal name", dropped))
	}
	t.Name = t.League
	if t.Name == "" {
		t.Name = t.TeamName
	}
	if t.League != "" && t.TeamName != "" && !strings.EqualFold(t.League, t.TeamName) {
		t.FullName = t.League + " - " + t.TeamName
	}
	if t.Charter.ID == "" {
		notes = append(notes, "no WFTDA charter id on it: it may not have been approved")
	}
	if *t.Charter == (Charter{}) {
		t.Charter = nil
		t.Source = "charter"
	} else {
		t.Source = strings.TrimSpace("charter " + t.Charter.ID)
	}
	return t, notes, true
}

// sharesName says whether the pronunciation has a word of the legal name
// that the skater name doesn't have.
func sharesName(pronunciation, legal, skaterName string) bool {
	words := func(s string) []string {
		return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	}
	inSkater := map[string]bool{}
	for _, w := range words(skaterName) {
		inSkater[w] = true
	}
	inLegal := map[string]bool{}
	for _, w := range words(legal) {
		if len([]rune(w)) >= 3 && !inSkater[w] {
			inLegal[w] = true
		}
	}
	for _, w := range words(pronunciation) {
		if inLegal[w] {
			return true
		}
	}
	return false
}

// isSkaterHeader is the header row of the skater list.
func isSkaterHeader(r []string) bool {
	number, name := false, false
	for _, c := range r {
		h := strings.ToLower(c)
		number = number || strings.Contains(h, "uniform number")
		name = name || strings.TrimSpace(h) == "skater name"
	}
	return number && name
}

// valueRight is the first non-empty cell right of a label, up to the next
// label.
func valueRight(r []string, j int) string {
	for k := j + 1; k < len(r); k++ {
		c := strings.TrimSpace(r[k])
		if strings.HasSuffix(c, ":") {
			return ""
		}
		if c != "" {
			return c
		}
	}
	return ""
}

// splitColors splits "Black / White", "Blue and White" or "red, black" into
// kits.
func splitColors(s string) []string {
	var out []string
	for _, p := range regexp.MustCompile(`(?i)\s*(?:/|,|&|\band\b|\bor\b)\s*`).Split(s, -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// pronouns tidies a pronouns cell for the roster display: the charter
// form's choices that aren't pronouns ("Prefer not to say", "Other", "Name
// only") mean none are shown, and "she/her" is written "She/Her".
func pronouns(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "-", "n/a", "na", "none", "", "prefer not to say", "other", "name only":
		return ""
	}
	return regexp.MustCompile(`\b[a-z]`).ReplaceAllStringFunc(s, strings.ToUpper)
}

// alnum keeps the letters and digits of s.
func alnum(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
