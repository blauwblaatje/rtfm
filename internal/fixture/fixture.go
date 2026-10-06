// Package fixture is RTFM's work: a tournament from its WFTDA sanctioning
// application (the schedule and the teams' charters) and, optionally, the
// officials' infopack (the crews), and from those each game as it is before
// it's played: a statsbook with the IGRF filled in and a game file for the
// Java CRG scoreboard (v2025.10).
//
// The reading is the CRG rewrite's (crgformat): tournament.Read for the
// application, library.ReadCharter for charters, crews.ReadInfopack for the
// infopack, prepare for the game, statsbook.Export and javaws.GameKeys for
// the two files.
package fixture

import (
	"archive/zip"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"crgformat"
	"crgformat/crews"
	"crgformat/javaws"
	"crgformat/library"
	"crgformat/prepare"
	"crgformat/replay"
	"crgformat/rulesets"
	"crgformat/statsbook"
	"crgformat/tournament"
)

// JavaVersion is the Java scoreboard version the game files are for.
const JavaVersion = javaws.Version

// Fetch downloads a Google Sheet as xlsx (FetchSheet, or a test's).
type Fetch func(url string) ([]byte, error)

var sheetURL = regexp.MustCompile(`^https://docs\.google\.com/spreadsheets/(?:u/\d+/)?d/([A-Za-z0-9_-]{20,})`)

// SheetID is the id of a Google Sheets link, or "".
func SheetID(url string) string {
	if m := sheetURL.FindStringSubmatch(strings.TrimSpace(url)); m != nil {
		return m[1]
	}
	return ""
}

// FetchSheet downloads a public Google Sheet as xlsx.
func FetchSheet(url string) ([]byte, error) {
	id := SheetID(url)
	if id == "" {
		return nil, errors.New("that isn't a Google Sheets link (https://docs.google.com/spreadsheets/d/…)")
	}
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Get("https://docs.google.com/spreadsheets/d/" + id + "/export?format=xlsx")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading the sheet: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(data, []byte("PK")) {
		return nil, errors.New("the sheet isn't shared publicly (Google asked to sign in)")
	}
	return data, nil
}

// Event is a loaded tournament.
type Event struct {
	ID      string                 `json:"id"`
	Loaded  time.Time              `json:"loaded"`
	T       *tournament.Tournament `json:"tournament"`
	Teams   map[int]*library.Team  `json:"teams"`   // by the application's team number
	Missing map[int]string         `json:"missing"` // team number -> why there's no charter
	Crews   []*crews.Crew          `json:"crews"`
	Notes   []string               `json:"notes"`
	Ruleset string                 `json:"ruleset"` // a preset's name

	mu sync.RWMutex // Crews, which can be edited while games are made
}

// Options are how Load runs: the tournament's id ("" makes one) and where
// it logs what it does.
type Options struct {
	ID   string
	Logf func(format string, args ...any)
}

// NewID is a new tournament id.
func NewID() string { return prepare.NewID("") }

// Load reads a sanctioning application and its teams' charters (fetched,
// several at a time), and an infopack if there is one.
func Load(application, infopack []byte, fetch Fetch, o Options) (*Event, error) {
	logf := o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	start := time.Now()
	t, notes, err := tournament.Read(application)
	if err != nil {
		logf("sanctioning application: can't read it: %v", err)
		return nil, fmt.Errorf("reading the sanctioning application: %w", err)
	}
	logf("sanctioning application: %q, %d teams, %d games, %d notes", t.Name, len(t.Teams), len(t.Games), len(notes))
	for _, n := range notes {
		logf("  note: %s", n)
	}
	ev := &Event{ID: cmp.Or(o.ID, NewID()), Loaded: time.Now().UTC(), T: t, Teams: map[int]*library.Team{},
		Missing: map[int]string{}, Notes: append([]string{}, notes...), Ruleset: rulesets.Presets[0].Name}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, tm := range t.Teams {
		if tm.CharterURL == "" {
			ev.Missing[tm.No] = "no charter link on the application"
			logf("team %d %q: no charter link on the application", tm.No, tm.Name)
			continue
		}
		wg.Add(1)
		go func(tm tournament.Team) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			began := time.Now()
			data, err := fetch(tm.CharterURL)
			var lt *library.Team
			var cn []string
			if err == nil {
				lt, cn, err = library.ReadCharter(data)
			}
			mu.Lock()
			defer mu.Unlock()
			took := time.Since(began).Round(time.Millisecond)
			if err != nil {
				ev.Missing[tm.No] = err.Error()
				logf("team %d %q: charter %s: ERROR after %s: %v", tm.No, tm.Name, tm.CharterURL, took, err)
				return
			}
			logf("team %d %q: charter read in %s: %s - %s, %d skaters, colours %v", tm.No, tm.Name, took, lt.League, lt.TeamName,
				len(lt.Skaters), lt.UniformColors)
			lt.ID = fmt.Sprintf("team-%d", tm.No)
			ev.Teams[tm.No] = lt
			for _, n := range cn {
				ev.Notes = append(ev.Notes, fmt.Sprintf("%s: %s", tm.Name, n))
			}
		}(tm)
	}
	wg.Wait()
	if len(infopack) > 0 {
		cs, cn, err := crews.ReadInfopack(infopack)
		if err != nil {
			logf("infopack: can't read it: %v", err)
			return nil, fmt.Errorf("reading the infopack: %w", err)
		}
		for _, c := range cs {
			logf("infopack: crew %q, %d officials, games %v", c.Name, len(c.Officials), c.Games)
		}
		for i, c := range cs {
			c.ID = fmt.Sprintf("crew-%d", i+1)
		}
		ev.Crews = cs
		ev.Notes = append(ev.Notes, cn...)
	}
	slices.Sort(ev.Notes)
	logf("loaded in %s: %d of %d charters, %d crews", time.Since(start).Round(time.Millisecond), len(ev.Teams), len(t.Teams), len(ev.Crews))
	return ev, nil
}

// Choice is how a game is made: its teams (the application's team
// numbers), their uniform colours, and its crew.
type Choice struct {
	Teams  [2]int    `json:"teams"`
	Colors [2]string `json:"colors"`
	Crew   string    `json:"crew"` // a crew's ID, or "" for none
}

// GameView is a game on the schedule with what's known of it.
type GameView struct {
	tournament.Game
	Sides   [2]string `json:"sides"`   // the team's name, or "Winner Game 1"
	Fixed   [2]bool   `json:"fixed"`   // the team is on the schedule
	Default Choice    `json:"default"` // fixed teams, schedule or charter colours, the infopack's crew
}

// Games are the games on the schedule.
func (ev *Event) Games() []GameView {
	var out []GameView
	for _, g := range ev.T.Games {
		v := GameView{Game: g}
		for i, slot := range []tournament.Slot{g.Home, g.Away} {
			v.Sides[i] = slot.Text
			if slot.Team > 0 && slot.From == nil {
				v.Fixed[i] = true
				v.Default.Teams[i] = slot.Team
				if tm := ev.T.TeamByNo(slot.Team); tm != nil {
					v.Sides[i] = tm.Name
				}
			}
			v.Default.Colors[i] = ev.color(v.Default.Teams[i], slot.Color, i)
		}
		if c := ev.crewFor(g, v.Default.Teams); c != nil {
			v.Default.Crew = c.ID
		}
		out = append(out, v)
	}
	return out
}

// color is a team's uniform colour for a game: the schedule's (its
// "Dark/Light" column, when filled in), else the charter's first (home) or
// last (away).
func (ev *Event) color(team int, scheduled string, side int) string {
	if scheduled = strings.TrimSpace(scheduled); scheduled != "" {
		return scheduled
	}
	lt := ev.Teams[team]
	if lt == nil || len(lt.UniformColors) == 0 {
		return ""
	}
	if side == 1 {
		return lt.UniformColors[len(lt.UniformColors)-1]
	}
	return lt.UniformColors[0]
}

// CrewList is a copy of the crews, to show or change (SetCrews).
func (ev *Event) CrewList() []*crews.Crew {
	ev.mu.RLock()
	defer ev.mu.RUnlock()
	out := make([]*crews.Crew, len(ev.Crews))
	for i, c := range ev.Crews {
		cp := *c
		cp.Officials = append([]crews.Official{}, c.Officials...)
		cp.Games = append([]string{}, c.Games...)
		out[i] = &cp
	}
	return out
}

// SetCrews replaces the crews (edited, or filled in from a roster). A crew
// without an ID gets one.
func (ev *Event) SetCrews(cs []*crews.Crew) {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	used := map[string]bool{}
	for _, c := range cs {
		used[c.ID] = true
	}
	n := 0
	for _, c := range cs {
		for c.ID == "" || c.ID == "-" {
			n++
			if id := fmt.Sprintf("crew-%d", n); !used[id] {
				c.ID, used[id] = id, true
			}
		}
	}
	ev.Crews = cs
}

// MarshalJSON keeps the crews still while they're written.
func (ev *Event) MarshalJSON() ([]byte, error) {
	ev.mu.RLock()
	defer ev.mu.RUnlock()
	type plain Event
	return json.Marshal((*plain)(ev))
}

// crewFor is the crew the infopack assigned to a game: by its teams'
// names, a bracket side by how infopacks write it ("Winner Game 1": "WG1",
// "Winner G1"); else by its number ("Game 3", "G3").
func (ev *Event) crewFor(g tournament.Game, teams [2]int) *crews.Crew {
	var names [2][]string
	for i, slot := range []tournament.Slot{g.Home, g.Away} {
		if no := teams[i]; no > 0 {
			if tm := ev.T.TeamByNo(no); tm != nil {
				names[i] = append(names[i], tm.Name)
			}
			if lt := ev.Teams[no]; lt != nil {
				names[i] = append(names[i], lt.Name, lt.League, lt.TeamName)
			}
		} else if slot.From != nil {
			names[i] = bracketNames(slot.From.Winner, slot.From.Game)
		}
	}
	ev.mu.RLock()
	defer ev.mu.RUnlock()
	if len(names[0]) > 0 && len(names[1]) > 0 {
		for _, c := range ev.Crews {
			for _, label := range c.Games {
				if mentionsAny(label, names[0]) && mentionsAny(label, names[1]) {
					return c
				}
			}
		}
	}
	return prepare.MatchCrewByNumber(ev.Crews, g.No)
}

// bracketNames are how infopacks write "Winner Game 1": "WG1", "W G1",
// "Winner G1", "Winner Game 1".
func bracketNames(winner bool, game int) []string {
	w, word := "L", "Loser"
	if winner {
		w, word = "W", "Winner"
	}
	n := strconv.Itoa(game)
	return []string{w + "G" + n, word + " G" + n, word + " Game " + n, word + " of Game " + n, word + " " + n}
}

// mentionsAny says whether a game label names one of these (compared
// without spaces and punctuation; "WG1" isn't in "WG12").
func mentionsAny(label string, names []string) bool {
	l := prepare.Plain(label)
	for _, n := range names {
		p := prepare.Plain(n)
		if p == "" {
			continue
		}
		for i := strings.Index(l, p); i >= 0; {
			end := i + len(p)
			lastDigit := p[len(p)-1] >= '0' && p[len(p)-1] <= '9'
			if !lastDigit || end >= len(l) || l[end] < '0' || l[end] > '9' {
				return true
			}
			next := strings.Index(l[i+1:], p)
			if next < 0 {
				break
			}
			i += 1 + next
		}
	}
	return false
}

// Crew is a crew by ID.
func (ev *Event) Crew(id string) *crews.Crew {
	ev.mu.RLock()
	defer ev.mu.RUnlock()
	for _, c := range ev.Crews {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// Build makes one game as it is before it's played, with a ruleset preset
// ("" for the tournament's).
func (ev *Event) Build(v *crgformat.Validators, no int, ch Choice, ruleset string) (*replay.Summary, error) {
	g := ev.T.GameByNo(no)
	if g == nil {
		return nil, fmt.Errorf("there is no game %d", no)
	}
	pg := prepare.Game{ID: fmt.Sprintf("%s-g%d", Slug(ev.T.Name), no), Info: map[string]any{"Tournament": ev.T.Name, "GameNo": strconv.Itoa(no)}}
	for k, val := range map[string]string{"HostLeague": ev.T.HostLeague, "Venue": ev.T.Venue.Name, "City": ev.T.Venue.City,
		"State": ev.T.Venue.State, "Date": g.Date, "StartTime": g.Time} {
		if val != "" {
			pg.Info[k] = val
		}
	}
	for _, p := range rulesets.Presets {
		if p.Name == cmp.Or(ruleset, ev.Ruleset, rulesets.Presets[0].Name) {
			pg.Ruleset = rulesets.Ruleset(p)
		}
	}
	if pg.Ruleset == nil {
		pg.Ruleset = rulesets.Ruleset(rulesets.Presets[0])
	}
	var names []string
	for i, no := range ch.Teams {
		lt := ev.Teams[no]
		if lt == nil {
			if no == 0 {
				return nil, fmt.Errorf("game %d: pick both teams", g.No)
			}
			return nil, fmt.Errorf("game %d: no charter for team %d (%s)", g.No, no, ev.Missing[no])
		}
		gt := lt.GameTeam(ch.Colors[i])
		delete(gt, "preparedTeam") // not in the Java scoreboard's team database
		pg.Teams = append(pg.Teams, gt)
		names = append(names, lt.Name)
	}
	if ch.Teams[0] == ch.Teams[1] {
		return nil, fmt.Errorf("game %d: a team can't play itself", g.No)
	}
	pg.Name = fmt.Sprintf("%s game %d: %s", ev.T.Name, no, strings.Join(names, " vs "))
	var crew *crews.Crew
	if ch.Crew != "" {
		if crew = ev.Crew(ch.Crew); crew == nil {
			return nil, fmt.Errorf("game %d: there is no crew %q", g.No, ch.Crew)
		}
	}
	_, sum, err := prepare.Build(v, pg, crew, ev.Loaded)
	return sum, err
}

// Statsbook is a game's statsbook, made from a blank one.
func Statsbook(sum *replay.Summary, blank []byte) ([]byte, error) {
	data, _, err := statsbook.Export(sum, blank)
	return data, err
}

// JavaGames is a Java scoreboard data file with these games, to import on
// its Data Management page (Import JSON).
func JavaGames(sums ...*replay.Summary) ([]byte, error) {
	state := javaws.Keys{"ScoreBoard.Version(release)": JavaVersion}
	for _, s := range sums {
		for k, val := range javaws.GameKeys(s, nil) {
			state[k] = val
		}
	}
	return json.MarshalIndent(map[string]any{"state": state}, "", "  ")
}

// FileName is a game's file name without extension: "STATS-2026-10-15_Gotham_vs_Rose".
func FileName(sum *replay.Summary) string {
	var teams []string
	for _, t := range sum.Teams {
		teams = append(teams, Slug(cmp.Or(t.League+" "+t.TeamName, t.Name)))
	}
	return fmt.Sprintf("STATS-%s_%s", cmp.Or(sum.Info["Date"], "0000-00-00"), strings.Join(teams, "_vs_"))
}

// Slug is a name as a file name part: "Gotham Roller Derby" -> "GothamRollerDerby".
func Slug(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			if up && r >= 'a' && r <= 'z' {
				r -= 'a' - 'A'
			}
			b.WriteRune(r)
			up = false
		default:
			up = true
		}
	}
	return b.String()
}

// Zip packs files (name -> content) into a zip.
func Zip(files map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		w, err := z.Create(n)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(files[n]); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
