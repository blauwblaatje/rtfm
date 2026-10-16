package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"crgformat/crews"

	"github.com/xuri/excelize/v2"

	"rtfm/internal/fixture"
	"rtfm/internal/logs"
)

const testData = "../../.."

func localSheets(link string) ([]byte, error) {
	id := fixture.SheetID(link)
	if id == "" {
		return nil, errors.New("not a sheet link")
	}
	return os.ReadFile(filepath.Join(testData, "sanctioning/charters", id+".xlsx"))
}

// The whole round: load the Championships (application and infopack as
// files), then the page's data and the downloads.
func TestChampionshipsSite(t *testing.T) {
	app, err := os.ReadFile(filepath.Join(testData, "sanctioning/2026-wftda-championships.xlsx"))
	if err != nil {
		t.Skip("no Championships application")
	}
	info, _ := os.ReadFile(filepath.Join(testData, "infopacks/champs-2026.xlsx"))
	logDir := t.TempDir()
	lg, err := logs.Open(logDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	site, err := New(Config{Blank: filepath.Join(testData, "statsbooks/template"), Data: t.TempDir(), Fetch: localSheets, Logs: lg})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(site.Handler())
	defer ts.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("sanctionFile", "app.xlsx")
	fw.Write(app)
	fw, _ = mw.CreateFormFile("infopackFile", "info.xlsx")
	fw.Write(info)
	mw.Close()
	resp, err := client.Post(ts.URL+"/load", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/t/") {
		t.Fatalf("load: %d %s", resp.StatusCode, loc)
	}
	get := func(path string) (*http.Response, []byte) {
		t.Helper()
		r, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r, b
	}
	if r, b := get(loc); r.StatusCode != 200 || !strings.Contains(string(b), "2026 WFTDA Championships") {
		t.Fatalf("page %d", r.StatusCode)
	}
	var v EventView
	_, b := get(loc + "/data.json")
	json.Unmarshal(b, &v)
	if len(v.Teams) != 16 || len(v.Games) != 23 || len(v.Crews) == 0 || len(v.Blanks) != 2 {
		t.Fatalf("data: %d teams, %d games, %d crews, blanks %v", len(v.Teams), len(v.Games), len(v.Crews), v.Blanks)
	}
	g1 := v.Games[0]
	q := url.Values{"t1": {itoa(g1.Default.Teams[0])}, "t2": {itoa(g1.Default.Teams[1])}, "c1": {g1.Default.Colors[0]},
		"c2": {g1.Default.Colors[1]}, "crew": {g1.Default.Crew}, "paper": {v.Blanks[0]}}.Encode()
	r, sb := get(loc + "/game/1/statsbook.xlsx?" + q)
	if r.StatusCode != 200 || !bytes.HasPrefix(sb, []byte("PK")) || !strings.Contains(r.Header.Get("Content-Disposition"), "STATS-2026-10-15") {
		t.Errorf("statsbook: %d %s", r.StatusCode, r.Header.Get("Content-Disposition"))
	}
	r, java := get(loc + "/game/1/java.json?" + q)
	if r.StatusCode != 200 || !strings.Contains(string(java), `"ScoreBoard.Version(release)": "v2025.10"`) {
		t.Errorf("java: %d", r.StatusCode)
	}
	// A bracket game without its teams answers with why.
	if r, b := get(loc + "/game/23/java.json"); r.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(b), "pick both teams") {
		t.Errorf("final without teams: %d %s", r.StatusCode, b)
	}
	// All: the fixed games, and the final with teams picked (kept on the
	// server).
	final := v.Games[len(v.Games)-1]
	cj, _ := json.Marshal(fixture.Choice{Teams: [2]int{1, 2}})
	req, _ := http.NewRequest("PUT", ts.URL+loc+"/games/"+itoa(final.No), bytes.NewReader(cj))
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode != http.StatusNoContent {
		t.Fatalf("picking the final's teams: %v %v", err, r)
	}
	r, err = http.PostForm(ts.URL+loc+"/all.zip", url.Values{"paper": {v.Blanks[0]}})
	if err != nil {
		t.Fatal(err)
	}
	zb, _ := io.ReadAll(r.Body)
	r.Body.Close()
	z, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	if err != nil {
		t.Fatalf("zip: %v (%s)", err, zb[:min(200, len(zb))])
	}
	books := 0
	for _, f := range z.File {
		if strings.HasPrefix(f.Name, "statsbooks/") {
			books++
		}
	}
	fixed := 0
	for _, g := range v.Games {
		if g.Fixed[0] && g.Fixed[1] {
			fixed++
		}
	}
	if books != fixed+1 {
		t.Errorf("%d statsbooks in the zip, want %d", books, fixed+1)
	}
	// A tab's games only: one crew's, say.
	var crewGames []string
	for _, g := range v.Games {
		if g.Fixed[0] && g.Fixed[1] && g.Choice.Crew == v.Games[0].Choice.Crew {
			crewGames = append(crewGames, itoa(g.No))
		}
	}
	r, err = http.PostForm(ts.URL+loc+"/all.zip", url.Values{"paper": {v.Blanks[0]}, "games": {strings.Join(crewGames, ",")}, "label": {"Crew One"}})
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("a crew's zip: %v %v", err, r)
	}
	zb, _ = io.ReadAll(r.Body)
	r.Body.Close()
	z, _ = zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	books = 0
	for _, f := range z.File {
		if strings.HasPrefix(f.Name, "statsbooks/") {
			books++
		}
	}
	if books != len(crewGames) || !strings.Contains(r.Header.Get("Content-Disposition"), "CrewOne") {
		t.Errorf("a crew's zip: %d statsbooks for games %v, %s", books, crewGames, r.Header.Get("Content-Disposition"))
	}
	// The logs: everything in rtfm.log, this tournament's in <id>.log,
	// errors the page ran into too.
	id := strings.TrimPrefix(loc, "/t/")
	r, err = http.Post(ts.URL+loc+"/log", "application/json", strings.NewReader(`{"message":"Cannot read properties of null (reading 'length')","source":"app.js","line":96}`))
	if err != nil || r.StatusCode != http.StatusNoContent {
		t.Fatalf("browser log: %v %v", err, r)
	}
	own, _ := os.ReadFile(filepath.Join(logDir, id+".log"))
	for _, want := range []string{"loading: sanctioning application file", "charter read in", "infopack: crew", "loaded \"2026 WFTDA Championships\"",
		"game 1: STATS-2026-10-15", "game 23 java.json: can't make it", "all.zip: 5 games", "BROWSER ERROR", "reading 'length'"} {
		if !strings.Contains(string(own), want) {
			t.Errorf("%s.log has no %q", id, want)
		}
	}
	all, _ := os.ReadFile(filepath.Join(logDir, "rtfm.log"))
	if !strings.Contains(string(all), "["+id+"] loaded") || !strings.Contains(string(all), "POST /load 303") {
		t.Errorf("rtfm.log:\n%s", all)
	}
	// Kept: a new site on the same folder still has it.
	site2, _ := New(Config{Data: site.cfg.Data, Fetch: localSheets})
	if site2.event(strings.TrimPrefix(loc, "/t/")) == nil {
		t.Error("the tournament wasn't kept")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// Crews: filled in from WFTDA's roster (and from a list), edited, and on
// the IGRF of the games they officiate.
func TestCrewsEditAndFill(t *testing.T) {
	app, err := os.ReadFile(filepath.Join(testData, "sanctioning/2026-wftda-championships.xlsx"))
	if err != nil {
		t.Skip("no Championships application")
	}
	info, _ := os.ReadFile(filepath.Join(testData, "infopacks/champs-2026.xlsx"))
	roster, _ := os.ReadFile("../fixture/testdata/wftda-roster.html")
	lg, _ := logs.Open(t.TempDir(), nil)
	asked := 0
	site, _ := New(Config{Blank: filepath.Join(testData, "statsbooks/template"), Fetch: localSheets, Logs: lg,
		Roster: func() ([]byte, error) { asked++; return roster, nil }})
	ts := httptest.NewServer(site.Handler())
	defer ts.Close()
	ev, err := fixture.Load(app, info, localSheets, fixture.Options{})
	if err != nil {
		t.Fatal(err)
	}
	site.events[ev.ID] = ev
	base := ts.URL + "/t/" + ev.ID

	var ans CrewsAnswer
	r, err := http.Post(base+"/crews/roster", "application/x-www-form-urlencoded", nil)
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("roster: %v %v", err, r.Status)
	}
	json.NewDecoder(r.Body).Decode(&ans)
	if ans.Report == nil || ans.Report.Matched < 46 || asked != 1 {
		t.Fatalf("report %+v, asked %d", ans.Report, asked)
	}
	http.Post(base+"/crews/roster", "application/x-www-form-urlencoded", nil)
	if asked != 1 {
		t.Error("the roster was downloaded again within the hour")
	}
	// A list for the rest, and an edit by hand.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "officials.csv")
	io.WriteString(fw, "Name,League,Certification\nspike,Rat City Roller Derby,Non-Skating Level 1\n")
	mw.Close()
	r, _ = http.Post(base+"/crews/list", mw.FormDataContentType(), &body)
	ans = CrewsAnswer{}
	json.NewDecoder(r.Body).Decode(&ans)
	if ans.Report == nil || ans.Report.Matched != 1 {
		t.Fatalf("list: %+v", ans.Report)
	}
	cs := ans.Crews
	cs[0].Officials[0].League = "Edited League"
	cs[0].Officials = append(cs[0].Officials, crews.Official{Name: " ", Role: "Jam Timer"}) // an empty row: dropped
	b, _ := json.Marshal(cs)
	req, _ := http.NewRequest("PUT", base+"/crews", bytes.NewReader(b))
	r, _ = http.DefaultClient.Do(req)
	if r.StatusCode != 200 {
		t.Fatalf("put: %s", r.Status)
	}
	// Game 1's crew in the game file, with the league from the roster.
	var v EventView
	rr, _ := http.Get(base + "/data.json")
	json.NewDecoder(rr.Body).Decode(&v)
	g1 := v.Games[0]
	sum, err := ev.Build(site.v, 1, g1.Default, "")
	if err != nil {
		t.Fatal(err)
	}
	withLeague, edited := 0, false
	for _, o := range sum.Officials {
		if o.League != "" && o.Cert != "" {
			withLeague++
		}
		edited = edited || o.League == "Edited League"
	}
	// Crew Divide by Zero: 9 of its 17 are on the roster.
	if withLeague < 9 || !edited || g1.Default.Crew != cs[0].ID {
		t.Errorf("game 1 (crew %s): %d of %d officials with league and certification, edit there: %v", g1.Default.Crew, withLeague, len(sum.Officials), edited)
	}
	// A position that isn't on the IGRF is refused.
	cs[0].Officials[0].Role = "Mascot"
	b, _ = json.Marshal(cs)
	req, _ = http.NewRequest("PUT", base+"/crews", bytes.NewReader(b))
	if r, _ = http.DefaultClient.Do(req); r.StatusCode != http.StatusBadRequest {
		t.Errorf("a mascot: %s", r.Status)
	}
}

// A team's own link: its games, a line-up per game (skaters, captain;
// bench and colour for the home team) that goes on the IGRF; a new link
// shuts the old one out; links outlive a restart and stay out of the logs.
func TestTeamLinks(t *testing.T) {
	app, err := os.ReadFile(filepath.Join(testData, "sanctioning/2026-wftda-championships.xlsx"))
	if err != nil {
		t.Skip("no Championships application")
	}
	logDir, data := t.TempDir(), t.TempDir()
	lg, _ := logs.Open(logDir, nil)
	site, _ := New(Config{Blank: filepath.Join(testData, "statsbooks/template"), Data: data, Fetch: localSheets, Logs: lg})
	ts := httptest.NewServer(site.Handler())
	defer ts.Close()
	ev, err := fixture.Load(app, nil, localSheets, fixture.Options{})
	if err != nil {
		t.Fatal(err)
	}
	site.events[ev.ID] = ev
	site.linked(ev)
	base := ts.URL + "/t/" + ev.ID
	do := func(method, path string, body any) (int, []byte) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, path, rd)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, b
	}
	var v EventView
	_, b := do("GET", base+"/data.json", nil)
	json.Unmarshal(b, &v)
	g1 := v.Games[0]
	home, away := v.Teams[g1.Choice.Teams[0]-1], v.Teams[g1.Choice.Teams[1]-1]
	if home.No != g1.Choice.Teams[0] || !strings.HasPrefix(home.Link, "/team/") || away.Link == "" || home.Link == away.Link {
		t.Fatalf("links: %q %q", home.Link, away.Link)
	}
	if code, page := do("GET", ts.URL+home.Link, nil); code != 200 || !strings.Contains(string(page), home.Name) || strings.Contains(string(page), ev.ID) {
		t.Fatalf("team page: %d", code)
	}
	var tp TeamPageView
	_, b = do("GET", ts.URL+home.Link+"/data.json", nil)
	json.Unmarshal(b, &tp)
	if len(tp.Games) == 0 || tp.Games[0].No != g1.No || !tp.Games[0].Home || len(tp.Skaters) < 16 || strings.Contains(string(b), ev.ID) {
		t.Fatalf("team data: %d games, %d skaters", len(tp.Games), len(tp.Skaters))
	}
	var numbers []string
	for _, s := range tp.Skaters {
		numbers = append(numbers, s.Number)
	}
	game1 := ts.URL + home.Link + "/game/" + itoa(g1.No)
	// Line-ups close two hours before the game, by the venue's clock
	// (Malmö: Europe/Stockholm, guessed from the country).
	start, _ := ev.Start(g1.No)
	if v.Settings.TimeZone != "Europe/Stockholm" || !v.Settings.ZoneGuessed || start.Format(time.RFC3339) != "2026-10-15T10:00:00+02:00" {
		t.Errorf("game 1 starts %s, zone %+v", start, v.Settings)
	}
	if st := ev.LineupState(g1.No, start.Add(-119*time.Minute)); st.Open || !st.Closes.Equal(start.Add(-2*time.Hour)) {
		t.Errorf("after the deadline: %+v", st)
	}
	if st := ev.LineupState(g1.No, start.Add(-3*time.Hour)); !st.Open {
		t.Errorf("before the deadline: %+v", st)
	}
	// The organisers close it, and open it whatever the deadline.
	mode := func(m string) {
		if code, msg := do("PUT", base+"/games/"+itoa(g1.No)+"/lineups", map[string]string{"mode": m}); code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", m, code, msg)
		}
	}
	mode("closed")
	if code, msg := do("PUT", game1, fixture.Lineup{Skaters: numbers[:15]}); code != http.StatusUnprocessableEntity || !strings.Contains(string(msg), "closed") {
		t.Errorf("closed: %d %s", code, msg)
	}
	mode("open")
	for _, bad := range []fixture.Lineup{
		{Skaters: numbers[:16]},
		{Skaters: numbers[:15], Captain: numbers[15]},
		{Skaters: []string{"not a number"}},
		{Skaters: numbers[:15], Bench: "Upside down"},
	} {
		if code, msg := do("PUT", game1, bad); code != http.StatusUnprocessableEntity {
			t.Errorf("%+v: %d %s", bad, code, msg)
		}
	}
	if code, msg := do("PUT", game1, fixture.Lineup{Skaters: numbers[:15], Captain: numbers[3], Bench: "Pivot line", Color: "Teal"}); code != 200 {
		t.Fatalf("home line-up: %d %s", code, msg)
	}
	// The away team's bench and colour aren't theirs to pick.
	var al fixture.Lineup
	_, b = do("PUT", ts.URL+away.Link+"/game/"+itoa(g1.No), fixture.Lineup{Skaters: []string{}, Bench: "Jam line", Color: "Pink"})
	if json.Unmarshal(b, &al); al.Bench != "" || al.Color != "" {
		t.Errorf("away line-up: %s", b)
	}
	// Not their game.
	if code, _ := do("PUT", ts.URL+home.Link+"/game/"+itoa(v.Games[len(v.Games)-1].No), fixture.Lineup{}); code != http.StatusUnprocessableEntity {
		t.Errorf("a game the team doesn't play: %d", code)
	}
	// On the organisers' page, and in the game.
	_, b = do("GET", base+"/data.json", nil)
	json.Unmarshal(b, &v)
	g1 = v.Games[0]
	if l := g1.Lineups[0]; l == nil || l.Skaters != 15 || l.Bench != "Pivot line" || !strings.Contains(l.Captain, "#"+numbers[3]) || g1.Choice.Colors[0] != "Teal" ||
		g1.Benches != [2]string{"Pivot line", "Jam line"} || g1.Lineup.Mode != "open" {
		t.Errorf("organisers see %+v, colours %q, benches %q, line-ups %+v", g1.Lineups[0], g1.Choice.Colors, g1.Benches, g1.Lineup)
	}
	sum, err := ev.Build(site.v, g1.No, g1.Choice, "")
	if err != nil {
		t.Fatal(err)
	}
	// Every skater of the charter, the ones not playing Not Skating.
	captains, notSkating := 0, 0
	for _, sk := range sum.Teams[0].Skaters {
		if strings.Contains(" "+sk.Flags+" ", " C ") {
			captains++
		}
		if strings.Contains(" "+sk.Flags+" ", " ALT ") {
			notSkating++
		}
	}
	if len(sum.Teams[0].Skaters) != len(numbers) || notSkating != len(numbers)-15 || captains != 1 || sum.Teams[0].UniformColor != "Teal" {
		t.Errorf("game 1: %d home skaters, %d not skating, %d captains, colour %q", len(sum.Teams[0].Skaters), notSkating, captains, sum.Teams[0].UniformColor)
	}
	// On the IGRF: all of them, the ones not playing with an asterisk and
	// crossed out, as finished statsbooks have it.
	_, sb := do("GET", base+"/game/"+itoa(g1.No)+"/statsbook.xlsx", nil)
	f, err := excelize.OpenReader(bytes.NewReader(sb))
	if err != nil {
		t.Fatalf("statsbook: %v (%.200s)", err, sb)
	}
	struck, plain := 0, 0
	for _, sk := range tp.Skaters {
		cells, _ := f.SearchSheet("IGRF", "^"+regexp.QuoteMeta(sk.Name)+"$", true)
		if len(cells) == 0 {
			t.Errorf("%s isn't on the IGRF", sk.Name)
			continue
		}
		id, _ := f.GetCellStyle("IGRF", cells[0])
		col, row, _ := excelize.CellNameToCoordinates(cells[0])
		numCell, _ := excelize.CoordinatesToCellName(col-1, row)
		num, _ := f.GetCellValue("IGRF", numCell)
		st, _ := f.GetStyle(id)
		if st != nil && st.Font != nil && st.Font.Strike {
			struck++
			if num != sk.Number+"*" {
				t.Errorf("%s crossed out as %q, want %q", sk.Name, num, sk.Number+"*")
			}
		} else {
			plain++
			if num != sk.Number {
				t.Errorf("%s playing as %q", sk.Name, num)
			}
		}
	}
	if plain != 15 || struck != len(tp.Skaters)-15 {
		t.Errorf("IGRF: %d skaters crossed out, %d not", struck, plain)
	}
	// Benches renamed: the home team's pick goes along.
	if code, msg := do("PUT", base+"/settings", SettingsView{Benches: [2]string{"Window side", "Wall side"}, CloseHours: 1.5, TimeZone: "Europe/Stockholm"}); code != 200 {
		t.Fatalf("settings: %d %s", code, msg)
	}
	if l := ev.Lineup(g1.No, home.No); l.Bench != "Window side" || ev.CloseBefore() != 90*time.Minute {
		t.Errorf("after renaming: bench %q, close %s before", l.Bench, ev.CloseBefore())
	}
	if code, _ := do("PUT", base+"/settings", SettingsView{Benches: [2]string{"A", "a"}}); code != http.StatusUnprocessableEntity {
		t.Errorf("two benches the same: %d", code)
	}
	// The organiser's colour wins over the team's.
	do("PUT", base+"/games/"+itoa(g1.No), fixture.Choice{Teams: g1.Choice.Teams, Colors: [2]string{"Gold", ""}})
	_, b = do("GET", base+"/data.json", nil)
	json.Unmarshal(b, &v)
	if v.Games[0].Choice.Colors[0] != "Gold" {
		t.Errorf("organiser's colour: %q", v.Games[0].Choice.Colors)
	}
	// Two organisers each change one thing of a game: neither undoes the
	// other's, and the crew stays as it was.
	crew := v.Games[0].Choice.Crew
	gold, navy, none := "Gold", "Navy", ""
	for _, p := range []fixture.ChoicePatch{{Side: 1, Color: &navy}, {Side: 0, Color: &gold}} {
		if code, msg := do("PATCH", base+"/games/"+itoa(g1.No), p); code != 200 {
			t.Fatalf("patch: %d %s", code, msg)
		}
	}
	_, b = do("GET", base+"/data.json", nil)
	json.Unmarshal(b, &v)
	if ch := v.Games[0].Choice; ch.Colors != [2]string{"Gold", "Navy"} || ch.Crew != crew || ch.Teams != g1.Choice.Teams {
		t.Errorf("after two patches: %+v, crew was %q", ch, crew)
	}
	if code, _ := do("PATCH", base+"/games/"+itoa(g1.No), fixture.ChoicePatch{Crew: &[]string{"no such crew"}[0]}); code != http.StatusUnprocessableEntity {
		t.Errorf("an unknown crew: %d", code)
	}
	do("PATCH", base+"/games/"+itoa(g1.No), fixture.ChoicePatch{Side: 1, Color: &none})
	// A bracket game shows up once the organisers pick the team for it.
	final := v.Games[len(v.Games)-1]
	do("PUT", base+"/games/"+itoa(final.No), fixture.Choice{Teams: [2]int{away.No, home.No}})
	tp = TeamPageView{}
	_, b = do("GET", ts.URL+home.Link+"/data.json", nil)
	json.Unmarshal(b, &tp)
	if last := tp.Games[len(tp.Games)-1]; last.No != final.No || last.Home || last.Opponent != away.Name {
		t.Errorf("the final on the team's page: %+v", last)
	}
	// Kept: a new site on the same folder knows the link.
	// Saved whole: no file halfway left behind, and what a crash left
	// halfway is cleared on the next start.
	if left, _ := filepath.Glob(filepath.Join(data, "*.tmp")); len(left) > 0 {
		t.Errorf("left after saving: %v", left)
	}
	stray := filepath.Join(data, ev.ID+".123.tmp")
	os.WriteFile(stray, []byte(`{"id":`), 0o644)
	site2, _ := New(Config{Data: data, Fetch: localSheets})
	if _, err := os.Stat(stray); err == nil {
		t.Error("a file a crash left halfway is still there")
	}
	if ev2, team := site2.byToken(strings.TrimPrefix(home.Link, "/team/")); ev2 == nil || team != home.No || ev2.Lineup(g1.No, home.No) == nil {
		t.Error("the link or the line-up wasn't kept")
	}
	// A new link: the old one stops working.
	var nl struct{ Link string }
	_, b = do("POST", base+"/teams/"+itoa(home.No)+"/link", nil)
	json.Unmarshal(b, &nl)
	if code, _ := do("GET", ts.URL+home.Link+"/data.json", nil); code != http.StatusNotFound {
		t.Errorf("old link: %d", code)
	}
	if code, _ := do("GET", ts.URL+nl.Link+"/data.json", nil); code != 200 {
		t.Errorf("new link: %d", code)
	}
	all, _ := os.ReadFile(filepath.Join(logDir, "rtfm.log"))
	own, _ := os.ReadFile(filepath.Join(logDir, ev.ID+".log"))
	if strings.Contains(string(all), strings.TrimPrefix(home.Link, "/team/")) || !strings.Contains(string(own), "line-up: 15 skaters") ||
		!strings.Contains(string(own), "GET /team/<link>/data.json 200") {
		t.Errorf("logs:\n%s", own)
	}
}
