// Package legacy converts game files written by the Java scoreboard into a
// synthesized event log (see README "Old games").
package legacy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// node is one object in the Java state tree. The flat file keys
//
//	ScoreBoard.Game(g).Period(1).Jam(3).Duration = 94000
//	ScoreBoard.Game(g).Rule(Period.Duration)      = "30:00"
//
// become Game(g) -> Period(1) -> Jam(3) with prop Duration, and a child
// Rule(Period.Duration) holding a value directly. IDs in parentheses may
// contain dots, so keys are split on dots outside parentheses only.
type node struct {
	id    string
	value any                         // set for keys that end in Name(id)
	props map[string]any              // Name = value
	kids  map[string]map[string]*node // Name -> id -> child
}

func newNode(id string) *node {
	return &node{id: id, props: map[string]any{}, kids: map[string]map[string]*node{}}
}

// parseState reads a Java game file ({"state": {...}}).
func parseState(data []byte) (*node, error) {
	var doc struct {
		State map[string]json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.State == nil {
		return nil, fmt.Errorf(`not a scoreboard game file: no "state" object`)
	}
	root := newNode("")
	for key, raw := range doc.State {
		var v any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&v); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		segs, err := splitKey(key)
		if err != nil {
			return nil, err
		}
		n := root
		for i, seg := range segs {
			last := i == len(segs)-1
			if seg.id == nil && last {
				n.props[seg.name] = v
				break
			}
			id := "" // an intermediate part without id, like "ScoreBoard"
			if seg.id != nil {
				id = *seg.id
			}
			byID := n.kids[seg.name]
			if byID == nil {
				byID = map[string]*node{}
				n.kids[seg.name] = byID
			}
			child := byID[id]
			if child == nil {
				child = newNode(id)
				byID[id] = child
			}
			if last {
				child.value = v
			}
			n = child
		}
	}
	return root, nil
}

type segment struct {
	name string
	id   *string
}

func splitKey(key string) ([]segment, error) {
	var segs []segment
	depth, start := 0, 0
	flush := func(end int) error {
		part := key[start:end]
		if i := strings.IndexByte(part, '('); i >= 0 {
			if !strings.HasSuffix(part, ")") {
				return fmt.Errorf("bad key %q", key)
			}
			id := part[i+1 : len(part)-1]
			segs = append(segs, segment{name: part[:i], id: &id})
		} else {
			segs = append(segs, segment{name: part})
		}
		return nil
	}
	for i := 0; i < len(key); i++ {
		switch key[i] {
		case '(':
			depth++
		case ')':
			depth--
		case '.':
			if depth == 0 {
				if err := flush(i); err != nil {
					return nil, err
				}
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced parentheses in key %q", key)
	}
	return segs, flush(len(key))
}

// --- accessors -----------------------------------------------------------
// Missing properties read as the zero value, like the Java defaults.

func (n *node) str(name string) string {
	switch v := n.props[name].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func (n *node) int(name string) int64 {
	switch v := n.props[name].(type) {
	case json.Number:
		i, _ := v.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(v, 10, 64)
		return i
	}
	return 0
}

func (n *node) bool(name string) bool {
	switch v := n.props[name].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

func (n *node) has(name string) bool {
	_, ok := n.props[name]
	return ok
}

// kid returns the child Name(id), or nil.
func (n *node) kid(name, id string) *node { return n.kids[name][id] }

// list returns the children called name, sorted by numeric id where the ids
// are numbers (periods, jams, trips) and by id otherwise.
func (n *node) list(name string) []*node {
	var out []*node
	for _, c := range n.kids[name] {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		a, errA := strconv.Atoi(out[i].id)
		b, errB := strconv.Atoi(out[j].id)
		if errA == nil && errB == nil {
			return a < b
		}
		return out[i].id < out[j].id
	})
	return out
}

// values returns Name(id) = value children as a map of strings.
func (n *node) values(name string) map[string]string {
	out := map[string]string{}
	for id, c := range n.kids[name] {
		switch v := c.value.(type) {
		case string:
			out[id] = v
		case json.Number:
			out[id] = v.String()
		case bool:
			out[id] = strconv.FormatBool(v)
		}
	}
	return out
}
