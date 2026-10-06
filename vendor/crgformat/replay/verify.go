package replay

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CheckpointResult is the outcome of checking one Checkpoint event.
type CheckpointResult struct {
	Seq      int
	Expected string // hash stored in the log
	Got      string // hash of the replayed summary
}

func (c CheckpointResult) OK() bool { return c.Expected == c.Got }

// VerifyCheckpoints replays the log up to each Checkpoint and compares the
// summary hash with the one stored. Each checkpoint is replayed from the
// start, because an Undone after the checkpoint can't change what the
// summary looked like when the checkpoint was written.
func VerifyCheckpoints(events []*Event) ([]CheckpointResult, error) {
	var out []CheckpointResult
	for i, e := range events {
		if e.Type != "Checkpoint" {
			continue
		}
		res, err := Replay(events[:i])
		if err != nil {
			return out, fmt.Errorf("replaying up to checkpoint seq %d: %w", e.Seq, err)
		}
		got, err := SummaryHash(res.Summary)
		if err != nil {
			return out, err
		}
		out = append(out, CheckpointResult{Seq: e.Seq, Expected: e.SummarySha256, Got: got})
	}
	return out, nil
}

// Diff compares two JSON documents and describes each difference by path.
// Object key order is ignored; array order is not.
func Diff(want, got any) ([]string, error) {
	w, err := toGeneric(want)
	if err != nil {
		return nil, err
	}
	g, err := toGeneric(got)
	if err != nil {
		return nil, err
	}
	var out []string
	diff("", w, g, &out)
	return out, nil
}

func diff(path string, want, got any, out *[]string) {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: expected an object, got %s", orRoot(path), show(got)))
			return
		}
		keys := map[string]bool{}
		for k := range w {
			keys[k] = true
		}
		for k := range g {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			p := path + "." + k
			wv, inW := w[k]
			gv, inG := g[k]
			switch {
			case !inG:
				*out = append(*out, fmt.Sprintf("%s: missing (expected %s)", p[1:], show(wv)))
			case !inW:
				*out = append(*out, fmt.Sprintf("%s: unexpected %s", p[1:], show(gv)))
			default:
				diff(p, wv, gv, out)
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: expected an array, got %s", orRoot(path), show(got)))
			return
		}
		for i := range max(len(w), len(g)) {
			p := fmt.Sprintf("%s[%d]", path, i)
			switch {
			case i >= len(g):
				*out = append(*out, fmt.Sprintf("%s: missing (expected %s)", trim(p), show(w[i])))
			case i >= len(w):
				*out = append(*out, fmt.Sprintf("%s: unexpected %s", trim(p), show(g[i])))
			default:
				diff(p, w[i], g[i], out)
			}
		}
	default:
		if !equalScalar(want, got) {
			*out = append(*out, fmt.Sprintf("%s: expected %s, got %s", orRoot(path), show(want), show(got)))
		}
	}
}

func equalScalar(a, b any) bool {
	an, aok := a.(json.Number)
	bn, bok := b.(json.Number)
	if aok && bok {
		af, _ := an.Float64()
		bf, _ := bn.Float64()
		return af == bf
	}
	return a == b
}

func show(v any) string {
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}

func trim(p string) string { return strings.TrimPrefix(p, ".") }

func orRoot(p string) string {
	if p == "" {
		return "(root)"
	}
	return trim(p)
}
