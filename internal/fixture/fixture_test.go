package fixture

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"crgformat"
)

// localSheets serves Google Sheets from ../../../sanctioning/charters, saved
// there by their id.
func localSheets(url string) ([]byte, error) {
	id := SheetID(url)
	if id == "" {
		return nil, errors.New("not a sheet link")
	}
	return os.ReadFile(filepath.Join("../../../sanctioning/charters", id+".xlsx"))
}

func champs(t *testing.T) *Event {
	t.Helper()
	app, err := os.ReadFile("../../../sanctioning/2026-wftda-championships.xlsx")
	if err != nil {
		t.Skip("no Championships application in ../../../sanctioning")
	}
	info, _ := os.ReadFile("../../../infopacks/champs-2026.xlsx")
	ev, err := Load(app, info, localSheets, Options{Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

// The 2026 Championships: 16 teams with charters, 23 games, the later ones
// "Winner Game N"; the infopack's crews for the first games.
func TestChampionships(t *testing.T) {
	ev := champs(t)
	if len(ev.Teams) != 16 || len(ev.Missing) != 0 {
		t.Fatalf("%d charters, missing %v", len(ev.Teams), ev.Missing)
	}
	games := ev.Games()
	if len(games) != 23 {
		t.Fatalf("%d games", len(games))
	}
	g1 := games[0]
	if !g1.Fixed[0] || !g1.Fixed[1] || g1.Default.Crew == "" {
		t.Errorf("game 1 %+v", g1)
	}
	last := games[len(games)-1]
	if last.Fixed[0] || last.Fixed[1] {
		t.Errorf("the final has fixed teams: %+v", last.Sides)
	}
	v, err := crgformat.NewValidators()
	if err != nil {
		t.Fatal(err)
	}
	sum, err := ev.Build(v, g1.No, g1.Default)
	if err != nil {
		t.Fatal(err)
	}
	if sum.State != "prepared" || len(sum.Teams) != 2 || len(sum.Teams[0].Skaters) == 0 || len(sum.Officials) == 0 ||
		sum.Info["Tournament"] == "" || sum.Info["HeadReferee"] == "" {
		t.Errorf("game 1: %s, %d officials, info %v", sum.State, len(sum.Officials), sum.Info)
	}
	java, err := JavaGames(sum)
	if err != nil {
		t.Fatal(err)
	}
	if dir := os.Getenv("RTFM_OUT"); dir != "" {
		os.WriteFile(filepath.Join(dir, "game1.json"), java, 0o644)
		if blank, err := os.ReadFile("../../../statsbooks/template/wftda-statsbook-full-A4.xlsx"); err == nil {
			sb, err := Statsbook(sum, blank)
			if err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(dir, FileName(sum)+".xlsx"), sb, 0o644)
		}
	}
	// A bracket game with teams picked.
	ch := last.Default
	ch.Teams = [2]int{1, 2}
	if _, err := ev.Build(v, last.No, ch); err != nil {
		t.Errorf("final: %v", err)
	}
}
