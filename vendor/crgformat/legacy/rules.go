package legacy

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type ruleDef struct {
	Kind    string // integer, long, boolean, string, time
	Default string // as the Java version writes it in a game file
}

// ruleDefaults maps a scoreboard version to its rule defaults. Rule.java is
// identical in v2025.9, v2025.10 and v2027.1 (checked with git diff), so they
// share a table. v2025.6 lacks five rules added later (Period.JamsPer,
// Lineup.StopsPeriodClock, Score.PointsOnInitial, Score.EnforceTimeToOr,
// Penalties.PointsDeduction); the defaults of the others are the same. A
// version not listed here is refused rather than guessed: see README
// "Rulesets".
var ruleDefaults = map[string]map[string]ruleDef{
	"v2025.6":  defaults2025_6,
	"v2025.9":  defaults2025_10,
	"v2025.10": defaults2025_10,
	"v2027.1":  defaults2025_10,
}

// overrides returns the rules whose value differs from the version's
// defaults, typed as the spec wants them (numbers, booleans, strings).
func overrides(version string, rules map[string]string) (map[string]any, error) {
	defs, ok := ruleDefaults[version]
	if !ok {
		return nil, fmt.Errorf("no rule defaults known for scoreboard version %q", version)
	}
	out := map[string]any{}
	for name, val := range rules {
		def, ok := defs[name]
		if !ok {
			return nil, fmt.Errorf("rule %q is not a %s rule", name, version)
		}
		if strings.EqualFold(val, def.Default) {
			continue
		}
		switch def.Kind {
		case "integer", "long":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("rule %s: %q is not a number", name, val)
			}
			out[name] = n
		case "boolean":
			b, err := strconv.ParseBool(val)
			if err != nil {
				return nil, fmt.Errorf("rule %s: %q is not true/false", name, val)
			}
			out[name] = b
		default:
			out[name] = val
		}
	}
	return out, nil
}

// ruleValue returns a rule's effective value.
func ruleValue(version string, rules map[string]string, name string) string {
	if v, ok := rules[name]; ok {
		return v
	}
	return ruleDefaults[version][name].Default
}

// parseClock turns "30:00" or "1:02:03" into milliseconds.
func parseClock(s string) (int64, error) {
	var seconds float64
	for _, part := range strings.Split(s, ":") {
		n, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0, fmt.Errorf("bad time %q", s)
		}
		seconds = seconds*60 + n
	}
	return int64(seconds*1000 + 0.5), nil
}

// RuleDefaults returns the Java rule defaults of a scoreboard version as
// strings, the way the Java scoreboard shows them (Rule(name) keys).
func RuleDefaults(version string) map[string]string {
	out := map[string]string{}
	for name, d := range ruleDefaults[version] {
		out[name] = d.Default
	}
	return out
}

// RuleDefinition is a rule as Rule.java defines it.
type RuleDefinition struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // integer, long, boolean, string, time
	Default string `json:"default"`
}

// RuleDefinitions lists the rules of a scoreboard version, sorted by name.
func RuleDefinitions(version string) []RuleDefinition {
	var out []RuleDefinition
	for name, d := range ruleDefaults[version] {
		out = append(out, RuleDefinition{Name: name, Kind: d.Kind, Default: d.Default})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
