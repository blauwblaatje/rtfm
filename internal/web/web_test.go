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

	"rtfm/internal/fixture"
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
	site, err := New(Config{Blank: filepath.Join(testData, "statsbooks/template"), Data: t.TempDir(), Fetch: localSheets, Logf: t.Logf})
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
	// Kept: a new site on the same folder still has it.
	site2, _ := New(Config{Data: site.cfg.Data, Fetch: localSheets})
	if site2.event(strings.TrimPrefix(loc, "/t/")) == nil {
		t.Error("the tournament wasn't kept")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
