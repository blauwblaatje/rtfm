// Package tournament is the tournament builder: a WFTDA tournament
// sanctioning application read into a tournament (its details, teams with
// their charters, and the schedule), and the bracket worked out from the
// games' results, so "Winner Game 1" becomes a team once game 1 is over.
//
// A tournament refers to games and library teams by id; it holds no game
// data itself. Each tournament is a JSON file in the tournaments folder.
package tournament

import (
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

// Tournament is a tournament.
type Tournament struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Dates      string `json:"dates,omitempty"` // as written on the application
	HostLeague string `json:"hostLeague,omitempty"`
	Venue      Venue  `json:"venue"`
	Teams      []Team `json:"teams"`
	Games      []Game `json:"games"`
	// Ruleset: the preset or custom ruleset (by name) its games start with;
	// WFTDA when empty.
	Ruleset string `json:"ruleset,omitempty"`
	Source  string `json:"source,omitempty"`
	Updated string `json:"updated,omitempty"`
}

// Venue is where the tournament is.
type Venue struct {
	Name       string `json:"name,omitempty"`
	Address    string `json:"address,omitempty"`
	City       string `json:"city,omitempty"`
	PostalCode string `json:"postalCode,omitempty"`
	State      string `json:"state,omitempty"`
	Country    string `json:"country,omitempty"`
}

// Team is a team in the tournament, numbered as on the application.
type Team struct {
	No         int    `json:"no"`
	Name       string `json:"name"` // "League - Team", as written
	CharterURL string `json:"charterUrl,omitempty"`
	// Library is the team library's team for it, once its charter is
	// imported or a team is picked.
	Library string `json:"library,omitempty"`
}

// Game is a game on the schedule.
type Game struct {
	No    int    `json:"no"`
	Date  string `json:"date,omitempty"` // YYYY-MM-DD, when it could be read
	Time  string `json:"time,omitempty"` // HH:MM
	Type  string `json:"type,omitempty"` // "GUR only", "Regional", "Bracket dependent"
	Track string `json:"track,omitempty"`
	Notes string `json:"notes,omitempty"`
	Home  Slot   `json:"home"`
	Away  Slot   `json:"away"`
	// GameID is the scoreboard game, once it's made, and Teams the two
	// teams (home, away) it was made with.
	GameID string `json:"gameId,omitempty"`
	Teams  [2]int `json:"teams,omitempty"`
	// Winner is the organiser's decision (a team number) where the result
	// doesn't decide it: a cancelled game, or a changed bracket. It wins
	// over the game's result.
	Winner int `json:"winner,omitempty"`
}

// Slot is one side of a game: a team, or the winner or loser of an earlier
// game.
type Slot struct {
	Text  string `json:"text"`            // as written on the schedule
	Color string `json:"color,omitempty"` // "Colors (Dark/Light)"
	Team  int    `json:"team,omitempty"`  // the team, when it's a team
	From  *Ref   `json:"from,omitempty"`  // "Winner Game 1"
	// Guess says the team was matched by name and should be checked.
	Guess bool `json:"guess,omitempty"`
	// Override is the organiser's choice of team for this side, whatever
	// the schedule or the bracket says.
	Override int `json:"override,omitempty"`
}

// Ref is the winner or loser of a game.
type Ref struct {
	Game   int  `json:"game"`
	Winner bool `json:"winner"`
}

func (r Ref) String() string {
	if r.Winner {
		return fmt.Sprintf("Winner Game %d", r.Game)
	}
	return fmt.Sprintf("Loser Game %d", r.Game)
}

// Result is how a game ended, in tournament team numbers: 0 means not
// decided (not over yet, cancelled, or a tie).
type Result struct {
	Winner int `json:"winner"`
	Loser  int `json:"loser"`
}

// Side is a slot worked out: the team, or what it waits for.
type Side struct {
	Team       int    `json:"team,omitempty"`
	Waiting    string `json:"waiting,omitempty"`    // "Winner Game 1", or why it's unknown
	Overridden bool   `json:"overridden,omitempty"` // the organiser chose the team
}

// Resolve works out who plays in each game (by game number), given the
// results of the games that are over, including the organiser's decisions.
func (t *Tournament) Resolve(results map[int]Result) map[int][2]Side {
	byNo := map[int]*Game{}
	for i := range t.Games {
		byNo[t.Games[i].No] = &t.Games[i]
	}
	out := map[int][2]Side{}
	var side func(s Slot, depth int) Side
	side = func(s Slot, depth int) Side {
		switch {
		case s.Override > 0:
			return Side{Team: s.Override, Overridden: true}
		case s.Team > 0:
			return Side{Team: s.Team}
		case s.From == nil:
			return Side{Waiting: fmt.Sprintf("%q isn't a team of the tournament: pick one", s.Text)}
		case depth > len(t.Games) || byNo[s.From.Game] == nil:
			return Side{Waiting: s.From.String() + ": there's no such game"}
		}
		r := results[s.From.Game]
		if g := byNo[s.From.Game]; g.Winner > 0 {
			// The organiser's decision; the loser is the other side.
			r = Result{Winner: g.Winner}
			h, a := side(g.Home, depth+1), side(g.Away, depth+1)
			if h.Team == g.Winner {
				r.Loser = a.Team
			} else if a.Team == g.Winner {
				r.Loser = h.Team
			}
		}
		team := r.Loser
		if s.From.Winner {
			team = r.Winner
		}
		if team == 0 {
			return Side{Waiting: s.From.String()}
		}
		return Side{Team: team}
	}
	for _, g := range t.Games {
		out[g.No] = [2]Side{side(g.Home, 0), side(g.Away, 0)}
	}
	return out
}

// TeamByNo is a team by its number.
func (t *Tournament) TeamByNo(no int) *Team {
	for i := range t.Teams {
		if t.Teams[i].No == no {
			return &t.Teams[i]
		}
	}
	return nil
}

// GameByNo is a game by its number.
func (t *Tournament) GameByNo(no int) *Game {
	for i := range t.Games {
		if t.Games[i].No == no {
			return &t.Games[i]
		}
	}
	return nil
}

// Check says what's wrong with a tournament that can't be saved: team and
// game numbers must be unique, and slots must refer to teams and games that
// exist.
func (t *Tournament) Check() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("a tournament needs a name")
	}
	teams, games := map[int]bool{}, map[int]bool{}
	for _, tm := range t.Teams {
		if teams[tm.No] {
			return fmt.Errorf("two teams numbered %d", tm.No)
		}
		teams[tm.No] = true
	}
	for _, g := range t.Games {
		if games[g.No] {
			return fmt.Errorf("two games numbered %d", g.No)
		}
		games[g.No] = true
	}
	for _, g := range t.Games {
		for _, s := range []Slot{g.Home, g.Away} {
			if s.Team > 0 && !teams[s.Team] {
				return fmt.Errorf("game %d: there's no team %d", g.No, s.Team)
			}
			if s.From != nil && !games[s.From.Game] {
				return fmt.Errorf("game %d: there's no game %d", g.No, s.From.Game)
			}
			if s.Override > 0 && !teams[s.Override] {
				return fmt.Errorf("game %d: there's no team %d", g.No, s.Override)
			}
		}
		if g.Winner > 0 && !teams[g.Winner] {
			return fmt.Errorf("game %d: there's no team %d", g.No, g.Winner)
		}
	}
	return nil
}

// --- storage -----------------------------------------------------------------------

// Store is a folder of tournaments.
type Store struct {
	dir string
	mu  sync.Mutex
}

// ErrNotFound is returned for a tournament that isn't there.
var ErrNotFound = errors.New("no such tournament")

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
		return "", fmt.Errorf("invalid tournament id %q", id)
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// Entry is a tournament in the list.
type Entry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Dates   string `json:"dates,omitempty"`
	Teams   int    `json:"teams"`
	Games   int    `json:"games"`
	Updated string `json:"updated,omitempty"`
}

// List is every tournament, newest first.
func (s *Store) List() ([]Entry, error) {
	files, err := filepath.Glob(filepath.Join(s.dir, "*.json"))
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	for _, f := range files {
		t, err := s.Get(strings.TrimSuffix(filepath.Base(f), ".json"))
		if err != nil {
			continue
		}
		out = append(out, Entry{ID: t.ID, Name: t.Name, Dates: t.Dates, Teams: len(t.Teams), Games: len(t.Games), Updated: t.Updated})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated > out[j].Updated })
	return out, nil
}

// Get reads a tournament.
func (s *Store) Get(id string) (*Tournament, error) {
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
	var t Tournament
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("tournament %s: %w", id, err)
	}
	t.ID = id
	if t.Teams == nil {
		t.Teams = []Team{}
	}
	if t.Games == nil {
		t.Games = []Game{}
	}
	return &t, nil
}

// Put saves a tournament; a new one gets an id from its name.
func (s *Store) Put(t *Tournament) error {
	if err := t.Check(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ID == "" {
		base := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(t.Name), "-"), "-")
		if len(base) > 40 {
			base = strings.Trim(base[:40], "-")
		}
		if base == "" || base == "current" { // /api/tournaments/current is taken
			base = "tournament"
		}
		for i := 1; ; i++ {
			id := base
			if i > 1 {
				id = fmt.Sprintf("%s-%d", base, i)
			}
			if _, err := os.Stat(filepath.Join(s.dir, id+".json")); errors.Is(err, os.ErrNotExist) {
				t.ID = id
				break
			}
		}
	}
	p, err := s.path(t.ID)
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

// Delete removes a tournament (not its games or teams).
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
