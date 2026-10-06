// Package logs writes RTFM's logs: everything to <dir>/rtfm.log (and
// standard error), and what concerns one loaded tournament also to
// <dir>/<id>.log, so a problem with one tournament can be read on its own.
package logs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// MaxMain is the size at which rtfm.log is moved to rtfm.log.1 and started
// again.
const MaxMain = 10 << 20

// Logs is the log folder.
type Logs struct {
	dir string
	std io.Writer
	mu  sync.Mutex
}

// Open makes the folder ("" logs to std only).
func Open(dir string, std io.Writer) (*Logs, error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return &Logs{dir: dir, std: std}, nil
}

var idRe = regexp.MustCompile(`^[0-9a-f]{10}$`)

// Printf logs a line; with an id (a tournament's) also to that id's log.
func (l *Logs) Printf(id, format string, args ...any) {
	msg := strings.TrimRight(fmt.Sprintf(format, args...), "\n")
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	line := stamp + " " + msg + "\n"
	if id != "" {
		line = stamp + " [" + id + "] " + msg + "\n"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.std != nil {
		io.WriteString(l.std, line)
	}
	if l.dir == "" {
		return
	}
	main := filepath.Join(l.dir, "rtfm.log")
	if st, err := os.Stat(main); err == nil && st.Size() > MaxMain {
		os.Rename(main, main+".1")
	}
	appendLine(main, line)
	if idRe.MatchString(id) {
		appendLine(filepath.Join(l.dir, id+".log"), stamp+" "+msg+"\n")
	}
}

// For is a Printf for one id.
func (l *Logs) For(id string) func(format string, args ...any) {
	return func(format string, args ...any) { l.Printf(id, format, args...) }
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	f.WriteString(line)
	f.Close()
}

// Remove deletes an id's log (when its tournament is forgotten).
func (l *Logs) Remove(id string) {
	if l.dir != "" && idRe.MatchString(id) {
		os.Remove(filepath.Join(l.dir, id+".log"))
	}
}
