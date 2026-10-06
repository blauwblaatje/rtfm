package crews

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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

// Infopacks aren't standardised. What they share: a column of positions
// (abbreviated: CHR, IPR, JR, OPR, CHNSO, JT, ePLT, …; or written out),
// and next to it a column of names per crew. Above each column of names is
// what it is: a crew ("Crew Boston", "Crew A"), a game ("G1 London v
// Lomme", "Game 2", "GAME 12", or a number under a "Game" label, with the
// game's time and teams around it), or just "Name". Pronouns, league and
// certification may be columns of their own, or the pronouns in the name
// ("Joonas (he/him)"). Which crew does which game is in a schedule ("Crew
// A" in the row of "Game 1 Period 1…") or an assignment grid ("GAME 1"
// above "Crew 1"). Referees and NSOs may be one crew or two.
//
// Contact details are never read: only positions, names, pronouns, league
// and certification.

// abbreviations are positions as infopacks abbreviate them.
var abbreviations = map[string]string{
	"HR": "Head Referee", "CHR": "Head Referee", "IPR": "Inside Pack Referee", "JR": "Jammer Referee",
	"OPR": "Outside Pack Referee", "ALTREF": "Referee Alternate", "ALT REF": "Referee Alternate", "REFALT": "Referee Alternate",
	"REF ALT": "Referee Alternate", "RALT": "Referee Alternate", "ALTR": "Referee Alternate", "AR": "Referee Alternate",
	"HNSO": "Head Non-Skating Official", "CHNSO": "Head Non-Skating Official", "NSO CREW": "Head Non-Skating Official",
	"PW": "Penalty Wrangler", "IWB": "Inside Whiteboard Operator", "IWBO": "Inside Whiteboard Operator",
	"JT": "Jam Timer", "SK": "Scorekeeper", "SBO": "ScoreBoard Operator", "PBM": "Penalty Box Manager",
	"PBT": "Penalty Box Timer", "LT": "Lineup Tracker", "PT": "Penalty Tracker", "PLT": "Penalty Lineup Tracker",
	"EPLT": "Penalty Lineup Tracker", "EPT": "Penalty Tracker", "ELT": "Lineup Tracker",
	"ALTNSO": "Non-Skating Official Alternate", "ALT NSO": "Non-Skating Official Alternate",
	"NSOALT": "Non-Skating Official Alternate", "NSO ALT": "Non-Skating Official Alternate", "ANSO": "Non-Skating Official Alternate",
	"ALT": alternate, "ALTERNATE": alternate,
}

// alternate is an alternate whose kind (referee or NSO) the block decides.
const alternate = "alternate"

var heads = map[string]bool{"HR": true, "CHR": true, "HNSO": true, "CHNSO": true, "NSO CREW": true}

// Tournament positions: not a game's, so not in a crew.
var tournamentRoles = map[string]bool{"THR": true, "THNSO": true, "ATHNSO": true, "ATHR": true, "TH": true, "THO": true, "GTO": true}

// Positions that are no IGRF position: in a crew's column, but left out.
var otherRoles = map[string]bool{"DEV": true, "SHADOW": true, "MENTOR": true, "TRAINEE": true, "OBSERVER": true, "EVAL": true, "EVALUATOR": true}

// written are positions written out, by words in them (lower case).
var written = []struct {
	words []string
	role  string
	head  bool
}{
	{[]string{"head", "ref"}, "Head Referee", true},
	{[]string{"head", "nso"}, "Head Non-Skating Official", true},
	{[]string{"head", "non-skating"}, "Head Non-Skating Official", true},
	{[]string{"inside pack"}, "Inside Pack Referee", false},
	{[]string{"jammer ref"}, "Jammer Referee", false},
	{[]string{"outside pack"}, "Outside Pack Referee", false},
	{[]string{"ref", "alt"}, "Referee Alternate", false},
	{[]string{"nso", "alt"}, "Non-Skating Official Alternate", false},
	{[]string{"non-skating official alt"}, "Non-Skating Official Alternate", false},
	{[]string{"jam timer"}, "Jam Timer", false},
	{[]string{"penalty", "lineup"}, "Penalty Lineup Tracker", false},
	{[]string{"lineup tracker"}, "Lineup Tracker", false},
	{[]string{"penalty tracker"}, "Penalty Tracker", false},
	{[]string{"wrangler"}, "Penalty Wrangler", false},
	{[]string{"whiteboard"}, "Inside Whiteboard Operator", false},
	{[]string{"scorekeeper"}, "Scorekeeper", false},
	{[]string{"score keeper"}, "Scorekeeper", false},
	{[]string{"scoreboard"}, "ScoreBoard Operator", false},
	{[]string{"score board"}, "ScoreBoard Operator", false},
	{[]string{"box manager"}, "Penalty Box Manager", false},
	{[]string{"box timer"}, "Penalty Box Timer", false},
	{[]string{"alternate"}, alternate, false},
}

var trailingNumber = regexp.MustCompile(`\s+\d+$`)

// position is a position cell read.
type position struct {
	role  string // an IGRF position, alternate, or "" for skip/other
	head  bool
	skip  bool // a tournament position (THR, THNSO, GTO)
	other bool // not on the IGRF (DEV, shadow)
}

// role reads a position cell ("CHR", "CHR 2", "ePLT", "CHNSO/PW", "JR/OPR",
// "Jam Timer", "ePenalty/Lineup Tracker").
func role(cell string) (position, bool) {
	c := strings.TrimSpace(trailingNumber.ReplaceAllString(strings.TrimSpace(cell), ""))
	if c == "" || len(c) > 40 {
		return position{}, false
	}
	up := strings.ToUpper(c)
	for _, x := range Roles {
		if strings.EqualFold(c, x) {
			return position{role: x, head: x == "Head Referee" || x == "Head Non-Skating Official"}, true
		}
	}
	if p, ok := abbreviation(up); ok {
		return p, true
	}
	for _, part := range regexp.MustCompile(`\s*[/\\,+&]\s*`).Split(up, -1) {
		if p, ok := abbreviation(part); ok {
			return p, true
		}
	}
	// Written out: only something that looks like a position (a few words,
	// no digits).
	low := strings.ToLower(c)
	if strings.ContainsAny(low, "0123456789:@") || len(strings.Fields(low)) > 5 {
		return position{}, false
	}
	for _, w := range written {
		all := true
		for _, word := range w.words {
			all = all && strings.Contains(low, word)
		}
		if all {
			return position{role: w.role, head: w.head}, true
		}
	}
	return position{}, false
}

func abbreviation(s string) (position, bool) {
	s = strings.TrimSpace(s)
	if tournamentRoles[s] {
		return position{skip: true}, true
	}
	if otherRoles[s] {
		return position{other: true}, true
	}
	if x, ok := abbreviations[s]; ok {
		return position{role: x, head: heads[s]}, true
	}
	return position{}, false
}

var pronounWords = regexp.MustCompile(`(?i)^(she|he|they|them|her|him|hers|his|theirs|their|any|all|it|its|xe|xem|ze|zir|hir|ey|em|fae|faer|ask|name|none|no pronouns|use name|any pronouns)$`)

// pronouns reads a pronouns cell ("She/Her", "Any", "He/Him 2",
// "She/Her, They/Them"), or "".
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

var (
	gameLabel  = regexp.MustCompile(`(?i)^\s*(?:game|g)\s*#?\s*(\d+)\b`)
	gameNumber = regexp.MustCompile(`^\s*(\d+)(?:[.,]0+)?\s*(?:/.*|\(.*\))?$`)
	versus     = regexp.MustCompile(`(?i)\sv(s|\.)?\s`)
	weekday    = regexp.MustCompile(`(?i)^(monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b`)
	inlinePron = regexp.MustCompile(`\s*\(([^()]{2,30})\)\s*$`)
	contact    = regexp.MustCompile(`@|\+?\d[\d \-]{6,}\d`)
	clockTime  = regexp.MustCompile(`^\d{1,2}([:.]\d\d)+(\s*[ap]m)?$`)
)

var gameAnywhere = regexp.MustCompile(`(?i)(?:^|[\s:])(?:game|g)\s*#?\s*(\d+)\b`)

// GameNumber is the number of the game an infopack names ("Friday: G5
// Paris v Winner G1" is 5, "GAME 12 (Quart. Final)" 12, "Game2" 2), or 0.
func GameNumber(label string) int {
	m := gameAnywhere.FindStringSubmatch(label)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// isGame says whether a header cell names a game.
func isGame(s string) bool { return gameLabel.MatchString(s) || versus.MatchString(" "+s+" ") }

// isName is a cell that can be an official's name.
func isName(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.IndexFunc(s, unicode.IsLetter) < 0 || contact.MatchString(s) || clockTime.MatchString(s) {
		return false
	}
	if _, ok := role(s); ok {
		return false
	}
	low := strings.ToLower(s)
	switch {
	case strings.HasPrefix(s, "(") || strings.HasPrefix(s, "["):
		return false // a note: "(Angie playing)"
	case strings.HasPrefix(low, "from ") || strings.HasPrefix(low, "tbd") || strings.HasPrefix(low, "tba") || low == "n/a":
		return false // "From Crew B"
	case strings.Contains(s, ":") || strings.Contains(low, "mentor"):
		return false // "THNSO: Eagle Eye Mary", "ePLT Mentor- Line"
	case pronouns(s) != "" || isGame(s) || weekday.MatchString(s):
		return false
	}
	switch low {
	case "name", "names", "pronouns", "league", "level", "role", "position", "crew", "game", "official", "officials", "sanc", "sanctioned":
		return false
	}
	return true
}

// cleanName takes an official's name from a cell: "Joonas (he/him)" ->
// "Joonas", "he/him"; "REFlex 2" -> "REFlex"; of "Zero/Becks" the first.
func cleanName(s string) (name, pron string, split bool) {
	name = strings.TrimSpace(s)
	if m := inlinePron.FindStringSubmatch(name); m != nil && pronouns(m[1]) != "" {
		pron = strings.TrimSpace(m[1])
		name = strings.TrimSpace(name[:len(name)-len(m[0])])
	}
	name = strings.TrimSpace(trailingNumber.ReplaceAllString(name, ""))
	for _, sep := range []string{"/", "\\"} {
		if i := strings.Index(name, sep); i > 0 && i < len(name)-1 {
			name, split = strings.TrimSpace(name[:i]), true
		}
	}
	return name, pron, split
}

// ReadInfopack reads officiating crews from a tournament's infopack
// (downloaded as .xlsx).
func ReadInfopack(data []byte) ([]*Crew, []string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	r := &reader{byKey: map[string]*Crew{}}
	var sheets []grid
	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			continue
		}
		g := grid{name: sheet, rows: rows}
		sheets = append(sheets, g)
		r.readSheet(g)
	}
	r.assign(sheets)
	crews := r.finish()
	if len(crews) == 0 {
		return nil, nil, ErrNoCrews
	}
	var notes []string
	if r.skipped > 0 {
		notes = append(notes, fmt.Sprintf("%d tournament officials (THR, THNSO, GTO) left out: they aren't in a game's crew.", r.skipped))
	}
	if len(r.split) > 0 {
		notes = append(notes, fmt.Sprintf("Two names in one place, the first taken: %s. Check those in the crew.", strings.Join(r.split, "; ")))
	}
	return crews, notes, nil
}

type grid struct {
	name string
	rows [][]string
}

func (g grid) at(r, c int) string {
	if r < 0 || r >= len(g.rows) || c < 0 || c >= len(g.rows[r]) {
		return ""
	}
	return strings.TrimSpace(g.rows[r][c])
}

func (g grid) width() int {
	w := 0
	for _, r := range g.rows {
		w = max(w, len(r))
	}
	return w
}

type reader struct {
	crews   []*Crew // in the order found
	byKey   map[string]*Crew
	perGame map[*Crew]bool
	skipped int
	split   []string
}

// column kinds next to a column of positions.
const (
	kindNames = iota + 1
	kindPronouns
	kindLeague
	kindCert
	kindIgnore
)

// run is a block of positions down a column.
type run struct {
	col, start, end int
	headers         map[int]string // name column -> its crew's key
	label           map[int]string // name column -> its crew's name
	games           map[int]string // name column -> its game
}

func (r *reader) readSheet(g grid) {
	width := g.width()
	kinds := map[int]int{} // learned from headers in this sheet
	var prev []*run
	for c := 0; c < width; c++ {
		for row := 0; row < len(g.rows); row++ {
			if _, ok := role(g.at(row, c)); !ok {
				continue
			}
			// A run: positions, with at most one empty row between them.
			start, end := row, row
			for k := row + 1; k < len(g.rows); k++ {
				v := g.at(k, c)
				if _, ok := role(v); ok {
					end = k
					continue
				}
				if v == "" && k-end < 2 {
					continue
				}
				break
			}
			row = end
			if end == start {
				continue // one position alone is a mention, not a crew
			}
			ru := &run{col: c, start: start, end: end}
			r.readRun(g, ru, kinds, prev)
			prev = append(prev, ru)
		}
	}
}

// readRun reads the columns next to a run of positions.
func (r *reader) readRun(g grid, ru *run, kinds map[int]int, prev []*run) {
	c := ru.col
	// The columns of this run: up to the next column of positions, or four
	// empty columns (names sit under headings merged over several columns).
	type col struct{ idx, kind int }
	var cols []col
	empty := 0
	for cc := c + 1; cc < g.width() && len(cols) < 40; cc++ {
		names, prons, positions, numbers, filled := 0, 0, 0, 0, 0
		for row := ru.start; row <= ru.end; row++ {
			v := g.at(row, cc)
			if v == "" {
				continue
			}
			filled++
			if _, ok := role(v); ok {
				positions++
			}
			switch {
			case pronouns(v) != "":
				prons++
			case isName(v):
				names++
			case gameNumber.MatchString(v):
				numbers++
			}
		}
		if positions >= 2 {
			break // the next crew's positions
		}
		if filled == 0 {
			if empty++; empty > 4 {
				break
			}
			continue
		}
		empty = 0
		kind := headerKind(g, ru, cc)
		if kind == 0 {
			kind = kinds[cc]
		} else {
			kinds[cc] = kind
		}
		if kind == 0 {
			switch {
			case prons > 0 && prons >= names:
				kind = kindPronouns
			case names > 0 && names >= numbers:
				kind = kindNames
			default:
				kind = kindIgnore
			}
		}
		cols = append(cols, col{cc, kind})
	}
	// Each column of names is a crew's; the pronouns, league and
	// certification columns after it are its.
	ru.headers, ru.label, ru.games = map[int]string{}, map[int]string{}, map[int]string{}
	for i, cl := range cols {
		if cl.kind != kindNames {
			continue
		}
		extra := map[int]int{}
		for _, nx := range cols[i+1:] {
			if nx.kind == kindNames {
				break
			}
			if extra[nx.kind] == 0 {
				extra[nx.kind] = nx.idx
			}
		}
		names := 0
		for _, x := range cols {
			if x.kind == kindNames {
				names++
			}
		}
		key, label, game := r.header(g, ru, cl.idx, names == 1, prev)
		ru.headers[cl.idx], ru.label[cl.idx], ru.games[cl.idx] = key, label, game
		cr := r.byKey[key]
		if cr == nil {
			cr = &Crew{Name: label, Officials: []Official{}}
			if game != "" {
				cr.Games = []string{game}
			}
			r.byKey[key] = cr
			r.crews = append(r.crews, cr)
			if game != "" {
				if r.perGame == nil {
					r.perGame = map[*Crew]bool{}
				}
				r.perGame[cr] = true
			}
		}
		refs, nsos := 0, 0
		for row := ru.start; row <= ru.end; row++ {
			if p, ok := role(g.at(row, c)); ok && p.role != "" && p.role != alternate {
				if strings.Contains(p.role, "Referee") {
					refs++
				} else {
					nsos++
				}
			}
		}
		for row := ru.start; row <= ru.end; row++ {
			p, ok := role(g.at(row, c))
			cell := g.at(row, cl.idx)
			if !ok || !isName(cell) {
				continue
			}
			if p.skip {
				r.skipped++
				continue
			}
			if p.other || p.role == "" {
				continue
			}
			if p.role == alternate {
				p.role = "Non-Skating Official Alternate"
				if refs > nsos {
					p.role = "Referee Alternate"
				}
			}
			name, pron, split := cleanName(cell)
			if split {
				r.split = append(r.split, fmt.Sprintf("%q (%s)", cell, label))
			}
			o := Official{Name: name, Role: p.role, Head: p.head, Pronouns: pron}
			if i := extra[kindPronouns]; i > 0 && o.Pronouns == "" {
				o.Pronouns = pronouns(g.at(row, i))
			}
			if i := extra[kindLeague]; i > 0 {
				if v := g.at(row, i); isName(v) {
					o.League = v
				}
			}
			if i := extra[kindCert]; i > 0 {
				o.Cert = certification(g.at(row, i), o.Role)
			}
			if !has(cr, o) {
				cr.Officials = append(cr.Officials, o)
			}
		}
	}
}

func has(c *Crew, o Official) bool {
	for _, x := range c.Officials {
		if strings.EqualFold(x.Name, o.Name) && x.Role == o.Role {
			return true
		}
	}
	return false
}

// headerKind is what a column's header says it holds (up to three rows
// above the run), or 0.
func headerKind(g grid, ru *run, cc int) int {
	for row := ru.start - 1; row >= max(0, ru.start-3); row-- {
		if h := g.at(row, cc); h != "" {
			if k := kindWord(h); k != 0 {
				return k
			}
		}
	}
	return 0
}

// kindWord is what a column title says the column holds, or 0. A title
// is short ("League Affiliation"), not a game ("Victorian Roller Derby
// League - All-Stars v WG5").
func kindWord(h string) int {
	if isGame(h) || len(strings.Fields(h)) > 3 {
		return 0
	}
	h = strings.ToLower(strings.TrimSpace(h))
	{
		switch {
		case h == "":
			return 0
		case strings.Contains(h, "pronoun"):
			return kindPronouns
		case strings.Contains(h, "league") || strings.Contains(h, "affil"):
			return kindLeague
		case strings.Contains(h, "level") || strings.Contains(h, "cert"):
			return kindCert
		case strings.Contains(h, "mail") || strings.Contains(h, "phone") || strings.Contains(h, "contact") || strings.Contains(h, "legal") ||
			strings.Contains(h, "note") || strings.Contains(h, "comment") || strings.Contains(h, "first name") ||
			strings.Contains(h, "last name") || strings.Contains(h, "surname") || strings.Contains(h, "real name"):
			return kindIgnore
		case h == "name" || h == "names" || strings.Contains(h, "derby name") || strings.Contains(h, "skate name"):
			return kindNames
		}
	}
	return 0
}

// header works out whose a column of names is: a game's, a crew's, or (for
// one column of names) the crew's in the title above the positions. A run
// right under another without headers of its own (the NSOs under the
// referees) belongs to the same crews.
func (r *reader) header(g grid, ru *run, cc int, single bool, prev []*run) (key, label, game string) {
	c := ru.col
	// The header block above: rows up to the previous run.
	top := max(0, ru.start-8)
	for _, p := range prev {
		if p.end < ru.start && p.end >= top && overlaps(p, ru) {
			top = p.end + 1
		}
	}
	var texts []string
	day := ""
	for row := ru.start - 1; row >= top; row-- {
		v, lab := g.at(row, cc), g.at(row, c)
		if weekday.MatchString(lab) && day == "" {
			day = lab
		}
		if v == "" || strings.HasPrefix(v, "(") || strings.HasPrefix(v, "[") {
			continue // nothing, or a note: "(Angie playing)"
		}
		if weekday.MatchString(v) && day == "" {
			day = v
			continue
		}
		switch {
		case game == "" && gameLabel.MatchString(v), game == "" && versus.MatchString(" "+v+" "):
			game = v
		case game == "" && gameNumber.MatchString(v) && strings.Contains(strings.ToLower(lab), "game"):
			game = "Game " + gameNumber.FindStringSubmatch(v)[1]
		case pronouns(v) != "" || clockTime.MatchString(v) || kindWord(v) != 0:
		case row == ru.start-1 || strings.Contains(strings.ToLower(v), "crew"):
			// A crew's name: right above its names, or saying it's a crew.
			texts = append(texts, v)
		}
	}
	if day != "" {
		if rs := []rune(day); len(rs) > 0 {
			day = strings.ToUpper(string(rs[:1])) + strings.ToLower(string(rs[1:]))
		}
	}
	switch {
	case game != "":
		if day != "" && !weekday.MatchString(game) {
			game = day + ": " + game
		}
		return g.name + "\x00game\x00" + strings.ToLower(game), game, game
	case len(texts) > 0 && !(single && looksTeamless(texts[0], g.at(ru.start-1, c))):
		crew := texts[0]
		for _, t := range texts {
			if strings.Contains(strings.ToLower(t), "crew") {
				crew = t
				break
			}
		}
		return g.name + "\x00crew\x00" + strings.ToLower(crew), crew, ""
	}
	// No header: the crews of the run just above, if it had the column and
	// only empty rows are between them.
	for i := len(prev) - 1; i >= 0; i-- {
		p := prev[i]
		if p.col != c || p.end >= ru.start || ru.start-p.end > 4 {
			continue
		}
		between := false
		for row := p.end + 1; row < ru.start; row++ {
			for _, v := range []string{g.at(row, c), g.at(row, cc)} {
				between = between || v != "" && !strings.HasPrefix(v, "(") && !strings.HasPrefix(v, "[")
			}
		}
		if k, ok := p.headers[cc]; ok && !between {
			return k, p.label[cc], p.games[cc]
		}
	}
	// A crew of its own, named by its title (above the positions) or later
	// after its head.
	title := g.at(ru.start-1, c)
	if _, ok := role(title); ok || strings.EqualFold(title, "role") || strings.EqualFold(title, "position") {
		title = ""
	}
	return fmt.Sprintf("%s\x00at\x00%d,%d,%d", g.name, c, ru.start, cc), title, ""
}

// looksTeamless: a header over one column of names that is just the
// column's title ("Name"), or the crew's title in the positions' column.
func looksTeamless(text, title string) bool {
	low := strings.ToLower(text)
	return low == "name" || low == "names" || (title != "" && strings.EqualFold(text, title))
}

func overlaps(a, b *run) bool { return a.col == b.col }

// certification reads a certification cell: a level number is the level
// that fits the position ("2" for a referee: "Skating Level 2").
func certification(v, role string) string {
	v = strings.TrimSpace(v)
	m := gameNumber.FindStringSubmatch(v)
	if m == nil {
		if strings.EqualFold(v, "r") || strings.EqualFold(v, "rec") {
			m = []string{v, "R"}
		} else {
			return v
		}
	}
	kind := "Non-Skating"
	if strings.Contains(role, "Referee") {
		kind = "Skating"
	}
	if m[1] == "R" {
		return kind + " Recognized"
	}
	if n, _ := strconv.Atoi(m[1]); n >= 1 && n <= 3 {
		return fmt.Sprintf("%s Level %d", kind, n)
	}
	return v
}

// assign finds which named crew does which game: a cell with the crew's
// name, and in its row (to the left) or up its column a game.
func (r *reader) assign(sheets []grid) {
	named := map[string]*Crew{}
	for _, c := range r.crews {
		if !r.perGame[c] && c.Name != "" {
			named[strings.ToLower(strings.TrimSpace(c.Name))] = c
		}
	}
	if len(named) == 0 {
		return
	}
	for _, g := range sheets {
		for row := range g.rows {
			for col := range g.rows[row] {
				c := named[strings.ToLower(g.at(row, col))]
				if c == nil {
					continue
				}
				game := ""
				for k := col - 1; k >= 0 && game == ""; k-- {
					if v := g.at(row, k); gameLabel.MatchString(v) {
						game = gameLabel.FindString(v)
					}
				}
				for k := row - 1; k >= max(0, row-6) && game == ""; k-- {
					if v := g.at(k, col); gameLabel.MatchString(v) {
						game = strings.TrimSpace(v)
					}
				}
				if game != "" && !contains(c.Games, game) {
					c.Games = append(c.Games, game)
				}
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// finish names crews, drops what isn't a crew, gives a game's crew that is
// one of the named crews to that crew, and merges duplicates.
func (r *reader) finish() []*Crew {
	// Names in capitals are written properly where they're written so
	// elsewhere ("ANGIE" -> "Angie").
	proper := map[string]string{}
	for _, c := range r.crews {
		for _, o := range c.Officials {
			if o.Name != strings.ToUpper(o.Name) {
				proper[strings.ToLower(o.Name)] = o.Name
			}
		}
	}
	var kept []*Crew
	for _, c := range r.crews {
		for i := range c.Officials {
			if p := proper[strings.ToLower(c.Officials[i].Name)]; p != "" {
				c.Officials[i].Name = p
			}
		}
		if len(c.Officials) < 2 {
			continue
		}
		if c.Name == "" {
			c.Name = "Crew " + headName(c)
		}
		kept = append(kept, c)
	}
	var out []*Crew
	for _, c := range kept {
		merged := false
		for _, o := range out {
			if Same(c, o) && (r.perGame[c] || strings.EqualFold(c.Name, o.Name) || r.perGame[o]) {
				for _, g := range c.Games {
					if !contains(o.Games, g) {
						o.Games = append(o.Games, g)
					}
				}
				if r.perGame[o] && !r.perGame[c] {
					o.Name = c.Name // the named crew's name
					delete(r.perGame, o)
				}
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, c)
		}
	}
	return out
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
