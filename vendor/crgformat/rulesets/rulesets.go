// Package rulesets holds the rulesets a new game can start from: the Java
// scoreboard's presets (RulesetsImpl.java), written as WFTDA plus overrides
// so every game file says all its rules.
package rulesets

import (
	"crgformat/engine"
	"crgformat/legacy"
	"crgformat/replay"
)

// Version is the rule definitions' version.
const Version = "v2025.10"

// Preset is a ruleset to start a game with.
type Preset struct {
	Name      string         `json:"name"`
	Overrides map[string]any `json:"overrides"`
	// Unsupported lists rules the engine can't run yet (docs/engine.md
	// "Rules"); a game with them on can't start.
	Unsupported []string `json:"unsupported,omitempty"`
	// Custom: made here (a Custom in the Store), with its id.
	Custom      bool   `json:"custom,omitempty"`
	ID          string `json:"id,omitempty"`
	Description string `json:"description,omitempty"`
}

// Presets are Java's presets.
var Presets = []Preset{
	{Name: "WFTDA", Overrides: map[string]any{}},
	{Name: "JRDA", Overrides: map[string]any{"Jam.SuddenScoring": true, "Jam.InjuryContinuation": true}},
	{Name: "Sevens", Overrides: map[string]any{"Intermission.Durations": "60:00", "Penalties.NumberToFoulout": 4,
		"Period.Duration": "21:00", "Period.Number": 1, "Team.OfficialReviews": 0, "Team.Timeouts": 0}},
	{Name: "RDCL", Overrides: map[string]any{"Intermission.Durations": "5:00,15:00,5:00,60:00", "Jam.Duration": "1:00",
		"Jam.ResetNumberEachPeriod": false, "Penalties.DefinitionFile": "/config/penalties/RDCL.json", "Period.Duration": "15:00",
		"Period.EndBetweenJams": false, "Period.Number": 4, "Team.RDCLPerHalfRules": true, "Score.WftdaLateChangeRule": false}},
	{Name: "RDCL half game", Overrides: map[string]any{"Intermission.Durations": "5:00,60:00", "Jam.Duration": "1:00",
		"Jam.ResetNumberEachPeriod": false, "Penalties.DefinitionFile": "/config/penalties/RDCL.json", "Period.Duration": "15:00",
		"Period.EndBetweenJams": false, "Period.Number": 2, "Team.RDCLPerHalfRules": true, "Score.WftdaLateChangeRule": false,
		"Penalties.NumberToFoulout": 4, "Team.Timeouts": 1, "Team.TimeoutsPer": true}},
	{Name: "Short Track", Overrides: map[string]any{"Period.JamsPer": 10, "Jam.Duration": "1:00", "Team.Timeouts": 1,
		"Team.OfficialReviews": 0, "Penalties.PointsDeduction": 2, "Penalties.Duration": "0:00", "Lineup.OvertimeDuration": "0:30",
		"Score.PointsOnInitial": true, "Score.WftdaLateChangeRule": false}},
}

func init() {
	for i := range Presets {
		s := &replay.Summary{Ruleset: replay.Ruleset{Overrides: Presets[i].Overrides}}
		Presets[i].Unsupported = engine.Unsupported(s)
	}
}

// Ruleset returns a preset as a game's ruleset.
func Ruleset(p Preset) map[string]any {
	ov := map[string]any{}
	for k, v := range p.Overrides {
		ov[k] = v
	}
	return map[string]any{"base": "WFTDARuleset", "baseVersion": Version, "name": p.Name, "overrides": ov}
}

// Definitions are the rules with their kind and WFTDA default.
func Definitions() []legacy.RuleDefinition { return legacy.RuleDefinitions(Version) }
