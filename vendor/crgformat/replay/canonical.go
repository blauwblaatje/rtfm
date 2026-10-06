package replay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"unicode/utf16"
)

// SummaryHash is the value a Checkpoint stores: SHA-256 of the RFC 8785
// (JCS) canonical form of the summary, without its "log" member. Leaving
// "log" out means renaming the log file doesn't change the hash.
func SummaryHash(doc any) (string, error) {
	generic, err := toGeneric(doc)
	if err != nil {
		return "", err
	}
	if m, ok := generic.(map[string]any); ok {
		delete(m, "log")
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, generic); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

// toGeneric round-trips v through JSON into maps, slices and json.Number.
func toGeneric(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var out any
	return out, d.Decode(&out)
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch v := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		buf.WriteString(strconv.FormatBool(v))
	case json.Number:
		return writeNumber(buf, v)
	case string:
		writeString(buf, v)
	case []any:
		buf.WriteByte('[')
		for i, x := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, x); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		// JCS sorts keys by UTF-16 code units, not bytes.
		slices.SortFunc(keys, func(a, b string) int {
			return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
		})
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeString(buf, k)
			buf.WriteByte(':')
			if err := writeCanonical(buf, v[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("canonical JSON: unexpected type %T", v)
	}
	return nil
}

// writeNumber handles integers exactly. The format only uses integers; any
// other number must be one that ECMAScript prints without an exponent.
func writeNumber(buf *bytes.Buffer, n json.Number) error {
	if i, err := n.Int64(); err == nil && math.Abs(float64(i)) <= 1<<53 {
		buf.WriteString(strconv.FormatInt(i, 10))
		return nil
	}
	f, err := n.Float64()
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return fmt.Errorf("canonical JSON: unsupported number %s", n)
	}
	if math.Abs(f) >= 1e-6 && math.Abs(f) < 1e21 {
		buf.WriteString(strconv.FormatFloat(f, 'f', -1, 64))
		return nil
	}
	return fmt.Errorf("canonical JSON: number %s needs exponent form, which is not supported", n)
}

// writeString escapes like ECMAScript JSON.stringify, as JCS requires:
// only quote, backslash and control characters; everything else is literal UTF-8.
func writeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
}
