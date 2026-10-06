package crews

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/xuri/excelize/v2"
)

// ErrNoCrews is returned for a spreadsheet without crews in it.
var ErrNoCrews = errors.New("no officiating crews found in this spreadsheet")

// Roles are the IGRF's positions (the Java scoreboard's list).
var Roles = []string{
	"Head Non-Skating Official", "Penalty Lineup Tracker", "Penalty Tracker", "Penalty Wrangler", "Inside Whiteboard Operator",
	"Jam Timer", "Scorekeeper", "ScoreBoard Operator", "Penalty Box Manager", "Penalty Box Timer", "Lineup Tracker",
	"Non-Skating Official Alternate", "Head Referee", "Inside Pack Referee", "Jammer Referee", "Outside Pack Referee",
	"Referee Alternate",
}

// abbreviations are the positions as infopacks write them. Infopacks aren't
// standardised, their abbreviations are.
var abbreviations = map[string]string{
	"HR": "Head Referee", "CHR": "Head Referee", "IPR": "Inside Pack Referee", "JR": "Jammer Referee",
	"OPR": "Outside Pack Referee", "ALTREF": "Referee Alternate", "ALT REF": "Referee Alternate", "REFALT": "Referee Alternate",
	"RALT": "Referee Alternate", "HNSO": "Head Non-Skating Official", "CHNSO": "Head Non-Skating Official",
	"PW": "Penalty Wrangler", "IWB": "Inside Whiteboard Operator", "IWBO": "Inside Whiteboard Operator",
	"JT": "Jam Timer", "SK": "Scorekeeper", "SBO": "ScoreBoard Operator", "PBM": "Penalty Box Manager",
	"PBT": "Penalty Box Timer", "LT": "Lineup Tracker", "PT": "Penalty Tracker", "PLT": "Penalty Lineup Tracker",
	"EPLT": "Penalty Lineup Tracker", "ALTNSO": "Non-Skating Official Alternate", "ALT NSO": "Non-Skating Official Alternate",
	"NSOALT": "Non-Skating Official Alternate",
}

// Tournament positions: not a game's, so not in a crew.
var tournamentRoles = map[string]bool{"THR": true, "THNSO": true, "TH": true, "THO": true}

var trailingNumber = regexp.MustCompile(`\s+\d+$`)

// role reads a position cell ("CHR", "CHR 2", "ePLT", "CHNSO/PW", "Jam
// Timer"): the IGRF role, and whether it's the crew's head. skip is a
// tournament position.
func role(cell string) (r string, head, skip, ok bool) {
	c := strings.ToUpper(trailingNumber.ReplaceAllString(strings.TrimSpace(cell), ""))
	if c == "" {
		return "", false, false, false
	}
	for _, part := range strings.Split(c, "/") {
		part = strings.TrimSpace(part)
		if tournamentRoles[part] {
			return "", false, true, true
		}
		if x, found := abbreviations[part]; found {
			return x, part == "HR" || part == "CHR" || part == "HNSO" || part == "CHNSO", false, true
		}
	}
	for _, x := range Roles {
		if strings.EqualFold(c, x) {
			return x, false, false, true
		}
	}
	return "", false, false, false
}

var pronounWords = regexp.MustCompile(`(?i)^(she|he|they|them|her|him|hers|his|theirs|any|all|it|its|xe|xem|ze|zir|ey|em|fae|ask|name|none|no pronouns|use name|any pronouns)$`)

// pronouns reads a pronouns cell ("She/Her", "Any", "He/Him 2"), or "".
func pronouns(cell string) string {
	c := strings.TrimSpace(trailingNumber.ReplaceAllString(strings.TrimSpace(cell), ""))
	if c == "" || len(c) > 30 {
		return ""
	}
	for _, w := range regexp.MustCompile(`\s*[/,&]\s*|\s+`).Split(c, -1) {
		if w != "" && !pronounWords.MatchString(w) {
			return ""
		}
	}
	return c
}

// ReadInfopack reads officiating crews from a tournament's infopack
// (downloaded as .xlsx). Positions are found by their abbreviations
// (CHR, IPR, JR, OPR, CHNSO, JT, ePLT, SBO, SK, PBM, PBT, …) wherever they
// are, in two layouts:
//
//   - crews side by side: in every run of positions down a column, the
//     name is next to the position and the pronouns after that;
//   - crew assignments: a column of positions with a game in the row above
//     each column of names.
//
// An assignment crew that is one of the crews already found adds its game
// to that crew. Contact details are never read: only positions, names and
// pronouns.
func ReadInfopack(data []byte) ([]*Crew, []string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	var crews, assigned []*Crew
	skipped := 0
	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			continue
		}
		c, a, n := readSheet(rows)
		crews, assigned, skipped = append(crews, c...), append(assigned, a...), skipped+n
	}
	var notes []string
	for _, a := range assigned {
		found := false
		for _, c := range crews {
			if Same(a, c) {
				c.Games = append(c.Games, a.Games...)
				found = true
				break
			}
		}
		if !found {
			crews = append(crews, a)
		}
	}
	if len(crews) == 0 {
		return nil, nil, ErrNoCrews
	}
	if skipped > 0 {
		notes = append(notes, fmt.Sprintf("%d tournament head officials (THR, THNSO) left out: they aren't in a game's crew.", skipped))
	}
	return crews, notes, nil
}

func at(rows [][]string, r, c int) string {
	if r < 0 || r >= len(rows) || c < 0 || c >= len(rows[r]) {
		return ""
	}
	return strings.TrimSpace(rows[r][c])
}

// readSheet finds the runs of positions down each column.
func readSheet(rows [][]string) (crews, assigned []*Crew, skipped int) {
	width := 0
	for _, r := range rows {
		width = max(width, len(r))
	}
	for c := 0; c < width; c++ {
		for r := 0; r < len(rows); r++ {
			if _, _, _, ok := role(at(rows, r, c)); !ok {
				continue
			}
			// A run: positions, with at most one empty row between them.
			start, end := r, r
			for k := r + 1; k < len(rows); k++ {
				v := at(rows, k, c)
				if _, _, _, ok := role(v); ok {
					end = k
					continue
				}
				if v == "" && k-end < 2 {
					continue
				}
				break
			}
			r = end
			if end == start {
				continue // one position alone is a mention, not a crew
			}
			if title := at(rows, start-1, c+1); title != "" && pronouns(title) == "" && !strings.EqualFold(title, "name") {
				a, n := readAssignments(rows, start, end, c)
				assigned, skipped = append(assigned, a...), skipped+n
			} else if cr, n := readCrew(rows, start, end, c); cr != nil {
				crews, skipped = append(crews, cr), skipped+n
			}
		}
	}
	return crews, assigned, skipped
}

// readCrew reads a crew side by side: position, name, pronouns.
func readCrew(rows [][]string, start, end, c int) (*Crew, int) {
	cr := &Crew{Officials: []Official{}}
	skipped := 0
	for r := start; r <= end; r++ {
		ro, head, skip, ok := role(at(rows, r, c))
		name := at(rows, r, c+1)
		if !ok || !isName(name) {
			continue
		}
		if skip {
			skipped++
			continue
		}
		cr.Officials = append(cr.Officials, Official{Name: name, Role: ro, Head: head, Pronouns: pronouns(at(rows, r, c+2))})
	}
	if len(cr.Officials) < 2 {
		return nil, 0
	}
	if t := at(rows, start-1, c); t != "" {
		if _, _, _, ok := role(t); !ok {
			cr.Name = t
		}
	}
	if cr.Name == "" {
		cr.Name = "Crew " + headName(cr)
	}
	return cr, skipped
}

// readAssignments reads a column of positions with a game above each
// column of names.
func readAssignments(rows [][]string, start, end, c int) ([]*Crew, int) {
	var out []*Crew
	day := at(rows, start-1, c)
	if r := []rune(day); len(r) > 0 {
		day = strings.ToUpper(string(r[:1])) + strings.ToLower(string(r[1:]))
	}
	for gc := c + 1; gc < len(rows[start-1]); gc++ {
		game := at(rows, start-1, gc)
		if !versus.MatchString(game) {
			continue // not a game: a schedule of something else
		}
		cr := &Crew{Officials: []Official{}}
		for r := start; r <= end; r++ {
			ro, head, skip, ok := role(at(rows, r, c))
			name := at(rows, r, gc)
			if !ok || skip || !isName(name) {
				continue
			}
			cr.Officials = append(cr.Officials, Official{Name: name, Role: ro, Head: head})
		}
		if len(cr.Officials) < 2 {
			continue
		}
		if day != "" {
			game = day + ": " + game
		}
		cr.Games = []string{game}
		cr.Name = game
		out = append(out, cr)
	}
	return out, 0
}

var versus = regexp.MustCompile(`(?i)\sv(s|\.)?\s`)

// isName is a cell that can be an official's name: not empty, not a
// position, with a letter in it.
func isName(s string) bool {
	if _, _, _, ok := role(s); ok || s == "" {
		return false
	}
	return strings.IndexFunc(s, unicode.IsLetter) >= 0
}

// headName names a crew after its head referee, else its head NSO.
func headName(c *Crew) string {
	for _, want := range []string{"Head Referee", "Head Non-Skating Official"} {
		for _, o := range c.Officials {
			if o.Role == want {
				return o.Name
			}
		}
	}
	return c.Officials[0].Name
}
