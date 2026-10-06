package rulesets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"crgformat/engine"
	"crgformat/replay"
)

// Custom is a ruleset made here (MRDWC's short games without clock
// stoppage, say): WFTDA plus overrides, like the presets.
type Custom struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Overrides   map[string]any `json:"overrides"`
	Updated     string         `json:"updated,omitempty"`
}

// Store is a folder of custom rulesets.
type Store struct {
	dir string
	mu  sync.Mutex
}

// ErrNotFound is returned for a ruleset that isn't stored.
var ErrNotFound = errors.New("no such ruleset")

// Open opens (and makes) the folder.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (s *Store) path(id string) (string, error) {
	if !validID.MatchString(id) {
		return "", fmt.Errorf("invalid ruleset id %q", id)
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// List is every custom ruleset, by name.
func (s *Store) List() []*Custom {
	files, _ := filepath.Glob(filepath.Join(s.dir, "*.json"))
	out := []*Custom{}
	for _, f := range files {
		if c, err := s.Get(strings.TrimSuffix(filepath.Base(f), ".json")); err == nil {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Get reads a custom ruleset.
func (s *Store) Get(id string) (*Custom, error) {
	p, err := s.path(id)
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
	var c Custom
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("ruleset %s: %w", id, err)
	}
	c.ID = id
	if c.Overrides == nil {
		c.Overrides = map[string]any{}
	}
	return &c, nil
}

// Put checks and saves a custom ruleset; a new one gets an id from its
// name. Its name can't be a preset's or another custom ruleset's.
func (s *Store) Put(c *Custom) error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return errors.New("a ruleset needs a name")
	}
	for _, p := range Presets {
		if strings.EqualFold(p.Name, c.Name) {
			return fmt.Errorf("%q is a built-in ruleset", p.Name)
		}
	}
	ov, err := Check(c.Overrides)
	if err != nil {
		return err
	}
	c.Overrides = ov
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.List() {
		if x.ID != c.ID && strings.EqualFold(x.Name, c.Name) {
			return fmt.Errorf("there already is a ruleset %q", x.Name)
		}
	}
	if c.ID == "" {
		c.ID = s.newID(c.Name)
	}
	p, err := s.path(c.ID)
	if err != nil {
		return err
	}
	c.Updated = time.Now().UTC().Format(time.RFC3339)
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Delete removes a custom ruleset. Games made with it keep their rules.
func (s *Store) Delete(id string) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else {
		return err
	}
}

func (s *Store) newID(name string) string {
	base := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	if base == "" {
		base = "ruleset"
	}
	for i := 1; ; i++ {
		id := base
		if i > 1 {
			id = fmt.Sprintf("%s-%d", base, i)
		}
		if _, err := os.Stat(filepath.Join(s.dir, id+".json")); errors.Is(err, os.ErrNotExist) {
			return id
		}
	}
}

var timeValue = regexp.MustCompile(`^\d+(:\d{1,2}){0,2}(,\d+(:\d{1,2}){0,2})*$`)

// Check checks overrides against the rule definitions and gives each its
// kind's type (a number for a number, true or false for a boolean); a
// value that is WFTDA's default isn't an override and is left out.
func Check(ov map[string]any) (map[string]any, error) {
	defs := map[string]string{}
	defaults := map[string]string{}
	for _, d := range Definitions() {
		defs[d.Name], defaults[d.Name] = d.Kind, d.Default
	}
	out := map[string]any{}
	for name, v := range ov {
		kind, ok := defs[name]
		if !ok {
			return nil, fmt.Errorf("%s is not a rule", name)
		}
		text := strings.TrimSpace(fmt.Sprint(v))
		if text == defaults[name] {
			continue
		}
		switch kind {
		case "boolean":
			b, err := strconv.ParseBool(text)
			if err != nil {
				return nil, fmt.Errorf("%s: %q isn't true or false", name, text)
			}
			out[name] = b
		case "integer", "long":
			n, err := strconv.ParseFloat(text, 64)
			if err != nil || n != float64(int64(n)) {
				return nil, fmt.Errorf("%s: %q isn't a whole number", name, text)
			}
			out[name] = int64(n)
		case "time":
			if !timeValue.MatchString(text) {
				return nil, fmt.Errorf("%s: %q isn't a time (like 2:00)", name, text)
			}
			out[name] = text
		default:
			out[name] = text
		}
	}
	return out, nil
}

// AsPreset is a custom ruleset as a preset, with what the engine can't run.
func (c *Custom) AsPreset() Preset {
	p := Preset{Name: c.Name, Overrides: c.Overrides, ID: c.ID, Description: c.Description, Custom: true}
	p.Unsupported = engine.Unsupported(&replay.Summary{Ruleset: replay.Ruleset{Overrides: c.Overrides}})
	return p
}
