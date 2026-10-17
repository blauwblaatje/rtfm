package fixture

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"crgformat/library"
)

// MaxSkaters is how many skaters a team can have on a game's IGRF.
const MaxSkaters = 15

// DefaultBenches are the benches the home team (the higher seed) picks
// from, unless the organiser names them otherwise.
var DefaultBenches = [2]string{"Pivot line", "Jam line"}

// DefaultCloseBefore is how long before a game its line-ups close, unless
// the organiser says otherwise.
const DefaultCloseBefore = 2 * time.Hour

// Settings are the organiser's for the teams' line-ups.
type Settings struct {
	Benches [2]string `json:"benches"`
	// CloseBefore is how many minutes before a game its line-ups close
	// (nil: DefaultCloseBefore).
	CloseBefore *int `json:"closeBefore,omitempty"`
	// TimeZone is the venue's, for the schedule's times ("": guessed from
	// the venue's country).
	TimeZone string `json:"timeZone,omitempty"`
}

// The organiser can open or close a game's line-ups whatever the deadline.
const (
	LineupsAuto   = "auto" // open until the deadline
	LineupsOpen   = "open"
	LineupsClosed = "closed"
)

// LineupState says whether teams can change their line-ups for a game.
type LineupState struct {
	Mode   string    `json:"mode"` // LineupsAuto, LineupsOpen or LineupsClosed
	Open   bool      `json:"open"`
	Closes time.Time `json:"closes,omitzero"` // the deadline, in auto mode, when the game's start is known
}

// Lineup is what a team says about one of its games, through its own link:
// the skaters who play (charter numbers), the captain, and for the home
// team, the higher seed, its bench and uniform colour.
type Lineup struct {
	Skaters []string  `json:"skaters"`
	Captain string    `json:"captain,omitempty"`
	Bench   string    `json:"bench,omitempty"`
	Color   string    `json:"color,omitempty"`
	Updated time.Time `json:"updated"`
}

func lineupKey(game, team int) string { return fmt.Sprintf("%d/%d", game, team) }

// NewToken is a team link's secret: 128 random bits.
func NewToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// EnsureLinks gives every team without a link one, and the tournament a
// public calendar; it says whether any was made.
func (ev *Event) EnsureLinks() bool {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	made := false
	if ev.CalendarLink == "" {
		ev.CalendarLink = NewToken()
		made = true
	}
	for _, tm := range ev.T.Teams {
		if ev.Links[tm.No] == "" {
			if ev.Links == nil {
				ev.Links = map[int]string{}
			}
			ev.Links[tm.No] = NewToken()
			made = true
		}
	}
	return made
}

// TeamLinks are the teams' link tokens, by team number.
func (ev *Event) TeamLinks() map[int]string {
	ev.mu.RLock()
	defer ev.mu.RUnlock()
	out := make(map[int]string, len(ev.Links))
	for k, v := range ev.Links {
		out[k] = v
	}
	return out
}

// NewLink replaces a team's link: the old one stops working. It answers the
// old token and the new one.
func (ev *Event) NewLink(team int) (old, token string, err error) {
	if ev.T.TeamByNo(team) == nil {
		return "", "", fmt.Errorf("there is no team %d", team)
	}
	ev.mu.Lock()
	defer ev.mu.Unlock()
	if ev.Links == nil {
		ev.Links = map[int]string{}
	}
	old, token = ev.Links[team], NewToken()
	ev.Links[team] = token
	return old, token, nil
}

// TeamByToken is the team whose link this is, or 0.
func (ev *Event) TeamByToken(token string) int {
	ev.mu.RLock()
	defer ev.mu.RUnlock()
	for no, t := range ev.Links {
		if t == token {
			return no
		}
	}
	return 0
}

// Lineup is a copy of what a team said about a game, or nil.
func (ev *Event) Lineup(game, team int) *Lineup {
	ev.mu.RLock()
	defer ev.mu.RUnlock()
	return ev.lineup(game, team)
}

func (ev *Event) lineup(game, team int) *Lineup {
	l := ev.Lineups[lineupKey(game, team)]
	if l == nil || team == 0 {
		return nil
	}
	cp := *l
	cp.Skaters = append([]string{}, l.Skaters...)
	return &cp
}

// SetLineup keeps what a team says about one of its games, after checking
// it: the line-ups are open, the team plays the game, the skaters are on
// its charter (at most MaxSkaters), the captain is one of them, and bench
// and colour only come from the home team.
func (ev *Event) SetLineup(game, team int, l Lineup) (*Lineup, error) {
	if st := ev.LineupState(game, time.Now()); !st.Open {
		if st.Mode == LineupsClosed || st.Closes.IsZero() {
			return nil, fmt.Errorf("the line-ups for game %d are closed: ask the organisers", game)
		}
		return nil, fmt.Errorf("the line-ups for game %d closed at %s: ask the organisers", game, ev.FormatTime(st.Closes))
	}
	var gv *GameView
	for _, g := range ev.Games() {
		if g.No == game {
			gv = &g
		}
	}
	if gv == nil {
		return nil, fmt.Errorf("there is no game %d", game)
	}
	side := slices.Index(gv.Choice.Teams[:], team)
	if side < 0 {
		return nil, fmt.Errorf("your team doesn't play game %d", game)
	}
	lt := ev.Teams[team]
	if lt == nil {
		return nil, errors.New("there's no charter for your team: ask the organisers")
	}
	onCharter := map[string]bool{}
	for _, s := range lt.Skaters {
		onCharter[strings.TrimSpace(s.Number)] = true
	}
	out := Lineup{Updated: time.Now().UTC(), Skaters: []string{}}
	for _, n := range l.Skaters {
		n = strings.TrimSpace(n)
		if !onCharter[n] {
			return nil, fmt.Errorf("#%s isn't on your charter", n)
		}
		if !slices.Contains(out.Skaters, n) {
			out.Skaters = append(out.Skaters, n)
		}
	}
	if len(out.Skaters) > MaxSkaters {
		return nil, fmt.Errorf("%d skaters: at most %d can play a game", len(out.Skaters), MaxSkaters)
	}
	if out.Captain = strings.TrimSpace(l.Captain); out.Captain != "" && !slices.Contains(out.Skaters, out.Captain) {
		return nil, fmt.Errorf("the captain, #%s, isn't one of the skaters playing", out.Captain)
	}
	if side == 0 {
		benches := ev.Benches()
		if out.Bench = strings.TrimSpace(l.Bench); out.Bench != "" && !slices.Contains(benches[:], out.Bench) {
			return nil, fmt.Errorf("%q isn't a bench", out.Bench)
		}
		out.Color = strings.TrimSpace(l.Color)
	}
	ev.mu.Lock()
	defer ev.mu.Unlock()
	if ev.Lineups == nil {
		ev.Lineups = map[string]*Lineup{}
	}
	ev.Lineups[lineupKey(game, team)] = &out
	return &out, nil
}

// withLineup is a team with its line-up: every skater of the charter, the
// ones who don't play Not Skating (crossed out on the IGRF), the captain
// flagged C; the team itself if there's no line-up (yet).
func withLineup(lt *library.Team, l *Lineup) *library.Team {
	if l == nil || len(l.Skaters) == 0 {
		return lt
	}
	cp := *lt
	cp.Skaters = nil
	for _, s := range lt.Skaters {
		n := strings.TrimSpace(s.Number)
		var flags []string
		for _, f := range strings.Fields(s.Flags) {
			if f != "C" && f != "ALT" {
				flags = append(flags, f)
			}
		}
		switch {
		case !slices.Contains(l.Skaters, n):
			flags = append(flags, "ALT")
		case n == l.Captain:
			flags = append(flags, "C")
		}
		s.Flags = strings.Join(flags, " ")
		cp.Skaters = append(cp.Skaters, s)
	}
	return &cp
}

// SetChoice keeps the organiser's choice for a game: its teams (a team on
// the schedule stays), uniform colours ("" for the default: the home team's
// pick, the schedule's, the charter's) and crew.
func (ev *Event) SetChoice(no int, ch Choice) error {
	ev.choiceMu.Lock()
	defer ev.choiceMu.Unlock()
	return ev.setChoice(no, ch)
}

// ChoicePatch is one change to a game's choice: a side's team (which puts
// its colour back to the default) or colour, or the crew. The rest of the
// choice stays as it is on the server, so two organisers changing
// different things don't undo each other's.
type ChoicePatch struct {
	Side  int     `json:"side"`
	Team  *int    `json:"team,omitempty"`
	Color *string `json:"color,omitempty"`
	Crew  *string `json:"crew,omitempty"`
}

// PatchChoice changes one thing of a game's choice, and returns the choice.
func (ev *Event) PatchChoice(no int, p ChoicePatch) (Choice, error) {
	if p.Side != 0 && p.Side != 1 {
		return Choice{}, fmt.Errorf("there is no side %d", p.Side)
	}
	ev.choiceMu.Lock()
	defer ev.choiceMu.Unlock()
	var ch Choice
	found := false
	for _, g := range ev.Games() {
		if g.No == no {
			found = true
			ch = g.Stored
			if !g.Picked {
				ch = Choice{Teams: g.Choice.Teams, Crew: g.Choice.Crew}
			}
		}
	}
	if !found {
		return Choice{}, fmt.Errorf("there is no game %d", no)
	}
	if p.Team != nil {
		ch.Teams[p.Side], ch.Colors[p.Side] = *p.Team, ""
	}
	if p.Color != nil {
		ch.Colors[p.Side] = *p.Color
	}
	if p.Crew != nil {
		ch.Crew = *p.Crew
	}
	if err := ev.setChoice(no, ch); err != nil {
		return Choice{}, err
	}
	return ch, nil
}

func (ev *Event) setChoice(no int, ch Choice) error {
	var gv *GameView
	for _, g := range ev.Games() {
		if g.No == no {
			gv = &g
		}
	}
	if gv == nil {
		return fmt.Errorf("there is no game %d", no)
	}
	for i := range ch.Teams {
		if gv.Fixed[i] {
			ch.Teams[i] = gv.Default.Teams[i]
		}
		if ch.Teams[i] != 0 && ev.T.TeamByNo(ch.Teams[i]) == nil {
			return fmt.Errorf("there is no team %d", ch.Teams[i])
		}
		ch.Colors[i] = strings.TrimSpace(ch.Colors[i])
	}
	if ch.Crew != "" && ev.Crew(ch.Crew) == nil {
		return fmt.Errorf("there is no crew %q", ch.Crew)
	}
	ev.mu.Lock()
	defer ev.mu.Unlock()
	if ev.Choices == nil {
		ev.Choices = map[int]Choice{}
	}
	ev.Choices[no] = ch
	return nil
}

// ClearChoices forgets the organiser's choices: every game is back to its
// defaults. The teams' line-ups stay.
func (ev *Event) ClearChoices() {
	ev.choiceMu.Lock()
	defer ev.choiceMu.Unlock()
	ev.mu.Lock()
	defer ev.mu.Unlock()
	ev.Choices = nil
}

// TeamGames are the games a team plays, as far as known: on the schedule,
// or picked for a bracket game.
func (ev *Event) TeamGames(team int) []GameView {
	var out []GameView
	for _, g := range ev.Games() {
		if g.Choice.Teams[0] == team || g.Choice.Teams[1] == team {
			out = append(out, g)
		}
	}
	return out
}

// Benches are the two benches' names.
func (ev *Event) Benches() [2]string {
	ev.mu.RLock()
	defer ev.mu.RUnlock()
	b := ev.Settings.Benches
	if b[0] == "" || b[1] == "" {
		return DefaultBenches
	}
	return b
}

// OtherBench is the bench the away team gets when the home team took b
// ("" when it took none).
func (ev *Event) OtherBench(b string) string {
	switch bs := ev.Benches(); b {
	case bs[0]:
		return bs[1]
	case bs[1]:
		return bs[0]
	}
	return ""
}

// CloseBefore is how long before a game its line-ups close.
func (ev *Event) CloseBefore() time.Duration {
	ev.mu.RLock()
	defer ev.mu.RUnlock()
	if ev.Settings.CloseBefore == nil {
		return DefaultCloseBefore
	}
	return time.Duration(*ev.Settings.CloseBefore) * time.Minute
}

// Location is the venue's time zone: the organiser's, else guessed from
// the venue's country (guessed true), else UTC (guessed true too).
func (ev *Event) Location() (loc *time.Location, guessed bool) {
	ev.mu.RLock()
	tz := ev.Settings.TimeZone
	ev.mu.RUnlock()
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc, false
		}
	}
	if loc, err := time.LoadLocation(countryZones[strings.ToLower(strings.TrimSpace(ev.T.Venue.Country))]); err == nil && loc != time.UTC {
		return loc, true
	}
	return time.UTC, true
}

// countryZones are the time zones of countries with one (or one that
// tournaments are mostly in).
var countryZones = map[string]string{
	"sweden": "Europe/Stockholm", "norway": "Europe/Oslo", "denmark": "Europe/Copenhagen", "finland": "Europe/Helsinki",
	"iceland": "Atlantic/Reykjavik", "netherlands": "Europe/Amsterdam", "the netherlands": "Europe/Amsterdam",
	"belgium": "Europe/Brussels", "luxembourg": "Europe/Luxembourg", "france": "Europe/Paris", "germany": "Europe/Berlin",
	"austria": "Europe/Vienna", "switzerland": "Europe/Zurich", "italy": "Europe/Rome", "spain": "Europe/Madrid",
	"portugal": "Europe/Lisbon", "united kingdom": "Europe/London", "uk": "Europe/London", "england": "Europe/London",
	"scotland": "Europe/London", "wales": "Europe/London", "ireland": "Europe/Dublin", "poland": "Europe/Warsaw",
	"czech republic": "Europe/Prague", "czechia": "Europe/Prague", "hungary": "Europe/Budapest", "greece": "Europe/Athens",
	"estonia": "Europe/Tallinn", "latvia": "Europe/Riga", "lithuania": "Europe/Vilnius", "new zealand": "Pacific/Auckland",
	"japan": "Asia/Tokyo", "south korea": "Asia/Seoul", "singapore": "Asia/Singapore", "argentina": "America/Argentina/Buenos_Aires",
	"colombia": "America/Bogota", "chile": "America/Santiago", "south africa": "Africa/Johannesburg", "israel": "Asia/Jerusalem",
}

// Start is when a game starts, if the schedule says.
func (ev *Event) Start(no int) (time.Time, bool) {
	g := ev.T.GameByNo(no)
	if g == nil {
		return time.Time{}, false
	}
	loc, _ := ev.Location()
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02 15:04:05", "2006-01-02 3:04 PM", "2006-01-02 3:04PM", "2006-01-02 3PM"} {
		if t, err := time.ParseInLocation(layout, strings.TrimSpace(g.Date)+" "+strings.ToUpper(strings.TrimSpace(g.Time)), loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// LineupState says whether a game's line-ups can be changed now.
func (ev *Event) LineupState(no int, now time.Time) LineupState {
	ev.mu.RLock()
	mode := ev.LineupModes[no]
	ev.mu.RUnlock()
	switch mode {
	case LineupsOpen:
		return LineupState{Mode: mode, Open: true}
	case LineupsClosed:
		return LineupState{Mode: mode}
	}
	st := LineupState{Mode: LineupsAuto, Open: true}
	if start, ok := ev.Start(no); ok {
		st.Closes = start.Add(-ev.CloseBefore())
		st.Open = now.Before(st.Closes)
	}
	return st
}

// FormatTime is a time as the venue's clock says it: "Thu 15 Oct 08:00".
func (ev *Event) FormatTime(t time.Time) string {
	loc, _ := ev.Location()
	return t.In(loc).Format("Mon 2 Jan 15:04")
}

// SetLineupMode opens or closes a game's line-ups, or puts them back on
// the deadline (LineupsAuto).
func (ev *Event) SetLineupMode(no int, mode string) error {
	if ev.T.GameByNo(no) == nil {
		return fmt.Errorf("there is no game %d", no)
	}
	if mode != LineupsAuto && mode != LineupsOpen && mode != LineupsClosed {
		return fmt.Errorf("%q: the line-ups are auto, open or closed", mode)
	}
	ev.mu.Lock()
	defer ev.mu.Unlock()
	if mode == LineupsAuto {
		delete(ev.LineupModes, no)
		return nil
	}
	if ev.LineupModes == nil {
		ev.LineupModes = map[int]string{}
	}
	ev.LineupModes[no] = mode
	return nil
}

// SetSettings keeps the organiser's settings: two benches with a name each
// (benches the home teams picked are renamed along), how long before a
// game its line-ups close, the venue's time zone.
func (ev *Event) SetSettings(st Settings) error {
	for i := range st.Benches {
		st.Benches[i] = strings.TrimSpace(st.Benches[i])
	}
	if st.Benches[0] == "" || st.Benches[1] == "" || strings.EqualFold(st.Benches[0], st.Benches[1]) {
		return errors.New("the two benches need a name each, not the same")
	}
	if c := st.CloseBefore; c != nil && (*c < 0 || *c > 60*24*14) {
		return errors.New("line-ups can close from the game's start to two weeks before")
	}
	if st.TimeZone = strings.TrimSpace(st.TimeZone); st.TimeZone != "" {
		if _, err := time.LoadLocation(st.TimeZone); err != nil {
			return fmt.Errorf("%q isn't a time zone (like Europe/Stockholm)", st.TimeZone)
		}
	}
	old := ev.Benches()
	ev.mu.Lock()
	defer ev.mu.Unlock()
	for _, l := range ev.Lineups {
		for i := range old {
			if l.Bench == old[i] {
				l.Bench = st.Benches[i]
				break
			}
		}
	}
	ev.Settings = st
	return nil
}
