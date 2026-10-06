package replay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"crgformat"
)

// Event is one line of the event log. It holds the union of all payload
// fields; the schema guarantees which ones are present for each type.
// Fields whose JSON type differs between event types ("number", "info")
// stay raw and are decoded by the handler that knows the type.
type Event struct {
	V    int    `json:"v"`
	Seq  int    `json:"seq"`
	T    string `json:"t"`
	Type string `json:"type"`
	PC   *int64 `json:"pc"`
	JC   *int64 `json:"jc"`
	Src  *struct {
		Op     string `json:"op"`
		Device string `json:"device"`
		Role   string `json:"role"`
	} `json:"src"`

	// Raw per-type fields.
	Number json.RawMessage `json:"number"` // int (period/jam/clock) or string (skater)
	Info   json.RawMessage `json:"info"`   // object (game) or string (expulsion)

	// Nullable fields: missing, null and a value mean different things.
	Skater Opt[string] `json:"skater"`
	Trip   Opt[string] `json:"trip"`

	Game               string            `json:"game"`
	Name               *string           `json:"name"`
	Ruleset            *Ruleset          `json:"ruleset"`
	Overrides          map[string]any    `json:"overrides"`
	Team               string            `json:"team"`
	PreparedTeam       *string           `json:"preparedTeam"`
	FullName           *string           `json:"fullName"`
	League             *string           `json:"league"`
	TeamName           *string           `json:"teamName"`
	Initials           *string           `json:"initials"`
	UniformColor       *string           `json:"uniformColor"`
	Colors             map[string]string `json:"colors"`
	AlternateNames     map[string]string `json:"alternateNames"`
	Logo               *string           `json:"logo"`
	Pronouns           *string           `json:"pronouns"`
	Pronunciation      *string           `json:"pronunciation"`
	WUID               *string           `json:"wuid"`
	Flags              *string           `json:"flags"`
	Status             *string           `json:"status"`
	Timezone           *string           `json:"timezone"`
	GameType           *string           `json:"gameType"`
	CueMode            *string           `json:"cueMode"`
	NameCue            *string           `json:"nameCue"`
	NameCuePron        *string           `json:"nameCuePronunciation"`
	Detail             string            `json:"detail"`
	Outcome            string            `json:"outcome"`
	ForfeitingTeam     string            `json:"forfeitingTeam"`
	Staff              string            `json:"staff"`
	AsTimeout          *bool             `json:"asTimeout"`
	Official           string            `json:"official"`
	Role               *string           `json:"role"`
	Cert               *string           `json:"cert"`
	P1Team             *string           `json:"p1Team"`
	Swap               *bool             `json:"swap"`
	Period             string            `json:"period"`
	Jam                string            `json:"jam"`
	Overtime           bool              `json:"overtime"`
	InjuryContinuation bool              `json:"injuryContinuation"`
	Reason             string            `json:"reason"`
	Timeout            string            `json:"timeout"`
	AfterJam           string            `json:"afterJam"`
	Owner              *string           `json:"owner"`
	Review             *bool             `json:"review"`
	Duration           *int64            `json:"duration"`
	Request            *string           `json:"request"`
	Result             *string           `json:"result"`
	Retained           *bool             `json:"retained"`
	Clock              string            `json:"clock"`
	Time               *int64            `json:"time"`
	Score              *Score            `json:"score"`
	HR                 string            `json:"hr"`
	HNSO               string            `json:"hnso"`
	Reverts            []int             `json:"reverts"`
	Clocks             map[string]int64  `json:"clocks"`
	Flag               string            `json:"flag"`
	Value              bool              `json:"value"`
	AfterStarPass      *bool             `json:"afterStarPass"`
	Points             int               `json:"points"`
	Before             string            `json:"before"`
	Offset             int               `json:"offset"`
	Adjustment         string            `json:"adjustment"`
	Amount             int               `json:"amount"`
	DuringJam          bool              `json:"duringJam"`
	LastTwoMinutes     bool              `json:"lastTwoMinutes"`
	Position           string            `json:"position"`
	NotFielded         *bool             `json:"notFielded"`
	SitFor3            *bool             `json:"sitFor3"`
	FromJam            string            `json:"fromJam"`
	ToJam              string            `json:"toJam"`
	Penalty            string            `json:"penalty"`
	Code               *string           `json:"code"`
	Slot               *int              `json:"slot"`
	CalledBy           *string           `json:"calledBy"`
	CallingPosition    *string           `json:"callingPosition"`
	ExtraInfo          string            `json:"extraInfo"`
	Suspension         bool              `json:"suspension"`
	BoxTrip            string            `json:"boxTrip"`
	Jammer             bool              `json:"jammer"`
	BetweenJams        bool              `json:"betweenJams"`
	Start              *BoxPoint         `json:"start"`
	End                *BoxPoint         `json:"end"`
	SummarySha256      string            `json:"summarySha256"`
	Target             *struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"target"`
	Text  string `json:"text"`
	Check string `json:"check"`
	By    string `json:"by"` // SkaterReplaced
}

// Opt distinguishes a missing field (Set=false) from null (Null=true).
type Opt[T any] struct {
	Set  bool
	Null bool
	V    T
}

func (o *Opt[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.V)
}

// LineError is a problem with one line of the log.
type LineError struct {
	Line int
	Seq  int
	Type string
	Err  error
}

func (e *LineError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("line %d (seq %d, %s): %v", e.Line, e.Seq, e.Type, e.Err)
	}
	return fmt.Sprintf("line %d: %v", e.Line, e.Err)
}

func (e *LineError) Unwrap() error { return e.Err }

// ReadLog reads and validates an event log. Every line must match the
// schema, and seq must run 1, 2, 3, ... without gaps. It returns all problems
// found, not just the first, so one run shows everything that's wrong.
func ReadLog(r io.Reader, v *crgformat.Validators) ([]*Event, []error) {
	var events []*Event
	var errs []error
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	line, prevSeq := 0, 0
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			errs = append(errs, &LineError{Line: line, Err: fmt.Errorf("empty line")})
			continue
		}
		doc, err := crgformat.DecodeJSON(raw)
		if err != nil {
			errs = append(errs, &LineError{Line: line, Err: fmt.Errorf("invalid JSON: %w", err)})
			continue
		}
		var e Event
		if err := json.Unmarshal(raw, &e); err != nil {
			errs = append(errs, &LineError{Line: line, Err: err})
			continue
		}
		// Checked against the previous line, not the previous valid event,
		// so one bad line doesn't make every later seq look wrong.
		if e.Seq != prevSeq+1 {
			errs = append(errs, &LineError{Line: line, Seq: e.Seq, Type: e.Type,
				Err: fmt.Errorf("expected seq %d: events are missing or out of order", prevSeq+1)})
		}
		prevSeq = e.Seq
		if err := v.ValidateEvent(doc); err != nil {
			errs = append(errs, &LineError{Line: line, Seq: e.Seq, Type: e.Type, Err: err})
			continue
		}
		events = append(events, &e)
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, err)
	}
	return events, errs
}
