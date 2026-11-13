// Package logfile keeps messwa's own copy of its log in the state
// directory, one file per UTC day, so the log of a run survives restarts
// without anything outside tailing the container.
package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	prefix = "messwa-"
	suffix = ".log"
	// dayLayout names a file by the UTC day its lines were written,
	// matching the UTC timestamps slog puts on each line.
	dayLayout = "2006-01-02"
)

// Writer appends to <dir>/messwa-YYYY-MM-DD.log and moves to a new file
// when the UTC day changes. After each move it deletes the oldest files
// until Keep are left, today's included. It is safe for concurrent use;
// slog's handlers write one whole line per call.
type Writer struct {
	dir  string
	keep int
	now  func() time.Time

	mu   sync.Mutex
	day  string
	file *os.File
}

// Open creates dir if needed and opens today's file. keep below 1 counts
// as 1.
func Open(dir string, keep int) (*Writer, error) {
	return open(dir, keep, time.Now)
}

func open(dir string, keep int, now func() time.Time) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	w := &Writer{dir: dir, keep: max(keep, 1), now: now}
	if err := w.roll(); err != nil {
		return nil, err
	}
	return w, nil
}

// Path is the file lines go to now.
func (w *Writer) Path() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.path(w.day)
}

func (w *Writer) path(day string) string { return filepath.Join(w.dir, prefix+day+suffix) }

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.now().UTC().Format(dayLayout) != w.day || w.file == nil {
		if err := w.roll(); err != nil {
			return 0, err
		}
	}
	return w.file.Write(p)
}

// roll opens the current day's file and prunes old ones. w.mu is held, or
// w isn't shared yet.
func (w *Writer) roll() error {
	day := w.now().UTC().Format(dayLayout)
	f, err := os.OpenFile(w.path(day), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	if w.file != nil {
		w.file.Close()
	}
	w.file, w.day = f, day
	w.prune()
	return nil
}

// prune deletes the oldest day files beyond keep. A file it can't delete
// stays; the next roll tries again.
func (w *Writer) prune() {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	var days []string
	for _, e := range entries {
		day, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok || e.IsDir() {
			continue
		}
		day, ok = strings.CutSuffix(day, suffix)
		if _, err := time.Parse(dayLayout, day); !ok || err != nil {
			continue
		}
		days = append(days, day)
	}
	// The layout sorts by date as text.
	slices.Sort(days)
	for len(days) > w.keep {
		if days[0] != w.day {
			os.Remove(w.path(days[0]))
		}
		days = days[1:]
	}
}

// Close closes the current file. A nil Writer has nothing to close.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
