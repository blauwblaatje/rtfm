// Package web is RTFM's website: paste a sanctioning application (and an
// infopack), pick the teams of bracket games and each game's crew, download
// pre-game statsbooks and Java scoreboard game files.
package web

import (
	"bytes"
	"cmp"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"crgformat"
	"crgformat/replay"
	"crgformat/rulesets"

	"rtfm/internal/fixture"
	"rtfm/internal/logs"
)

//go:embed static templates
var files embed.FS

// Config is how the site runs.
type Config struct {
	// Blank is the folder with WFTDA's blank statsbooks: any .xlsx in it is
	// offered, named after its file ("wftda-statsbook-full-A4").
	Blank string
	// Data is where loaded tournaments are kept, so they outlive a restart
	// ("" keeps them in memory only).
	Data string
	// Keep is how long a loaded tournament is kept.
	Keep time.Duration
	// Fetch downloads Google Sheets (fixture.FetchSheet, or a test's).
	Fetch fixture.Fetch
	// Logs: everything to logs/rtfm.log, and per tournament to logs/<id>.log.
	Logs *logs.Logs
}

// Site serves RTFM.
type Site struct {
	cfg  Config
	v    *crgformat.Validators
	tmpl *template.Template

	mu     sync.Mutex
	events map[string]*fixture.Event
}

// New makes the site.
func New(cfg Config) (*Site, error) {
	v, err := crgformat.NewValidators()
	if err != nil {
		return nil, err
	}
	if cfg.Fetch == nil {
		cfg.Fetch = fixture.FetchSheet
	}
	if cfg.Logs == nil {
		cfg.Logs, _ = logs.Open("", os.Stderr)
	}
	if cfg.Keep == 0 {
		cfg.Keep = 60 * 24 * time.Hour
	}
	tmpl, err := template.ParseFS(files, "templates/*.html")
	if err != nil {
		return nil, err
	}
	s := &Site{cfg: cfg, v: v, tmpl: tmpl, events: map[string]*fixture.Event{}}
	if cfg.Data != "" {
		if err := os.MkdirAll(cfg.Data, 0o755); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Handler is the site's HTTP handler.
func (s *Site) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(files, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("POST /load", s.load)
	mux.HandleFunc("GET /t/{id}", s.withEvent(s.page))
	mux.HandleFunc("GET /t/{id}/data.json", s.withEvent(s.data))
	mux.HandleFunc("GET /t/{id}/game/{no}/{file}", s.withEvent(s.gameFile))
	mux.HandleFunc("POST /t/{id}/all.zip", s.withEvent(s.all))
	mux.HandleFunc("POST /log", s.clientLog)
	mux.HandleFunc("POST /t/{id}/log", s.clientLog)
	return s.logRequests(securityHeaders(mux))
}

// logf logs for a tournament ("" for none).
func (s *Site) logf(id, format string, args ...any) { s.cfg.Logs.Printf(id, format, args...) }

// logRequests logs every request with its answer and time; a failed one
// with what it answered, and one about a tournament in its log too.
func (s *Site) logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/static/") && rec.status < 400 {
			return
		}
		id := ""
		if m := pathID.FindStringSubmatch(r.URL.Path); m != nil {
			id = m[1]
		}
		msg := fmt.Sprintf("%s %s %d %s %dB", r.Method, r.URL.RequestURI(), rec.status, time.Since(start).Round(time.Millisecond), rec.size)
		if rec.status >= 400 && rec.errBody.Len() > 0 {
			msg += ": " + strings.TrimSpace(rec.errBody.String())
		}
		if loc := rec.Header().Get("Location"); rec.status == http.StatusSeeOther && strings.Contains(loc, "error=") {
			if u, err := url.Parse(loc); err == nil {
				msg += " -> error: " + u.Query().Get("error")
			}
		}
		s.logf(id, "%s", msg)
	})
}

var pathID = regexp.MustCompile(`^/t/([0-9a-f]{10})(?:/|$)`)

type recorder struct {
	http.ResponseWriter
	status  int
	size    int
	errBody bytes.Buffer
}

func (r *recorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }
func (r *recorder) Write(b []byte) (int, error) {
	if r.status >= 400 && r.errBody.Len() < 500 {
		r.errBody.Write(b[:min(len(b), 500-r.errBody.Len())])
	}
	r.size += len(b)
	return r.ResponseWriter.Write(b)
}

// clientLog takes an error the page ran into in the browser, so it is in
// the logs too.
func (s *Site) clientLog(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	var e struct {
		Message, Source, Stack, Page, Agent string
		Line, Column                        int
	}
	if json.Unmarshal(body, &e) != nil {
		http.Error(w, "bad log", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	if !idRe.MatchString(id) {
		id = ""
	}
	clean := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " | ") }
	s.logf(id, "BROWSER ERROR on %s: %s (%s:%d:%d) stack: %s agent: %s", clean(e.Page), clean(e.Message), clean(e.Source),
		e.Line, e.Column, clean(e.Stack), clean(e.Agent))
	w.WriteHeader(http.StatusNoContent)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:")
		h.ServeHTTP(w, r)
	})
}

// --- pages ---------------------------------------------------------------------------

func (s *Site) home(w http.ResponseWriter, r *http.Request) {
	s.render(w, "home.html", map[string]any{"Error": r.URL.Query().Get("error")})
}

func (s *Site) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.logf("", "page %s: %v", name, err)
		http.Error(w, "page error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

// load reads the sanctioning application and the infopack, each a Google
// Sheets link or an uploaded file, and goes to the tournament's page.
func (s *Site) load(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	fail := func(err error) {
		http.Redirect(w, r, "/?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		fail(err)
		return
	}
	app, err := s.input(r, "sanction")
	if err != nil {
		s.logf("", "loading: sanctioning application: %v", err)
		fail(fmt.Errorf("sanctioning application: %w", err))
		return
	}
	if app == nil {
		fail(errors.New("paste the sanctioning application's link, or choose its file"))
		return
	}
	info, err := s.input(r, "infopack")
	if err != nil {
		s.logf("", "loading: infopack: %v", err)
		fail(fmt.Errorf("infopack: %w", err))
		return
	}
	id := fixture.NewID()
	from := func(field string) string {
		if f, h, err := r.FormFile(field + "File"); err == nil {
			f.Close()
			return fmt.Sprintf("file %q (%d bytes)", h.Filename, h.Size)
		}
		return strings.TrimSpace(r.FormValue(field))
	}
	s.logf(id, "loading: sanctioning application %s, infopack %s", from("sanction"), cmp.Or(from("infopack"), "none"))
	ev, err := fixture.Load(app, info, s.cfg.Fetch, fixture.Options{ID: id, Logf: s.cfg.Logs.For(id)})
	if err != nil {
		s.logf(id, "loading FAILED: %v", err)
		fail(err)
		return
	}
	s.mu.Lock()
	s.events[ev.ID] = ev
	s.mu.Unlock()
	s.save(ev)
	s.logf(ev.ID, "loaded %q: %d teams, %d games, %d crews", ev.T.Name, len(ev.Teams), len(ev.T.Games), len(ev.Crews))
	http.Redirect(w, r, "/t/"+ev.ID, http.StatusSeeOther)
}

// input is a form's link (field) or file (field+"File"); nil if neither.
func (s *Site) input(r *http.Request, field string) ([]byte, error) {
	if f, _, err := r.FormFile(field + "File"); err == nil {
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 32<<20))
		if err != nil {
			return nil, err
		}
		if len(data) > 0 {
			if !bytes.HasPrefix(data, []byte("PK")) {
				return nil, errors.New("that file isn't an .xlsx spreadsheet")
			}
			return data, nil
		}
	}
	link := strings.TrimSpace(r.FormValue(field))
	if link == "" {
		return nil, nil
	}
	return s.cfg.Fetch(link)
}

func (s *Site) withEvent(f func(http.ResponseWriter, *http.Request, *fixture.Event)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ev := s.event(r.PathValue("id"))
		if ev == nil {
			http.Redirect(w, r, "/?error="+url.QueryEscape("That tournament isn't here (any more): load it again."), http.StatusSeeOther)
			return
		}
		f(w, r, ev)
	}
}

func (s *Site) page(w http.ResponseWriter, r *http.Request, ev *fixture.Event) {
	s.render(w, "tournament.html", map[string]any{"Event": ev, "Java": fixture.JavaVersion})
}

// EventView is what the tournament page shows.
type EventView struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Dates    string         `json:"dates"`
	Venue    string         `json:"venue"`
	Host     string         `json:"host"`
	Teams    []TeamView     `json:"teams"`
	Games    []GameView     `json:"games"`
	Crews    []CrewView     `json:"crews"`
	Notes    []string       `json:"notes"`
	Rulesets []string       `json:"rulesets"`
	Blanks   []string       `json:"blanks"`
	Java     string         `json:"java"`
	Loaded   time.Time      `json:"loaded"`
	Missing  map[int]string `json:"missing"`
}

// TeamView is a team with its charter.
type TeamView struct {
	No      int      `json:"no"`
	Name    string   `json:"name"`
	Charter string   `json:"charter,omitempty"` // the charter's league and team
	Skaters int      `json:"skaters"`
	Colors  []string `json:"colors"`
	Missing string   `json:"missing,omitempty"`
}

// GameView is a game on the schedule.
type GameView struct {
	No      int            `json:"no"`
	Date    string         `json:"date"`
	Time    string         `json:"time"`
	Track   string         `json:"track"`
	Type    string         `json:"type"`
	Notes   string         `json:"notes"`
	Sides   [2]string      `json:"sides"`
	Fixed   [2]bool        `json:"fixed"`
	Default fixture.Choice `json:"default"`
}

// CrewView is a crew from the infopack.
type CrewView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Officials int      `json:"officials"`
	Heads     []string `json:"heads"`
	Games     []string `json:"games"`
}

func (s *Site) data(w http.ResponseWriter, r *http.Request, ev *fixture.Event) {
	t := ev.T
	v := EventView{ID: ev.ID, Name: t.Name, Dates: t.Dates, Venue: strings.Trim(strings.Join([]string{t.Venue.Name, t.Venue.City, t.Venue.Country}, ", "), ", "),
		Host: t.HostLeague, Notes: append([]string{}, ev.Notes...), Java: fixture.JavaVersion, Loaded: ev.Loaded, Blanks: s.blanks(),
		Missing: ev.Missing, Teams: []TeamView{}, Games: []GameView{}, Crews: []CrewView{}, Rulesets: []string{}}
	if v.Missing == nil {
		v.Missing = map[int]string{}
	}
	for _, p := range rulesets.Presets {
		v.Rulesets = append(v.Rulesets, p.Name)
	}
	for _, tm := range t.Teams {
		tv := TeamView{No: tm.No, Name: tm.Name, Missing: ev.Missing[tm.No], Colors: []string{}}
		if lt := ev.Teams[tm.No]; lt != nil {
			tv.Charter = strings.Trim(lt.League+" - "+lt.TeamName, " -")
			tv.Skaters = len(lt.Skaters)
			tv.Colors = append(tv.Colors, lt.UniformColors...)
		}
		v.Teams = append(v.Teams, tv)
	}
	for _, g := range ev.Games() {
		v.Games = append(v.Games, GameView{No: g.No, Date: g.Date, Time: g.Time, Track: g.Track, Type: g.Type, Notes: g.Notes,
			Sides: g.Sides, Fixed: g.Fixed, Default: g.Default})
	}
	for _, c := range ev.Crews {
		cv := CrewView{ID: c.ID, Name: c.Name, Officials: len(c.Officials), Games: append([]string{}, c.Games...), Heads: []string{}}
		for _, o := range c.Officials {
			if o.Head {
				cv.Heads = append(cv.Heads, o.Name+" ("+o.Role+")")
			}
		}
		v.Crews = append(v.Crews, cv)
	}
	writeJSON(w, v)
}

// --- downloads -----------------------------------------------------------------------

// choice reads a game's choices from a query: t1, t2 (team numbers), c1,
// c2 (uniform colours), crew.
func choice(q map[string][]string) fixture.Choice {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	var ch fixture.Choice
	ch.Teams[0], _ = strconv.Atoi(get("t1"))
	ch.Teams[1], _ = strconv.Atoi(get("t2"))
	ch.Colors = [2]string{get("c1"), get("c2")}
	ch.Crew = get("crew")
	return ch
}

var fileRe = regexp.MustCompile(`^(statsbook\.xlsx|java\.json)$`)

func (s *Site) gameFile(w http.ResponseWriter, r *http.Request, ev *fixture.Event) {
	no, _ := strconv.Atoi(r.PathValue("no"))
	file := r.PathValue("file")
	if !fileRe.MatchString(file) {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	ch := choice(q)
	sum, err := s.build(ev, no, ch, q.Get("rules"))
	if err != nil {
		s.logf(ev.ID, "game %d %s: can't make it (teams %v, colours %q, crew %q): %v", no, file, ch.Teams, ch.Colors, ch.Crew, err)
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	var data []byte
	name := fixture.FileName(sum)
	switch file {
	case "statsbook.xlsx":
		blank, err := s.blank(q.Get("paper"))
		if err == nil {
			data, err = fixture.Statsbook(sum, blank)
		}
		if err != nil {
			s.logf(ev.ID, "game %d statsbook (%q): %v", no, q.Get("paper"), err)
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		name += ".xlsx"
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	default:
		if data, err = fixture.JavaGames(sum); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		name = "crg-game-" + strings.TrimPrefix(name, "STATS-") + ".json"
		w.Header().Set("Content-Type", "application/json")
	}
	s.logf(ev.ID, "game %d: %s (teams %v, colours %q, crew %q, %d skaters, %d officials)", no, name, ch.Teams, ch.Colors, ch.Crew,
		len(sum.Teams[0].Skaters)+len(sum.Teams[1].Skaters), len(sum.Officials))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Write(data)
}

// all is every game whose teams are known, in one zip: the statsbooks and
// one Java scoreboard file with all the games. The form has "choices": a
// JSON map from game number to its choice, and "rules" and "paper".
func (s *Site) all(w http.ResponseWriter, r *http.Request, ev *fixture.Event) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	choices := map[string]fixture.Choice{}
	if err := json.Unmarshal([]byte(r.FormValue("choices")), &choices); err != nil {
		s.logf(ev.ID, "all.zip: choices %q: %v", r.FormValue("choices"), err)
		http.Error(w, "choices: "+err.Error(), http.StatusBadRequest)
		return
	}
	blank, blankErr := s.blank(r.FormValue("paper"))
	files := map[string][]byte{}
	var sums []*replay.Summary
	var skipped []string
	for _, g := range ev.T.Games {
		ch, ok := choices[strconv.Itoa(g.No)]
		if !ok || ch.Teams[0] == 0 || ch.Teams[1] == 0 {
			skipped = append(skipped, fmt.Sprintf("game %d: teams not picked yet", g.No))
			continue
		}
		sum, err := s.build(ev, g.No, ch, r.FormValue("rules"))
		if err != nil {
			skipped = append(skipped, err.Error())
			continue
		}
		sums = append(sums, sum)
		prefix := fmt.Sprintf("%02d ", g.No)
		if blankErr == nil {
			sb, err := fixture.Statsbook(sum, blank)
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("game %d statsbook: %v", g.No, err))
			} else {
				files["statsbooks/"+prefix+fixture.FileName(sum)+".xlsx"] = sb
			}
		}
	}
	if len(sums) == 0 {
		http.Error(w, "no game has both teams picked", http.StatusUnprocessableEntity)
		return
	}
	java, err := fixture.JavaGames(sums...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	files["crg-games-"+fixture.Slug(ev.T.Name)+".json"] = java
	if blankErr != nil {
		skipped = append(skipped, "statsbooks: "+blankErr.Error())
	}
	readme := fmt.Sprintf("%s\n\nMade by RTFM (Roller derby Tournament Fixture Maker) on %s.\n\n"+
		"statsbooks/: a WFTDA statsbook per game with the IGRF filled in.\n"+
		"crg-games-*.json: all %d games for the CRG scoreboard %s: Data Management (Settings), Import JSON.\n",
		ev.T.Name, time.Now().UTC().Format("2006-01-02 15:04 UTC"), len(sums), fixture.JavaVersion)
	if len(skipped) > 0 {
		readme += "\nLeft out:\n- " + strings.Join(skipped, "\n- ") + "\n"
	}
	files["README.txt"] = []byte(readme)
	data, err := fixture.Zip(files)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.logf(ev.ID, "all.zip: %d games, %d files; left out: %s", len(sums), len(files), cmp.Or(strings.Join(skipped, "; "), "nothing"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fixture.Slug(ev.T.Name)+".zip"))
	w.Write(data)
}

func (s *Site) build(ev *fixture.Event, no int, ch fixture.Choice, rules string) (*replay.Summary, error) {
	e := *ev // the ruleset for this download only
	if rules != "" {
		e.Ruleset = rules
	}
	return e.Build(s.v, no, ch)
}

// blanks are the blank statsbooks there are, by name.
func (s *Site) blanks() []string {
	out := []string{}
	if s.cfg.Blank == "" {
		return out
	}
	list, _ := filepath.Glob(filepath.Join(s.cfg.Blank, "*.xlsx"))
	for _, f := range list {
		out = append(out, strings.TrimSuffix(filepath.Base(f), ".xlsx"))
	}
	slices.Sort(out)
	return out
}

func (s *Site) blank(name string) ([]byte, error) {
	list := s.blanks()
	if len(list) == 0 {
		return nil, errors.New("this site has no blank WFTDA statsbook to fill in (see the README: RTFM_BLANK)")
	}
	pick := list[0]
	if slices.Contains(list, name) {
		pick = name
	}
	return os.ReadFile(filepath.Join(s.cfg.Blank, pick+".xlsx"))
}

// --- keeping tournaments -------------------------------------------------------------

var idRe = regexp.MustCompile(`^[0-9a-f]{10}$`)

func (s *Site) event(id string) *fixture.Event {
	if !idRe.MatchString(id) {
		return nil
	}
	s.mu.Lock()
	ev := s.events[id]
	s.mu.Unlock()
	if ev == nil && s.cfg.Data != "" {
		data, err := os.ReadFile(filepath.Join(s.cfg.Data, id+".json"))
		if err == nil {
			ev = &fixture.Event{}
			if json.Unmarshal(data, ev) != nil {
				return nil
			}
			s.mu.Lock()
			s.events[id] = ev
			s.mu.Unlock()
		}
	}
	if ev != nil && time.Since(ev.Loaded) > s.cfg.Keep {
		return nil
	}
	return ev
}

func (s *Site) save(ev *fixture.Event) {
	if s.cfg.Data == "" {
		return
	}
	data, err := json.Marshal(ev)
	if err == nil {
		err = os.WriteFile(filepath.Join(s.cfg.Data, ev.ID+".json"), data, 0o644)
	}
	if err != nil {
		s.logf(ev.ID, "keeping it on disk: %v", err)
	}
}

// Clean forgets tournaments older than Keep, in memory and on disk.
func (s *Site) Clean() {
	s.mu.Lock()
	for id, ev := range s.events {
		if time.Since(ev.Loaded) > s.cfg.Keep {
			delete(s.events, id)
			s.cfg.Logs.Remove(id)
		}
	}
	s.mu.Unlock()
	if s.cfg.Data == "" {
		return
	}
	list, _ := filepath.Glob(filepath.Join(s.cfg.Data, "*.json"))
	for _, f := range list {
		if st, err := os.Stat(f); err == nil && time.Since(st.ModTime()) > s.cfg.Keep {
			os.Remove(f)
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
