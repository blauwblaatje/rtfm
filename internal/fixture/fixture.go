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
}

// Load reads a sanctioning application and its teams' charters (fetched,
// several at a time), and an infopack if there is one.
func Load(application, infopack []byte, fetch Fetch) (*Event, error) {
	t, notes, err := tournament.Read(application)
	if err != nil {
		return nil, fmt.Errorf("reading the sanctioning application: %w", err)
	}
	ev := &Event{ID: prepare.NewID(""), Loaded: time.Now().UTC(), T: t, Teams: map[int]*library.Team{},
		Missing: map[int]string{}, Notes: notes, Ruleset: rulesets.Presets[0].Name}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, tm := range t.Teams {
		if tm.CharterURL == "" {
			ev.Missing[tm.No] = "no charter link on the application"
			continue
		}
		wg.Add(1)
		go func(tm tournament.Team) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, err := fetch(tm.CharterURL)
			var lt *library.Team
			var cn []string
			if err == nil {
				lt, cn, err = library.ReadCharter(data)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				ev.Missing[tm.No] = err.Error()
				return
			}
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
			return nil, fmt.Errorf("reading the infopack: %w", err)
		}
		for i, c := range cs {
			c.ID = fmt.Sprintf("crew-%d", i+1)
		}
		ev.Crews = cs
		ev.Notes = append(ev.Notes, cn...)
	}
	slices.Sort(ev.Notes)
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
		if c := ev.crewFor(v.Default.Teams); c != nil {
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

// crewFor is the crew the infopack assigned to the game between two teams.
func (ev *Event) crewFor(teams [2]int) *crews.Crew {
	if teams[0] == 0 || teams[1] == 0 {
		return nil
	}
	var names [2][]string
	for i, no := range teams {
		if tm := ev.T.TeamByNo(no); tm != nil {
			names[i] = append(names[i], tm.Name)
		}
		if lt := ev.Teams[no]; lt != nil {
			names[i] = append(names[i], lt.Name, lt.League, lt.TeamName)
		}
	}
	return prepare.MatchCrew(ev.Crews, names)
}

// Crew is a crew by ID.
func (ev *Event) Crew(id string) *crews.Crew {
	for _, c := range ev.Crews {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// Build makes one game as it is before it's played.
func (ev *Event) Build(v *crgformat.Validators, no int, ch Choice) (*replay.Summary, error) {
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
		if p.Name == cmp.Or(ev.Ruleset, rulesets.Presets[0].Name) {
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
