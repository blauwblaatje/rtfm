package web

import (
	"net/http"
	"strings"
	"time"

	"rtfm/internal/fixture"
)

// Every tournament has a public calendar, /calendar/<token>.ics: its games,
// to subscribe to in Google Calendar and the like, for the audience or a
// website. Like a team link it doesn't say which tournament it is.

// calendar serves the public calendar.
func (s *Site) calendar(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutSuffix(r.PathValue("file"), ".ics")
	var ev *fixture.Event
	if ok && tokenRe.MatchString(token) {
		s.mu.Lock()
		id := s.links[token]
		s.mu.Unlock()
		if ev = s.event(id); ev != nil && ev.CalendarToken() != token {
			ev = nil
		}
	}
	if ev == nil {
		http.Error(w, "This calendar doesn't exist (any more): ask the organisers for its link.", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="`+fixture.Slug(ev.T.Name)+`.ics"`)
	w.Header().Set("Cache-Control", "max-age=300")
	w.Write(ev.ICS(time.Now()))
}

// newCalendarLink gives the public calendar a new link; the old one stops
// working.
func (s *Site) newCalendarLink(w http.ResponseWriter, r *http.Request, ev *fixture.Event) {
	old, token := ev.NewCalendarLink()
	s.mu.Lock()
	delete(s.links, old)
	s.links[token] = ev.ID
	s.mu.Unlock()
	s.save(ev)
	s.logf(ev.ID, "public calendar: new link, the old one no longer works")
	writeJSON(w, map[string]string{"link": "/calendar/" + token + ".ics"})
}
