package fixture

import (
	"os"
	"testing"
)

// Sanctioning applications with their infopacks: how many games get the
// infopack's crew (by team names, "WG1" for a bracket side, or the game's
// number). Champs' infopack only has the first 12 games.
func TestInfopackCrewsForGames(t *testing.T) {
	want := map[string]int{"2026 WFTDA Regonal Championships-Europe": 17, "Holy Duck 2.0: Return Of The Quacken 2024": 7,
		"Brawlcelona 2025": 6, "2026 WFTDA Championships": 12}
	pairs := [][2]string{
		{"1ZANQ3NA0lC6SJnvd6dAsJlAoNNb3Il0KQ1Whld60ZKU", "1LDtJOMR0TO0hem_-OgqV0HbUAak8g6eFcNsvfdr1Yc0"},
		{"1vKPvMX02UAl1imYIrGgMz5ky_rPnCmxSt8WC7A6wwF0", "1ZdFAkDtCIxdmaCyQ8nPqnVB5HadbMAdTWhH6gEp-K78"},
		{"1vKTZUFSlX2LL-dE2iSxYY1U6CwFt_yLHzhYQIRzVOzA", "1DASAcyCfMizcS14_5MP6V4FcBJwYIkTw3_4I4T0-bks"},
		{"2026-wftda-championships", "champs-2026"},
	}
	for _, p := range pairs {
		app, err := os.ReadFile("../../../sanctioning/" + p[0] + ".xlsx")
		if err != nil {
			t.Skip("no sanctioning applications in ../../../sanctioning")
		}
		info, _ := os.ReadFile("../../../infopacks/" + p[1] + ".xlsx")
		ev, err := Load(app, info, localSheets, Options{})
		if err != nil {
			t.Fatal(err)
		}
		with := 0
		for _, g := range ev.Games() {
			c := ev.Crew(g.Default.Crew)
			name := ""
			if c != nil {
				with++
				name = c.Name
			}
			t.Logf("  %s g%d %q v %q -> %q", ev.T.Name, g.No, g.Sides[0], g.Sides[1], name)
		}
		if with != want[ev.T.Name] {
			t.Errorf("%s: %d of %d games with a crew, want %d", ev.T.Name, with, len(ev.T.Games), want[ev.T.Name])
		}
	}
}
