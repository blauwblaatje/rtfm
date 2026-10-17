package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"rtfm/internal/fixture"
)

// Every team has its own link, /team/<token>, to a page with its games:
// per game it picks the skaters who play and the captain, and the home
// team (the higher seed) its bench and uniform colour. The link doesn't
// say which tournament it is, so a team can't get to the organisers' page
// from it.

var tokenRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// linkPath finds a team link or public calendar in a request's path, to
// keep it out of the logs: it is a secret.
var linkPath = regexp.MustCompile(`^/(?:team|calendar)/([0-9a-f]{32})(/|\.ics$|$)`)

// byToken is the tournament and team of a team link (0 if none).
func (s *Site) byToken(token string) (*fixture.Event, int) {
	if !tokenRe.MatchString(token) {
		return nil, 0
	}
	s.mu.Lock()
	id := s.links[token]
	s.mu.Unlock()
	ev := s.event(id)
	if ev == nil {
		return nil, 0
	}
	return ev, ev.TeamByToken(token)
}

func (s *Site) withTeam(f func(http.ResponseWriter, *http.Request, *fixture.Event, int)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ev, team := s.byToken(r.PathValue("token"))
		if team == 0 {
			w.WriteHeader(http.StatusNotFound)
			s.render(w, "home.html", map[string]any{"Error": "This team link doesn't work (any more): ask the organisers for yours."})
			return
		}
		f(w, r, ev, team)
	}
}

// newLink gives a team a new link; the old one stops working.
func (s *Site) newLink(w http.ResponseWriter, r *http.Request, ev *fixture.Event) {
	no, _ := strconv.Atoi(r.PathValue("no"))
	old, token, err := ev.NewLink(no)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.mu.Lock()
	delete(s.links, old)
	s.links[token] = ev.ID
	s.mu.Unlock()
	s.save(ev)
	s.logf(ev.ID, "team %d %q: new link, the old one no longer works", no, ev.T.TeamByNo(no).Name)
	writeJSON(w, map[string]string{"link": "/team/" + token})
}

// putChoice keeps the organiser's choice for a game: teams, colours, crew.
func (s *Site) putChoice(w http.ResponseWriter, r *http.Request, ev *fixture.Event) {
	no, _ := strconv.Atoi(r.PathValue("no"))
	var ch fixture.Choice
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&ch); err != nil {
		http.Error(w, "choice: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := ev.SetChoice(no, ch); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	s.save(ev)
	s.logf(ev.ID, "game %d picked: teams %v, colours %q, crew %q", no, ch.Teams, ch.Colors, ch.Crew)
	w.WriteHeader(http.StatusNoContent)
}

// patchChoice changes one thing of the organiser's choice for a game; the
// page sends only what was changed, so a page that's behind doesn't undo
// what someone else changed meanwhile.
func (s *Site) patchChoice(w http.ResponseWriter, r *http.Request, ev *fixture.Event) {
	no, _ := strconv.Atoi(r.PathValue("no"))
	var p fixture.ChoicePatch
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&p); err != nil {
		http.Error(w, "choice: "+err.Error(), http.StatusBadRequest)
		return
	}
	ch, err := ev.PatchChoice(no, p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	s.save(ev)
	s.logf(ev.ID, "game %d picked: teams %v, colours %q, crew %q", no, ch.Teams, ch.Colors, ch.Crew)
	writeJSON(w, ch)
}

func (s *Site) clearChoices(w http.ResponseWriter, r *http.Request, ev *fixture.Event) {
	ev.ClearChoices()
	s.save(ev)
	s.logf(ev.ID, "games back to their defaults")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Site) teamPage(w http.ResponseWriter, r *http.Request, ev *fixture.Event, team int) {
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, "team.html", map[string]any{"Tournament": ev.T.Name, "Team": ev.T.TeamByNo(team).Name, "Token": r.PathValue("token"),
		"Max": fixture.MaxSkaters})
}

// TeamPageView is what a team's page shows.
type TeamPageView struct {
	Tournament string         `json:"tournament"`
	Dates      string         `json:"dates"`
	Venue      string         `json:"venue"`
	Team       string         `json:"team"`
	Charter    string         `json:"charter,omitempty"`
	Missing    string         `json:"missing,omitempty"` // why there's no charter
	Colors     []string       `json:"colors"`
	Skaters    []SkaterView   `json:"skaters"`
	Max        int            `json:"max"`
	Benches    [2]string      `json:"benches"`
	Games      []TeamGameView `json:"games"`
}

// SkaterView is a skater on the team's charter.
type SkaterView struct {
	Number   string `json:"number"`
	Name     string `json:"name"`
	Pronouns string `json:"pronouns,omitempty"`
}

// TeamGameView is one of the team's games.
type TeamGameView struct {
	No       int             `json:"no"`
	Date     string          `json:"date"`
	Time     string          `json:"time"`
	Track    string          `json:"track,omitempty"`
	Type     string          `json:"type,omitempty"`
	Home     bool            `json:"home"` // the higher seed: picks bench and colour
	Opponent string          `json:"opponent"`
	Color    string          `json:"color"`         // the team's uniform colour as it stands
	Other    string          `json:"opponentColor"` // the opponent's
	Bench    string          `json:"bench"`         // the team's bench: as the home team picked, the other for the away team
	Lineup   *fixture.Lineup `json:"lineup"`        // what the team said; nil if nothing yet
	Open     bool            `json:"open"`          // the line-up can be changed
	Closes   string          `json:"closes"`        // when it can't any more, by the venue's clock ("" if there's no deadline)
}

func (s *Site) teamData(w http.ResponseWriter, r *http.Request, ev *fixture.Event, team int) {
	t := ev.T
	v := TeamPageView{Tournament: t.Name, Dates: t.Dates, Team: t.TeamByNo(team).Name, Missing: ev.Missing[team],
		Venue:  strings.Trim(strings.Join([]string{t.Venue.Name, t.Venue.City, t.Venue.Country}, ", "), ", "),
		Colors: []string{}, Skaters: []SkaterView{}, Max: fixture.MaxSkaters, Benches: ev.Benches(), Games: []TeamGameView{}}
	if lt := ev.Teams[team]; lt != nil {
		v.Charter = strings.Trim(lt.League+" - "+lt.TeamName, " -")
		v.Colors = append(v.Colors, lt.UniformColors...)
		for _, sk := range lt.Skaters {
			v.Skaters = append(v.Skaters, SkaterView{Number: strings.TrimSpace(sk.Number), Name: sk.Name, Pronouns: sk.Pronouns})
		}
	}
	for _, g := range ev.TeamGames(team) {
		side := 0
		if g.Choice.Teams[1] == team {
			side = 1
		}
		gv := TeamGameView{No: g.No, Date: g.Date, Time: g.Time, Track: g.Track, Type: g.Type, Home: side == 0,
			Color: g.Choice.Colors[side], Other: g.Choice.Colors[1-side], Lineup: g.Lineups[side]}
		if other := t.TeamByNo(g.Choice.Teams[1-side]); other != nil {
			gv.Opponent = other.Name
		} else {
			gv.Opponent = g.Sides[1-side]
		}
		if home := g.Lineups[0]; home != nil {
			gv.Bench = home.Bench
			if side == 1 {
				gv.Bench = ev.OtherBench(home.Bench)
			}
		}
		gv.Open = g.LineupState.Open
		if !g.LineupState.Closes.IsZero() {
			gv.Closes = ev.FormatTime(g.LineupState.Closes)
		}
		v.Games = append(v.Games, gv)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, v)
}

// putLineup keeps what a team says about one of its games.
func (s *Site) putLineup(w http.ResponseWriter, r *http.Request, ev *fixture.Event, team int) {
	no, _ := strconv.Atoi(r.PathValue("no"))
	var l fixture.Lineup
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&l); err != nil {
		http.Error(w, "line-up: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := ev.T.TeamByNo(team).Name
	saved, err := ev.SetLineup(no, team, l)
	if err != nil {
		s.logf(ev.ID, "team %d %q game %d line-up refused: %v", team, name, no, err)
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	s.save(ev)
	s.logf(ev.ID, "team %d %q game %d line-up: %d skaters %v, captain %q, bench %q, colour %q", team, name, no,
		len(saved.Skaters), saved.Skaters, saved.Captain, saved.Bench, saved.Color)
	writeJSON(w, saved)
}

func (s *Site) teamLog(w http.ResponseWriter, r *http.Request, ev *fixture.Event, team int) {
	s.browserLog(w, r, ev.ID, fmt.Sprintf("team %d %q", team, ev.T.TeamByNo(team).Name))
}

// SettingsView is the organiser's settings for the line-ups.
type SettingsView struct {
	Benches     [2]string `json:"benches"`
	CloseHours  float64   `json:"closeHours"` // line-ups close this many hours before a game
	TimeZone    string    `json:"timeZone"`   // the venue's, as used
	ZoneGuessed bool      `json:"zoneGuessed"`
}

func settingsView(ev *fixture.Event) SettingsView {
	loc, guessed := ev.Location()
	return SettingsView{Benches: ev.Benches(), CloseHours: ev.CloseBefore().Hours(), TimeZone: loc.String(), ZoneGuessed: guessed}
}

// putSettings keeps the organiser's settings for the line-ups.
func (s *Site) putSettings(w http.ResponseWriter, r *http.Request, ev *fixture.Event) {
	var v SettingsView
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&v); err != nil {
		http.Error(w, "settings: "+err.Error(), http.StatusBadRequest)
		return
	}
	minutes := int(v.CloseHours*60 + 0.5)
	if err := ev.SetSettings(fixture.Settings{Benches: v.Benches, CloseBefore: &minutes, TimeZone: v.TimeZone}); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	s.save(ev)
	s.logf(ev.ID, "line-up settings: benches %q, close %d minutes before a game, time zone %q", v.Benches, minutes, v.TimeZone)
	writeJSON(w, settingsView(ev))
}

// putLineupMode opens or closes a game's line-ups, or puts them back on
// the deadline.
func (s *Site) putLineupMode(w http.ResponseWriter, r *http.Request, ev *fixture.Event) {
	no, _ := strconv.Atoi(r.PathValue("no"))
	var v struct{ Mode string }
	json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&v)
	if err := ev.SetLineupMode(no, v.Mode); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	s.save(ev)
	s.logf(ev.ID, "game %d line-ups: %s", no, v.Mode)
	w.WriteHeader(http.StatusNoContent)
}
