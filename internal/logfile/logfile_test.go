package logfile

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestDailyFilesAndKeep(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	now := time.Date(2026, 11, 10, 23, 59, 0, 0, time.UTC)
	// A file from an older run, and one that isn't a log, which stays.
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "messwa-2026-11-01.log"), []byte("old\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine\n"), 0o600)

	w, err := open(dir, 3, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	write := func(s string) {
		t.Helper()
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	write("a\n")
	now = now.Add(2 * time.Minute) // 11 Nov, UTC
	write("b\n")
	write("c\n")
	if got := filepath.Base(w.Path()); got != "messwa-2026-11-11.log" {
		t.Fatalf("writing to %s", got)
	}
	now = now.Add(24 * time.Hour)
	write("d\n")
	now = now.Add(24 * time.Hour)
	write("e\n")

	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"messwa-2026-11-11.log", "messwa-2026-11-12.log", "messwa-2026-11-13.log", "notes.txt"}
	if !slices.Equal(names, want) {
		t.Fatalf("files = %v, want %v", names, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "messwa-2026-11-11.log")); string(b) != "b\nc\n" {
		t.Fatalf("11 Nov = %q", b)
	}
}

// A restart on the same day appends to that day's file instead of
// starting over.
func TestReopenAppends(t *testing.T) {
	dir := t.TempDir()
	now := func() time.Time { return time.Date(2026, 11, 12, 14, 0, 0, 0, time.UTC) }
	for _, line := range []string{"before restart\n", "after restart\n"} {
		w, err := open(dir, 14, now)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(line))
		w.Close()
	}
	b, err := os.ReadFile(filepath.Join(dir, "messwa-2026-11-12.log"))
	if err != nil || string(b) != "before restart\nafter restart\n" {
		t.Fatalf("file = %q, %v", b, err)
	}
}
