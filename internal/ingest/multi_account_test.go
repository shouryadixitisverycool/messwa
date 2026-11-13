package ingest

import (
	"messwa/internal/config"
	"messwa/internal/store"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAccountsShareBoardAndDeduplicate(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	board, err := store.Open(filepath.Join(dir, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer board.Close()
	cfg := config.Default()
	cfg.Groups = []string{"allowed@g.us"}
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b"} {
		s, err := store.OpenPrivate(filepath.Join(dir, name+".db"), board)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		in := New(cfg, s)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, chat := range []string{"allowed@g.us", "private@g.us"} {
				if err := in.Handle(ctx, store.LogEntry{Chat: chat, ID: "same", Type: "post", Sender: "seller", Text: "sell palash lunch 30", TS: time.Now()}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	logs, err := board.Log(ctx, time.Time{})
	if err != nil || len(logs) != 1 {
		t.Fatalf("shared log: %+v %v", logs, err)
	}
	ls, err := board.Listings(ctx, store.SoldRule{}, store.ListingFilter{Since: time.Time{}})
	if err != nil || len(ls) != 1 {
		t.Fatalf("shared board: %+v %v", ls, err)
	}
}
