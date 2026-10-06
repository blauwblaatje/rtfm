package fixture

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"crgformat"
	"crgformat/crews"
)

// WFTDA's roster of certified officials as saved on 2026-10-06 (751).
func TestParseRoster(t *testing.T) {
	page, err := os.ReadFile("testdata/wftda-roster.html")
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseRoster(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(r) != 745 { // 751 entries, 6 names twice
		t.Errorf("%d names", len(r))
	}
	if roger := r[NameKey("Roger")]; roger == nil || !roger.Twice || roger.League != "" {
		t.Errorf("Roger, twice on the roster: %+v", roger)
	}
	if rep := Fill([]*crews.Crew{{Officials: []crews.Official{{Name: "Roger", Role: "Jam Timer"}}}}, r, false); len(rep.Twice) != 1 || rep.Matched != 0 {
		t.Errorf("Roger filled in: %+v", rep)
	}
	a := r[NameKey("Adam Smasher")]
	if a == nil || a.League != "Independent" || a.Pronouns != "he/him" || strings.Join(a.Certs, "|") != "Non-Skating Level 2|Skating Level 2" {
		t.Errorf("Adam Smasher: %+v", a)
	}
	if CertFor("Head Referee", a.Certs) != "Skating Level 2" || CertFor("Jam Timer", a.Certs) != "Non-Skating Level 2" {
		t.Errorf("certs by role: %q %q", CertFor("Head Referee", a.Certs), CertFor("Jam Timer", a.Certs))
	}
	// Only a skating certification: an NSO position gets it anyway.
	if CertFor("Scorekeeper", []string{"Skating Level 1"}) != "Skating Level 1" {
		t.Error("an official without a fitting certification")
	}
}

func TestNameKey(t *testing.T) {
	for _, pair := range [][2]string{{"Dropkick Bru’s", "dropkick brus"}, {"Phenïx", "PHENIX"}, {"Robot'Môx", "robot mox"}} {
		if NameKey(pair[0]) != NameKey(pair[1]) {
			t.Errorf("%q and %q differ", pair[0], pair[1])
		}
	}
}

func TestReadOfficialsList(t *testing.T) {
	csv := "Officials for Champs\nOfficial Role;Official Name;League Affiliation;Certification Level;Legal name\n" +
		"Head Referee;Spike;Rat City;Skating Level 3;Not This\nJam Timer;Phenix;Paris;NSO 2;Nor This\n"
	r, err := ReadOfficialsList([]byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	s := r[NameKey("spike")]
	if len(r) != 2 || s == nil || s.League != "Rat City" || s.Cert != "Skating Level 3" {
		t.Fatalf("list %+v", r)
	}
	for _, c := range r {
		if strings.Contains(c.Name+c.League+c.Cert+c.Pronouns, "Not") {
			t.Errorf("a legal name came along: %+v", c)
		}
	}
	cs := []*crews.Crew{{Name: "A", Officials: []crews.Official{{Name: "Spike", Role: "Head Referee"}, {Name: "Phenïx", Role: "Jam Timer", League: "Kept"},
		{Name: "Nobody", Role: "Scorekeeper"}}}}
	rep := Fill(cs, r, false)
	o := cs[0].Officials
	if o[0].League != "Rat City" || o[0].Cert != "Skating Level 3" || o[1].League != "Kept" || o[1].Cert != "NSO 2" ||
		rep.Matched != 2 || rep.Changed != 2 || len(rep.NotFound) != 1 {
		t.Errorf("fill %+v, report %+v", o, rep)
	}
	if Fill(cs, r, true); cs[0].Officials[1].League != "Paris" {
		t.Error("overwrite kept the old league")
	}
}

// The Championships' crews against the roster.
func TestChampsCrewsFromRoster(t *testing.T) {
	ev := champs(t)
	page, _ := os.ReadFile("testdata/wftda-roster.html")
	r, err := ParseRoster(page)
	if err != nil {
		t.Fatal(err)
	}
	rep := Fill(ev.Crews, r, false)
	t.Logf("%d officials, %d matched, %d changed; not found: %q", rep.Officials, rep.Matched, rep.Changed, rep.NotFound)
	if rep.Matched < 46 {
		t.Errorf("only %d of %d matched", rep.Matched, rep.Officials)
	}
	// Into the game: the CRG file and the IGRF.
	v, err := crgformat.NewValidators()
	if err != nil {
		t.Fatal(err)
	}
	g1 := ev.Games()[0]
	sum, err := ev.Build(v, g1.No, g1.Default, "")
	if err != nil {
		t.Fatal(err)
	}
	java, _ := JavaGames(sum)
	if !strings.Contains(string(java), `"Skating Level 3"`) || !strings.Contains(string(java), `"Perth Roller Derby"`) {
		t.Error("the CRG file has no certification or league")
	}
	blank, err := os.ReadFile("../../blank/wftda-statsbook-full-A4.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	sb, err := Statsbook(sum, blank)
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(sb))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for row := 60; row <= 90; row++ {
		name, _ := f.GetCellValue("IGRF", fmt.Sprintf("C%d", row))
		league, _ := f.GetCellValue("IGRF", fmt.Sprintf("H%d", row))
		cert, _ := f.GetCellValue("IGRF", fmt.Sprintf("K%d", row))
		if name == "Connie" {
			found = league == "Perth Roller Derby" && cert == "Non-Skating Level 3"
		}
	}
	if !found {
		t.Error("the IGRF has no league and certification for Connie (Head NSO)")
	}
}
