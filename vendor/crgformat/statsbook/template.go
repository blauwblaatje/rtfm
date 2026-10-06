package statsbook

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	legacyDrawing  = regexp.MustCompile(`<legacyDrawing [^>]*r:id="([^"]+)"`)
	relTarget      = regexp.MustCompile(`<Relationship [^>]*>`)
	relID          = regexp.MustCompile(`Id="([^"]+)"`)
	relTo          = regexp.MustCompile(`Target="([^"]+)"`)
	vmlNumber      = regexp.MustCompile(`vmlDrawing(\d+)\.vml$`)
	commentsTarget = regexp.MustCompile(`Target="\.\./comments(\d+)\.xml"`)
)

// alignCommentParts renumbers a workbook's VML drawings and comment parts
// the way excelize (v2.11) expects before it adds comments:
//
//   - a sheet with comments has commentsN.xml next to vmlDrawingN.vml, its
//     legacyDrawing; excelize writes a new comment to comments<that N>.xml;
//   - on a sheet without comments excelize creates vmlDrawingN.vml and
//     commentsN.xml with N = number of comment parts + 1, without checking
//     whether they exist.
//
// The WFTDA statsbook template breaks both (Lineups has vmlDrawing7.vml with
// comments3.xml; Penalties' header/footer drawing is vmlDrawing6.vml), so
// comments would land in, or overwrite, another sheet's parts. Here sheets
// with comments get numbers 1..k and every other drawing 100 and up, leaving
// k+1, k+2, ... free. Only part names change.
func alignCommentParts(xlsx []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(xlsx), int64(len(xlsx)))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	var order []string
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		files[f.Name] = b
		order = append(order, f.Name)
	}

	vmlRename := map[string]string{}      // old vml number -> new
	commentsRename := map[string]string{} // old comments number -> new
	var relsFiles []string
	for name := range files {
		if strings.HasPrefix(name, "xl/worksheets/_rels/") {
			relsFiles = append(relsFiles, name)
		}
	}
	sort.Strings(relsFiles)
	next := 1
	for _, name := range relsFiles {
		rels := files[name]
		c := commentsTarget.FindSubmatch(rels)
		sheet := files["xl/worksheets/"+strings.TrimSuffix(strings.TrimPrefix(name, "xl/worksheets/_rels/"), ".rels")]
		ld := legacyDrawing.FindSubmatch(sheet)
		if c == nil || ld == nil {
			continue
		}
		for _, rel := range relTarget.FindAll(rels, -1) {
			id, to := relID.FindSubmatch(rel), relTo.FindSubmatch(rel)
			if id == nil || to == nil || string(id[1]) != string(ld[1]) {
				continue
			}
			if v := vmlNumber.FindSubmatch(to[1]); v != nil {
				n := strconv.Itoa(next)
				next++
				vmlRename[string(v[1])] = n
				commentsRename[string(c[1])] = n
			}
		}
	}
	other := 100
	for _, name := range order {
		if v := vmlPart.FindStringSubmatch(name); v != nil {
			if _, ok := vmlRename[v[1]]; !ok {
				vmlRename[v[1]] = strconv.Itoa(other)
				other++
			}
		}
	}

	// Rename in two steps through placeholders, so that swaps don't collide.
	rename := func(s string) string {
		s = vmlRef.ReplaceAllStringFunc(s, func(x string) string {
			n := vmlRef.FindStringSubmatch(x)[1]
			if to, ok := vmlRename[n]; ok {
				return "vmlDrawing\x00" + to + ".vml"
			}
			return x
		})
		s = commentsRef.ReplaceAllStringFunc(s, func(x string) string {
			n := commentsRef.FindStringSubmatch(x)[1]
			if to, ok := commentsRename[n]; ok {
				return "comments\x00" + to + ".xml"
			}
			return x
		})
		return strings.ReplaceAll(s, "\x00", "")
	}
	out := map[string][]byte{}
	var names []string
	for _, name := range order {
		b := files[name]
		if strings.HasSuffix(name, ".rels") || name == "[Content_Types].xml" {
			b = []byte(rename(string(b)))
		}
		newName := rename(name)
		if _, dup := out[newName]; dup {
			return nil, fmt.Errorf("renumbering drawings: %s would be written twice", newName)
		}
		out[newName] = b
		names = append(names, newName)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(out[name]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var (
	vmlPart     = regexp.MustCompile(`^xl/drawings/vmlDrawing(\d+)\.vml$`)
	vmlRef      = regexp.MustCompile(`vmlDrawing(\d+)\.vml`)
	commentsRef = regexp.MustCompile(`comments(\d+)\.xml`)
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
