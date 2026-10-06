// Package crews stores officiating crews, to load a whole crew into a
// game's IGRF at once, and reads them from a tournament's infopack.
package crews

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
)

// Crew is a set of officials who work games together.
type Crew struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Officials []Official `json:"officials"`
	// Games: the games an infopack assigns this crew to.
	Games   []string `json:"games,omitempty"`
	Source  string   `json:"source,omitempty"`
	Updated string   `json:"updated,omitempty"`
}

// Official is one crew member in a position (an IGRF role).
type Official struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	Head     bool   `json:"head,omitempty"` // the crew's head referee or head NSO
	Pronouns string `json:"pronouns,omitempty"`
	League   string `json:"league,omitempty"`
	Cert     string `json:"cert,omitempty"`
}

// Entry is a crew in a listing.
type Entry struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Officials int      `json:"officials"`
	Games     []string `json:"games,omitempty"`
	Source    string   `json:"source,omitempty"`
	Updated   string   `json:"updated,omitempty"`
}

// Store is a folder of crews.
type Store struct {
	dir string
	mu  sync.Mutex
}

// ErrNotFound is returned for a crew that isn't stored.
var ErrNotFound = errors.New("no such crew")

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
		return "", fmt.Errorf("invalid crew id %q", id)
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// List is every crew, by name.
func (s *Store) List() ([]Entry, error) {
	files, err := filepath.Glob(filepath.Join(s.dir, "*.json"))
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	for _, f := range files {
		c, err := s.Get(strings.TrimSuffix(filepath.Base(f), ".json"))
		if err != nil {
			continue
		}
		out = append(out, Entry{ID: c.ID, Name: c.Name, Officials: len(c.Officials), Games: c.Games, Source: c.Source, Updated: c.Updated})
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name); a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// All is every stored crew in full.
func (s *Store) All() []*Crew {
	list, _ := s.List()
	out := []*Crew{}
	for _, e := range list {
		if c, err := s.Get(e.ID); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// Get reads a crew.
func (s *Store) Get(id string) (*Crew, error) {
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
	var c Crew
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("crew %s: %w", id, err)
	}
	c.ID = id
	if c.Officials == nil {
		c.Officials = []Official{}
	}
	return &c, nil
}

// Check says what's wrong with a crew: it needs a name, and every official
// a name and a role.
func (c *Crew) Check() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("a crew needs a name")
	}
	for i, o := range c.Officials {
		if strings.TrimSpace(o.Name) == "" || strings.TrimSpace(o.Role) == "" {
			return fmt.Errorf("official %d needs a name and a role", i+1)
		}
	}
	return nil
}

// Put saves a crew: a new one gets an id from its name.
func (s *Store) Put(c *Crew) error {
	if err := c.Check(); err != nil {
		return err
	}
	if c.Officials == nil {
		c.Officials = []Official{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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

// Delete removes a crew.
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
		base = "crew"
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
		if _, err := os.Stat(filepath.Join(s.dir, id+".json")); errors.Is(err, os.ErrNotExist) {
			return id
		}
	}
}

// Same says whether two crews have the same officials in the same roles.
func Same(a, b *Crew) bool {
	key := func(c *Crew) string {
		var k []string
		for _, o := range c.Officials {
			k = append(k, strings.ToLower(strings.TrimSpace(o.Name))+"\x00"+o.Role)
		}
		sort.Strings(k)
		return strings.Join(k, "\x01")
	}
	return key(a) == key(b)
}
