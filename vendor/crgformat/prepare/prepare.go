// Package prepare makes a game before it's played: its first events (the
// game, its teams and rosters, its officials, the first jam) and the crew an
// infopack assigned to it. The scoreboard's Start New Game and tournament
// games use it, and so does RTFM, which makes a tournament's games as
// statsbooks and Java game files.
package prepare

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"crgformat"
	"crgformat/crews"
	"crgformat/derive"
	"crgformat/replay"
)

// Game is what a game starts with.
type Game struct {
	ID      string
	Name    string
	Info    map[string]any
	Ruleset map[string]any
	// TeamSet fields plus "skaters": [SkaterAdded fields] and "staff":
	// [StaffAdded fields].
	Teams     []map[string]any
	Officials []map[string]any // OfficialAssigned fields
}

// ErrInput marks what's wrong with the game as given (a skater number), as
// against a log that doesn't validate or replay.
var ErrInput = errors.New("invalid game")

// NewID is a new random id with a prefix ("s_", "o_", …).
func NewID(prefix string) string {
	b := make([]byte, 5)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// Events are a game's first events. Skater numbers are checked
// (derive.SkaterNumber): "12*" is 12, Not Skating.
func Events(g Game) ([]map[string]any, error) {
	created := map[string]any{"type": "GameCreated", "game": g.ID, "ruleset": g.Ruleset}
	if g.Name != "" {
		created["name"] = g.Name
	}
	if len(g.Info) > 0 {
		created["info"] = g.Info
	}
	events := []map[string]any{created}
	for i, t := range g.Teams {
		team := map[string]any{"type": "TeamSet", "team": strconv.Itoa(i + 1)}
		var skaters, staff []any
		for k, v := range t {
			switch k {
			case "skaters":
				skaters = list(v)
			case "staff":
				staff = list(v)
			case "team":
			default:
				team[k] = v
			}
		}
		events = append(events, team)
		for _, sk := range skaters {
			m, _ := sk.(map[string]any)
			e := map[string]any{"type": "SkaterAdded", "team": team["team"], "skater": NewID("s_")}
			for k, v := range m {
				e[k] = v
			}
			raw, _ := e["number"].(string)
			n, notSkating, err := derive.SkaterNumber(raw)
			if err != nil {
				return nil, fmt.Errorf("%w: team %v: %w", ErrInput, team["team"], err)
			}
			e["number"] = n
			if notSkating {
				e["flags"] = "ALT"
			}
			events = append(events, e)
		}
		for _, st := range staff {
			m, _ := st.(map[string]any)
			e := map[string]any{"type": "StaffAdded", "team": team["team"], "staff": NewID("st_")}
			for k, v := range m {
				e[k] = v
			}
			events = append(events, e)
		}
	}
	for _, o := range g.Officials {
		e := map[string]any{"type": "OfficialAssigned", "official": NewID("o_")}
		for k, v := range o {
			e[k] = v
		}
		events = append(events, e)
	}
	// The first jam exists from the start, so its lineup can be entered
	// before the game.
	events = append(events, map[string]any{"type": "JamUpcoming", "jam": NewID("j_"), "number": 1})
	return events, nil
}

// list takes skaters or staff as []any or []map[string]any.
func list(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []map[string]any:
		out := make([]any, len(x))
		for i, m := range x {
			out[i] = m
		}
		return out
	}
	return nil
}

// Log writes events as an event log at time t, checking each against the
// schema and the whole by replaying it.
func Log(v *crgformat.Validators, events []map[string]any, t time.Time) ([]byte, *replay.Summary, error) {
	var buf bytes.Buffer
	now := t.UTC().Format("2006-01-02T15:04:05.000Z")
	for i, e := range events {
		e["v"], e["seq"], e["t"] = 1, i+1, now
		b, err := json.Marshal(e)
		if err != nil {
			return nil, nil, err
		}
		doc, err := crgformat.DecodeJSON(b)
		if err == nil {
			err = v.ValidateEvent(doc)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", e["type"], err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	evs, errs := replay.ReadLog(bytes.NewReader(buf.Bytes()), v)
	if len(errs) > 0 {
		return nil, nil, errs[0]
	}
	out, err := replay.Replay(evs)
	if err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), out.Summary, nil
}

// Build makes a game's log and summary, with a crew's officials if crew
// isn't nil.
func Build(v *crgformat.Validators, g Game, crew *crews.Crew, t time.Time) ([]byte, *replay.Summary, error) {
	events, err := Events(g)
	if err != nil {
		return nil, nil, err
	}
	log, sum, err := Log(v, events, t)
	if err != nil || crew == nil {
		return log, sum, err
	}
	return Log(v, append(events, CrewEvents(sum, crew, false)...), t)
}

// TeamRoles are the positions that belong to one team (the IGRF's P1 team).
var TeamRoles = map[string]bool{"Penalty Lineup Tracker": true, "Penalty Tracker": true, "Scorekeeper": true,
	"Penalty Box Timer": true, "Lineup Tracker": true, "Jammer Referee": true}

// CrewEvents are the events that put a crew into a game. An official the
// game already has in that role isn't added again. Of a pair in a team's
// position (two jammer refs, two scorekeepers…), the first gets the team in
// period 1 and the second the other: change it in the IGRF when it's the
// other way round.
func CrewEvents(sum *replay.Summary, c *crews.Crew, replace bool) []map[string]any {
	var evs []map[string]any
	have := map[string]string{} // name+role -> official id
	if replace {
		for _, o := range sum.Officials {
			evs = append(evs, map[string]any{"type": "OfficialRemoved", "official": o.ID})
		}
	} else {
		for _, o := range sum.Officials {
			have[strings.ToLower(o.Name)+"\x00"+o.Role] = o.ID
		}
	}
	count := map[string]int{}
	for _, o := range c.Officials {
		count[o.Role]++
	}
	seen := map[string]int{}
	info := map[string]any{}
	for _, o := range c.Officials {
		k := strings.ToLower(strings.TrimSpace(o.Name)) + "\x00" + o.Role
		id := have[k]
		if id == "" {
			id = NewID("o_")
			ev := map[string]any{"type": "OfficialAssigned", "official": id, "name": strings.TrimSpace(o.Name), "role": o.Role}
			if o.League != "" {
				ev["league"] = o.League
			}
			if o.Cert != "" {
				ev["cert"] = o.Cert
			}
			if TeamRoles[o.Role] && count[o.Role] == 2 && len(sum.Teams) == 2 {
				ev["p1Team"] = sum.Teams[seen[o.Role]].Team
			}
			evs = append(evs, ev)
			have[k] = id
		}
		seen[o.Role]++
		if o.Head {
			key := "HeadNSO"
			if o.Role == "Head Referee" {
				key = "HeadReferee"
			}
			if replace || sum.Info[key] == "" {
				info[key] = id
			}
		}
	}
	if len(info) > 0 {
		evs = append(evs, map[string]any{"type": "GameInfoUpdated", "info": info})
	}
	return evs
}

// MatchCrew is the crew an infopack assigned to the game between these
// teams: one of its games names both, by any of each team's names. Names
// are compared without spaces and punctuation: "2 x 4" is "2x4".
func MatchCrew(all []*crews.Crew, teams [2][]string) *crews.Crew {
	names := func(g string, list []string) bool {
		for _, n := range list {
			if n = Plain(n); n != "" && strings.Contains(g, n) {
				return true
			}
		}
		return false
	}
	for _, c := range all {
		for _, g := range c.Games {
			g = Plain(g)
			if names(g, teams[0]) && names(g, teams[1]) {
				return c
			}
		}
	}
	return nil
}

// MatchCrewByNumber is the crew an infopack assigned to game number no
// ("Game 3", "G3", "GAME 3 (Semifinal)"): the game's own crew (one named
// after it, or with only that game) before a crew that does several.
func MatchCrewByNumber(all []*crews.Crew, no int) *crews.Crew {
	var several *crews.Crew
	for _, c := range all {
		for _, g := range c.Games {
			if no <= 0 || crews.GameNumber(g) != no {
				continue
			}
			if len(c.Games) == 1 || crews.GameNumber(c.Name) == no {
				return c
			}
			if several == nil {
				several = c
			}
		}
	}
	return several
}

// Plain is a name in lower case without spaces and punctuation.
func Plain(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}
