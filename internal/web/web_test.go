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
	"strconv"
	"strings"
	"testing"

	"crgformat/crews"

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
	// All: the fixed games, and the final with teams picked.
	choices := map[string]fixture.Choice{}
	for _, g := range v.Games {
		choices[itoa(g.No)] = g.Default
	}
	final := v.Games[len(v.Games)-1]
	choices[itoa(final.No)] = fixture.Choice{Teams: [2]int{1, 2}}
	cj, _ := json.Marshal(choices)
	r, err = http.PostForm(ts.URL+loc+"/all.zip", url.Values{"choices": {string(cj)}, "paper": {v.Blanks[0]}})
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
