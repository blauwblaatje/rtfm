// Package library is the team library: teams kept between games (the Java
// scoreboard's prepared teams), so a new game starts with its rosters filled
// in. A team comes from an earlier game, from a statsbook's IGRF, or is typed
// in; a game made from it records its id as the team's preparedTeam.
//
// A library team is a starting point, not a link: changing it later doesn't
// change games already made from it, and the game's roster is changed in the
// game. Each team is a JSON file in the library folder.
package library

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"crgformat/derive"
	"crgformat/replay"
)

// Team is a team in the library.
type Team struct {
	ID string `json:"id"`
	// Name is the name on the scoreboard; the other names are as in TeamSet.
	Name                 string `json:"name"`
	FullName             string `json:"fullName,omitempty"`
	League               string `json:"league,omitempty"`
	TeamName             string `json:"teamName,omitempty"`
	Initials             string `json:"initials,omitempty"`
	NameCue              string `json:"nameCue,omitempty"`
	NameCuePronunciation string `json:"nameCuePronunciation,omitempty"`
	// UniformColors are the team's kits; a game picks one.
	UniformColors  []string          `json:"uniformColors,omitempty"`
	Colors         map[string]string `json:"colors,omitempty"`
	AlternateNames map[string]string `json:"alternateNames,omitempty"`
	Logo           string            `json:"logo,omitempty"`
	Skaters        []Skater          `json:"skaters"`
	Staff          []Staff           `json:"staff,omitempty"`
	Charter        *Charter          `json:"charter,omitempty"` // when read from a WFTDA charter
	// Source says where the team came from: "game <id>", "statsbook <file>".
	Source  string `json:"source,omitempty"`
	Updated string `json:"updated,omitempty"` // RFC 3339, set on save
}

// Skater is a skater on a library team.
type Skater struct {
	Number   string `json:"number"`
	Name     string `json:"name,omitempty"`
	Pronouns string `json:"pronouns,omitempty"`
	Flags    string `json:"flags,omitempty"` // C, A, BC, BA
	// From a charter: how to say the skater name (for announcers), and the
	// WFTDA unique skater id, the same skater across teams and years.
	Pronunciation string `json:"pronunciation,omitempty"`
	WUID          string `json:"wuid,omitempty"`
}

// Staff is bench staff on a library team.
type Staff struct {
	Name  string `json:"name"`
	Role  string `json:"role,omitempty"`
	Flags string `json:"flags,omitempty"`
}

// Entry is a team in the list.
type Entry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	League   string `json:"league,omitempty"`
	TeamName string `json:"teamName,omitempty"`
	Skaters  int    `json:"skaters"`
	Source   string `json:"source,omitempty"`
	Updated  string `json:"updated,omitempty"`
}

// Library is a folder of teams.
type Library struct {
	dir string
	mu  sync.Mutex
}

// Open opens (and makes) the library folder.
func Open(dir string) (*Library, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Library{dir: dir}, nil
}

// ErrNotFound is returned for a team that isn't in the library.
var ErrNotFound = errors.New("no such team in the library")

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (l *Library) path(id string) (string, error) {
	if !validID.MatchString(id) {
		return "", fmt.Errorf("invalid team id %q", id)
	}
	return filepath.Join(l.dir, id+".json"), nil
}

// List is every team, by name.
func (l *Library) List() ([]Entry, error) {
	files, err := filepath.Glob(filepath.Join(l.dir, "*.json"))
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	for _, f := range files {
		t, err := l.Get(strings.TrimSuffix(filepath.Base(f), ".json"))
		if err != nil {
			continue
		}
		out = append(out, Entry{ID: t.ID, Name: t.Name, League: t.League, TeamName: t.TeamName,
			Skaters: len(t.Skaters), Source: t.Source, Updated: t.Updated})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Get reads a team.
func (l *Library) Get(id string) (*Team, error) {
	p, err := l.path(id)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var t Team
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("team %s: %w", id, err)
	}
	t.ID = id
	if t.Skaters == nil {
		t.Skaters = []Skater{}
	}
	return &t, nil
}

// Put saves a team: a new one gets an id from its name.
func (l *Library) Put(t *Team) error {
	if err := t.Check(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if t.ID == "" {
		t.ID = l.newID(t)
	}
	p, err := l.path(t.ID)
	if err != nil {
		return err
	}
	t.Updated = time.Now().UTC().Format(time.RFC3339)
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Delete removes a team.
func (l *Library) Delete(id string) error {
	p, err := l.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else {
		return err
	}
}

// newID is a readable id from the team's name that isn't taken yet.
func (l *Library) newID(t *Team) string {
	base := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(t.label()), "-"), "-")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	if base == "" {
		base = "team"
	}
	for i := 1; ; i++ {
		id := base
		if i > 1 {
			id = fmt.Sprintf("%s-%d", base, i)
		}
		if i > 1000 {
			b := make([]byte, 4)
			rand.Read(b)
			id = base + "-" + hex.EncodeToString(b)
		}
		if _, err := os.Stat(filepath.Join(l.dir, id+".json")); errors.Is(err, os.ErrNotExist) {
			return id
		}
	}
}

func (t *Team) label() string {
	if s := strings.TrimSpace(strings.Join([]string{t.League, t.TeamName}, " ")); s != "" {
		return s
	}
	return t.Name
}

// Check says what's wrong with a team: it needs a name, and every skater a
// number no one else on the team has. Anything else is up to the user.
func (t *Team) Check() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("a team needs a name")
	}
	seen := map[string]bool{}
	for _, s := range t.Skaters {
		n, notSkating, err := derive.SkaterNumber(s.Number)
		switch {
		case strings.TrimSpace(s.Number) == "":
			return fmt.Errorf("skater %q has no number", s.Name)
		case err != nil:
			return err
		case notSkating:
			return fmt.Errorf("skater number %q: Not Skating (*) is for a game's roster, not the team library", s.Number)
		}
		if seen[n] {
			return fmt.Errorf("two skaters with number %s", n)
		}
		seen[n] = true
	}
	return nil
}

// FromGame is a game's team as a library team: its names, colours and
// roster. What only holds for that game stays behind: Not Skating flags and
// eligibility.
func FromGame(t *replay.Team, gameID string) *Team {
	out := &Team{ID: t.PreparedTeam, Name: t.Name, FullName: t.FullName, League: t.League, TeamName: t.TeamName,
		Initials: t.Initials, NameCue: t.NameCue, NameCuePronunciation: t.NameCuePron,
		Colors: t.Colors, AlternateNames: t.AlternateNames, Logo: t.Logo,
		Skaters: []Skater{}, Source: "game " + gameID}
	if t.UniformColor != "" {
		out.UniformColors = []string{t.UniformColor}
	}
	for _, s := range t.Skaters {
		out.Skaters = append(out.Skaters, Skater{Number: s.Number, Name: s.Name, Pronouns: s.Pronouns, Flags: rosterFlags(s.Flags),
			Pronunciation: s.Pronunciation, WUID: s.WUID})
	}
	for _, s := range t.Staff {
		out.Staff = append(out.Staff, Staff{Name: s.Name, Role: s.Role, Flags: rosterFlags(s.Flags)})
	}
	return out
}

// rosterFlags drops ALT (not skating in that game) from a flags list.
func rosterFlags(flags string) string {
	var keep []string
	for _, f := range strings.Fields(flags) {
		if f != "ALT" {
			keep = append(keep, f)
		}
	}
	return strings.Join(keep, " ")
}

// Merge keeps what the library team has that the game's team doesn't:
// its other uniform colours, and a name or cue left empty in the game.
// Saving a game's team back to the library uses it so another kit or a
// pronunciation isn't lost.
func (t *Team) Merge(old *Team) {
	for _, c := range old.UniformColors {
		found := false
		for _, x := range t.UniformColors {
			found = found || strings.EqualFold(x, c)
		}
		if !found {
			t.UniformColors = append(t.UniformColors, c)
		}
	}
	for _, f := range []struct {
		dst *string
		src string
	}{
		{&t.FullName, old.FullName}, {&t.League, old.League}, {&t.TeamName, old.TeamName}, {&t.Initials, old.Initials},
		{&t.NameCue, old.NameCue}, {&t.NameCuePronunciation, old.NameCuePronunciation}, {&t.Logo, old.Logo},
	} {
		if *f.dst == "" {
			*f.dst = f.src
		}
	}
}

// GameTeam is the team as a new game's team (server NewGame): TeamSet
// fields with "skaters" and "staff", the uniform colour being the first kit
// unless uniformColor picks another.
func (t *Team) GameTeam(uniformColor string) map[string]any {
	g := map[string]any{"preparedTeam": t.ID, "name": t.Name}
	for k, v := range map[string]string{"fullName": t.FullName, "league": t.League, "teamName": t.TeamName,
		"initials": t.Initials, "nameCue": t.NameCue, "nameCuePronunciation": t.NameCuePronunciation, "logo": t.Logo} {
		if v != "" {
			g[k] = v
		}
	}
	if uniformColor == "" && len(t.UniformColors) > 0 {
		uniformColor = t.UniformColors[0]
	}
	if uniformColor != "" {
		g["uniformColor"] = uniformColor
	}
	if len(t.Colors) > 0 {
		g["colors"] = t.Colors
	}
	if len(t.AlternateNames) > 0 {
		g["alternateNames"] = t.AlternateNames
	}
	skaters := []any{}
	for _, s := range t.Skaters {
		m := map[string]any{"number": strings.TrimSpace(s.Number)}
		if s.Name != "" {
			m["name"] = s.Name
		}
		if s.Pronouns != "" {
			m["pronouns"] = s.Pronouns
		}
		if s.Flags != "" {
			m["flags"] = s.Flags
		}
		if s.Pronunciation != "" {
			m["pronunciation"] = s.Pronunciation
		}
		if s.WUID != "" {
			m["wuid"] = s.WUID
		}
		skaters = append(skaters, m)
	}
	g["skaters"] = skaters
	if len(t.Staff) > 0 {
		staff := []any{}
		for _, s := range t.Staff {
			m := map[string]any{"name": s.Name}
			if s.Role != "" {
				m["role"] = s.Role
			}
			if s.Flags != "" {
				m["flags"] = s.Flags
			}
			staff = append(staff, m)
		}
		g["staff"] = staff
	}
	return g
}

// Find is the library team with this league and team name (ignoring case
// and spaces), or nil.
func (l *Library) Find(league, teamName string) *Team {
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	list, _ := l.List()
	for _, e := range list {
		if norm(e.League) == norm(league) && norm(e.TeamName) == norm(teamName) && (league != "" || teamName != "") {
			if t, err := l.Get(e.ID); err == nil {
				return t
			}
		}
	}
	return nil
}

// UpdateFrom takes a newer roster (a new charter) into a library team: its
// skaters, charter, source, and any names and kits it has. What the library
// adds stays: a skater's flags (captain) for the same number, the name on the
// scoreboard, and other kits.
func (t *Team) UpdateFrom(n *Team) {
	flags := map[string]string{}
	for _, s := range t.Skaters {
		flags[s.Number] = s.Flags
	}
	t.Skaters = n.Skaters
	for i := range t.Skaters {
		if t.Skaters[i].Flags == "" {
			t.Skaters[i].Flags = flags[t.Skaters[i].Number]
		}
	}
	old := &Team{UniformColors: t.UniformColors}
	t.UniformColors = n.UniformColors
	t.Merge(old)
	for _, f := range []struct {
		dst *string
		src string
	}{{&t.League, n.League}, {&t.TeamName, n.TeamName}, {&t.FullName, n.FullName}} {
		if f.src != "" {
			*f.dst = f.src
		}
	}
	t.Charter, t.Source = n.Charter, n.Source
}
