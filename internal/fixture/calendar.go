package fixture

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"time"
)

// The public calendar is the games, as an iCalendar feed (RFC 5545) that
// Google Calendar, Outlook and Apple's calendars subscribe to: the teams,
// the track and the venue, nothing of the crews or line-ups. Its link is a
// secret of its own, so it doesn't lead to the organisers' page.

// GameSlot is how long a game is on the calendar, unless the next game on
// its track starts sooner.
const GameSlot = 2 * time.Hour

// CalendarToken is the public calendar's link token.
func (ev *Event) CalendarToken() string {
	ev.mu.RLock()
	defer ev.mu.RUnlock()
	return ev.CalendarLink
}

// NewCalendarLink replaces the public calendar's link: the old one stops
// working. It answers the old token and the new one.
func (ev *Event) NewCalendarLink() (old, token string) {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	old, token = ev.CalendarLink, NewToken()
	ev.CalendarLink = token
	return old, token
}

// ICS is the public calendar: an event per game whose date is known, all
// day when its time isn't. A game keeps its UID, so a calendar that has it
// changes it rather than adding another.
func (ev *Event) ICS(now time.Time) []byte {
	t := ev.T
	games := ev.Games()
	tracks := map[string]bool{}
	starts := map[int]time.Time{}
	for _, g := range games {
		tracks[strings.TrimSpace(g.Track)] = true
		if at, ok := ev.Start(g.No); ok {
			starts[g.No] = at
		}
	}
	sum := sha256.Sum256([]byte(ev.ID))
	uid := fmt.Sprintf("%x", sum[:8]) // not the id: that's the organisers' page
	loc, _ := ev.Location()
	venue := strings.Trim(strings.Join(slices.DeleteFunc([]string{t.Venue.Name, t.Venue.Address, t.Venue.City, t.Venue.Country},
		func(s string) bool { return strings.TrimSpace(s) == "" }), ", "), ", ")

	var b strings.Builder
	line := func(name, value string) { b.WriteString(fold(name + ":" + value)) }
	line("BEGIN", "VCALENDAR")
	line("VERSION", "2.0")
	line("PRODID", "-//RTFM//Roller derby Tournament Fixture Maker//EN")
	line("CALSCALE", "GREGORIAN")
	line("METHOD", "PUBLISH")
	line("X-WR-CALNAME", text(t.Name))
	line("X-WR-TIMEZONE", loc.String())
	line("REFRESH-INTERVAL;VALUE=DURATION", "PT1H")
	line("X-PUBLISHED-TTL", "PT1H")
	stamp := now.UTC().Format("20060102T150405Z")
	for _, g := range games {
		var start, end string
		if at, ok := starts[g.No]; ok {
			until := at.Add(GameSlot)
			for _, o := range games {
				if next, ok := starts[o.No]; ok && o.No != g.No && strings.TrimSpace(o.Track) == strings.TrimSpace(g.Track) && next.After(at) && next.Before(until) {
					until = next
				}
			}
			start, end = "DTSTART:"+at.UTC().Format("20060102T150405Z"), "DTEND:"+until.UTC().Format("20060102T150405Z")
		} else if day, err := time.Parse("2006-01-02", strings.TrimSpace(g.Date)); err == nil {
			start, end = "DTSTART;VALUE=DATE:"+day.Format("20060102"), "DTEND;VALUE=DATE:"+day.AddDate(0, 0, 1).Format("20060102")
		} else {
			continue
		}
		sides := g.Sides
		for i, team := range g.Choice.Teams {
			if tm := t.TeamByNo(team); tm != nil {
				sides[i] = tm.Name
			}
		}
		summary := fmt.Sprintf("Game %d: %s vs %s", g.No, sides[0], sides[1])
		desc := t.Name
		if track := strings.TrimSpace(g.Track); track != "" {
			if len(tracks) > 1 {
				summary += " (" + track + ")"
			}
			desc += "\nTrack: " + track
		}
		line("BEGIN", "VEVENT")
		line("UID", fmt.Sprintf("%s-game-%d@rtfm", uid, g.No))
		line("DTSTAMP", stamp)
		b.WriteString(fold(start))
		b.WriteString(fold(end))
		line("SUMMARY", text(summary))
		line("DESCRIPTION", text(desc))
		if venue != "" {
			line("LOCATION", text(venue))
		}
		line("TRANSP", "TRANSPARENT")
		line("END", "VEVENT")
	}
	line("END", "VCALENDAR")
	return []byte(b.String())
}

// text escapes a value of text: backslash, semicolon, comma, new line.
func text(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", "").Replace(s)
}

// fold is a content line folded to at most 75 octets a line, not within
// a character, with its CRLF.
func fold(l string) string {
	var b strings.Builder
	n := 0
	for _, r := range l {
		size := len(string(r))
		if n+size > 75 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(r)
		n += size
	}
	b.WriteString("\r\n")
	return b.String()
}
