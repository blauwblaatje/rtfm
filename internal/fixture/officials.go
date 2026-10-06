package fixture

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"crgformat/crews"
)

// Officials' league affiliation, pronouns and certification, for the IGRF:
// from WFTDA's roster of certified officials, or from a list (a
// tournament's own officials document), matched to the crews by name.

// RosterURL is WFTDA's roster of certified officials: one page with all of
// them.
const RosterURL = "https://resources.wftda.org/officiating/roller-derby-certification-program-for-officials/roster-of-certified-officials/"

// Certified is what a roster or list says about an official.
type Certified struct {
	Name     string   `json:"name"`
	League   string   `json:"league,omitempty"`
	Pronouns string   `json:"pronouns,omitempty"`
	Certs    []string `json:"certs,omitempty"` // WFTDA's names: "Non-Skating Level 2", "Skating Recognized"
	Cert     string   `json:"cert,omitempty"`  // as a list wrote it, used as it is
	// Twice: more than one official on the roster has this name, so it
	// can't say which one a crew means.
	Twice bool `json:"twice,omitempty"`
}

// Roster is officials by NameKey.
type Roster map[string]*Certified

// NameKey is a name for matching: lower case, without accents, spaces or
// punctuation ("Dropkick Bru’s" and "dropkick brus" are the same).
func NameKey(name string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	plain, _, _ := transform.String(t, name)
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, plain)
}

// FetchRoster downloads WFTDA's roster page. Its site turns away requests
// that don't look like a browser.
func FetchRoster() ([]byte, error) {
	req, err := http.NewRequest("GET", RosterURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130 Safari/537.36 RTFM")
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Accept-Language", "en")
	c := &http.Client{Timeout: 60 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("WFTDA's site answered %s (it may be blocking automatic requests: save the page in a browser and upload it instead)", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

var (
	rosterEntry = regexp.MustCompile(`<div class="cn-list-row[^"]*"[^>]*data-entry-slug="[^"]*">`)
	rosterName  = regexp.MustCompile(`<span class="org fn notranslate">([^<]*)</span>`)
	rosterOrg   = regexp.MustCompile(`<span class="organization-unit notranslate">([^<]*)</span>`)
	rosterNotes = regexp.MustCompile(`(?s)<div class="cn-notes">\s*<p>(.*?)</p>`)
	rosterCert  = regexp.MustCompile(`<li class="cn-certification-name[^"]*">([^<]*)</li>`)
	tags        = regexp.MustCompile(`<[^>]+>`)
)

// ParseRoster reads WFTDA's roster page (as downloaded, or saved from a
// browser).
func ParseRoster(page []byte) (Roster, error) {
	out := Roster{}
	// Each entry runs from its start to the next one's.
	starts := rosterEntry.FindAllIndex(page, -1)
	for i, st := range starts {
		end := len(page)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		block := page[st[1]:end]
		name := rosterName.FindSubmatch(block)
		if name == nil {
			continue
		}
		c := &Certified{Name: clean(string(name[1]))}
		if o := rosterOrg.FindSubmatch(block); o != nil {
			c.League = clean(string(o[1]))
		}
		if n := rosterNotes.FindSubmatch(block); n != nil {
			c.Pronouns = clean(tags.ReplaceAllString(string(n[1]), " "))
		}
		for _, cm := range rosterCert.FindAllSubmatch(block, -1) {
			c.Certs = append(c.Certs, clean(string(cm[1])))
		}
		if k := NameKey(c.Name); k != "" {
			if out[k] != nil {
				c = &Certified{Name: c.Name, Twice: true}
			}
			out[k] = c
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no officials found on that page: is it WFTDA's roster of certified officials?")
	}
	return out, nil
}

func clean(s string) string { return strings.Join(strings.Fields(html.UnescapeString(s)), " ") }

// ReadOfficialsList reads a list of officials from a spreadsheet (.xlsx, its
// first sheet with a name column) or a CSV: the header row names the
// columns ("Name"/"Derby name"/"Official", "League"/"Affiliation",
// "Certification"/"Cert"/"Level", "Pronouns"). WFTDA's roster page saved
// from a browser works too.
func ReadOfficialsList(data []byte) (Roster, error) {
	switch {
	case bytes.HasPrefix(data, []byte("PK")):
		f, err := excelize.OpenReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		for _, sheet := range f.GetSheetList() {
			rows, err := f.GetRows(sheet)
			if err != nil {
				continue
			}
			if r, err := fromRows(rows); err == nil {
				return r, nil
			}
		}
		return nil, errors.New("no sheet with a name column and a league or certification column")
	case bytes.Contains(bytes.ToLower(data[:min(len(data), 4096)]), []byte("<html")) || bytes.Contains(data, []byte("cn-list-row")):
		return ParseRoster(data)
	default:
		r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
		r.FieldsPerRecord = -1
		if bytes.Count(data[:min(len(data), 2048)], []byte(";")) > bytes.Count(data[:min(len(data), 2048)], []byte(",")) {
			r.Comma = ';'
		}
		rows, err := r.ReadAll()
		if err != nil {
			return nil, err
		}
		return fromRows(rows)
	}
}

// fromRows finds the header row (within the first 20) and reads the rows
// under it.
func fromRows(rows [][]string) (Roster, error) {
	is := func(h string, words ...string) bool {
		h = strings.ToLower(strings.TrimSpace(h))
		for _, w := range words {
			if strings.Contains(h, w) {
				return true
			}
		}
		return false
	}
	for hi := 0; hi < min(len(rows), 20); hi++ {
		col := map[string]int{"name": -1, "league": -1, "cert": -1, "pronouns": -1}
		for i, h := range rows[hi] {
			switch {
			case is(h, "legal"): // never legal names
			case is(h, "role", "position"):
			case col["pronouns"] < 0 && is(h, "pronoun"):
				col["pronouns"] = i
			case col["league"] < 0 && is(h, "league", "affiliation"):
				col["league"] = i
			case col["cert"] < 0 && is(h, "cert", "level"):
				col["cert"] = i
			case col["name"] < 0 && is(h, "derby name", "skate name", "official name", "name", "official"):
				col["name"] = i
			}
		}
		if col["name"] < 0 || col["league"] < 0 && col["cert"] < 0 {
			continue
		}
		out := Roster{}
		cell := func(row []string, k string) string {
			if i := col[k]; i >= 0 && i < len(row) {
				return strings.TrimSpace(row[i])
			}
			return ""
		}
		for _, row := range rows[hi+1:] {
			c := &Certified{Name: cell(row, "name"), League: cell(row, "league"), Cert: cell(row, "cert"), Pronouns: cell(row, "pronouns")}
			if k := NameKey(c.Name); k != "" {
				out[k] = c
			}
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	return nil, errors.New("no header row with a name column and a league or certification column")
}

// skatingRoles are the referees' positions; every other position is a
// non-skating one.
var skatingRoles = map[string]bool{"Head Referee": true, "Inside Pack Referee": true, "Jammer Referee": true,
	"Outside Pack Referee": true, "Referee Alternate": true}

// CertFor is the certification to write on the IGRF for an official in a
// role: what fits the role (Skating for referees, Non-Skating for the
// rest), else everything they have.
func CertFor(role string, certs []string) string {
	var fit []string
	for _, c := range certs {
		skating := strings.HasPrefix(c, "Skating")
		if skating == skatingRoles[role] {
			fit = append(fit, c)
		}
	}
	if len(fit) == 0 {
		fit = certs
	}
	return strings.Join(fit, ", ")
}

// FillReport says what Fill did.
type FillReport struct {
	Officials int      `json:"officials"`
	Matched   int      `json:"matched"`
	Changed   int      `json:"changed"`
	NotFound  []string `json:"notFound"`
	Twice     []string `json:"twice"` // more than one with that name on the roster: fill these in by hand
}

// Fill puts league, pronouns and certification from a roster into the
// crews' officials it finds by name: into empty fields, or every field it
// has a value for with overwrite.
func Fill(cs []*crews.Crew, r Roster, overwrite bool) FillReport {
	rep := FillReport{NotFound: []string{}, Twice: []string{}}
	for _, c := range cs {
		for i := range c.Officials {
			o := &c.Officials[i]
			rep.Officials++
			e := r[NameKey(o.Name)]
			if e != nil && e.Twice {
				if !slices.Contains(rep.Twice, o.Name) {
					rep.Twice = append(rep.Twice, o.Name)
				}
				continue
			}
			if e == nil {
				if !slices.Contains(rep.NotFound, o.Name) {
					rep.NotFound = append(rep.NotFound, o.Name)
				}
				continue
			}
			rep.Matched++
			cert := e.Cert
			if cert == "" {
				cert = CertFor(o.Role, e.Certs)
			}
			changed := false
			set := func(field *string, v string) {
				if v != "" && *field != v && (overwrite || *field == "") {
					*field, changed = v, true
				}
			}
			set(&o.League, e.League)
			set(&o.Cert, cert)
			set(&o.Pronouns, e.Pronouns)
			if changed {
				rep.Changed++
			}
		}
	}
	slices.Sort(rep.NotFound)
	slices.Sort(rep.Twice)
	return rep
}
