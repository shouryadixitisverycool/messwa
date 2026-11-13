package wa

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"messwa/internal/config"
	"messwa/internal/ingest"
	mstore "messwa/internal/store"
	_ "modernc.org/sqlite"
)

func TestRuntimeUnlinkRetiresGenerationAndRewiresHandlers(t *testing.T) {
	cfg := config.Default()
	cfg.StateDir = t.TempDir()
	st, err := mstore.Open(cfg.DataDB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := Open(t.Context(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(t.Context(), c, ingest.New(cfg, st), st)
	defer r.Close()
	var calls atomic.Int32
	id := r.AddEventHandler(func(any) { calls.Add(1) })
	removed := r.AddEventHandler(func(any) { t.Error("removed handler called") })
	r.RemoveEventHandler(removed)
	// A generation-owned worker must finish before replacement is published.
	stopped, release := make(chan struct{}), make(chan struct{})
	c.background(func() { <-c.ctx.Done(); close(stopped); <-release })
	done := make(chan error, 1)
	go func() { done <- r.Unlink(t.Context()) }()
	<-stopped
	if err := r.Do(func(*Client) error { t.Error("retiring client escaped"); return nil }); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("did not join worker: %v", err)
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := reflect.ValueOf(c.WA).Elem().FieldByName("eventHandlers").Len(); n != 0 {
		t.Fatalf("old handlers: %d", n)
	}
	if c.ctx.Err() == nil {
		t.Fatal("old context not cancelled")
	}
	for range 2 {
		var next *Client
		if err := r.Do(func(c *Client) error { next = c; return nil }); err != nil {
			t.Fatal(err)
		}
		if next == c || next.WA == c.WA || next.WA.Store == c.WA.Store {
			t.Fatal("reused client or device")
		}
		if next.container != c.container {
			t.Fatal("left user's sqlstore container")
		}
		// linked-state, ingest, and runtime dispatcher: exactly one each.
		if n := reflect.ValueOf(next.WA).Elem().FieldByName("eventHandlers").Len(); n != 3 {
			t.Fatalf("new handlers: %d", n)
		}
		before := calls.Load()
		r.dispatch(c, struct{}{})
		r.dispatch(next, struct{}{})
		if calls.Load() != before+1 {
			t.Fatal("handler duplicated or old generation still active")
		}
		if err := r.Unlink(t.Context()); err != nil {
			t.Fatal(err)
		}
		c = next
	}
	if !r.RemoveEventHandler(id) {
		t.Fatal("handler ID lost across replacement")
	}
}

func TestRuntimeConcurrentOperationsAndUnlink(t *testing.T) {
	cfg := config.Default()
	cfg.StateDir = t.TempDir()
	st, err := mstore.Open(cfg.DataDB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := Open(t.Context(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(t.Context(), c, ingest.New(cfg, st), st)
	defer r.Close()
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				r.LoggedIn()
				r.IsConnected()
				r.Saved(ctx, "123@s.whatsapp.net")
				id := r.AddEventHandler(func(any) {})
				r.RemoveEventHandler(id)
				_ = r.Do(func(c *Client) error { c.WA.GenerateMessageID(); return nil })
			}
		}()
	}
	for range 20 {
		if err := r.Unlink(ctx); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	wg.Wait()
}

// A leased operation may need the handler registry while retirement drains it.
// Neither a pending retirement nor a whatsmeow callback may hold that up.
func TestRuntimeRetirementDrainsWithoutHoldingHandleLock(t *testing.T) {
	cfg := config.Default()
	cfg.StateDir = t.TempDir()
	st, err := mstore.Open(cfg.DataDB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := Open(t.Context(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(t.Context(), c, ingest.New(cfg, st), st)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- r.Do(func(*Client) error {
			close(entered)
			<-release
			id := r.AddEventHandler(func(any) {})
			r.RemoveEventHandler(id)
			return r.Do(func(*Client) error { return nil })
		})
	}()
	<-entered
	retired := make(chan error, 1)
	go func() { retired <- r.Unlink(t.Context()) }()
	deadline := time.After(3 * time.Second)
	for {
		probe := make(chan error, 1)
		go func() { probe <- r.Do(func(*Client) error { return nil }) }()
		select {
		case err := <-probe:
			if errors.Is(err, ErrUnavailable) {
				goto draining
			}
		case <-deadline:
			t.Fatal("retirement blocked the handle while draining a lease")
		}
	}
draining:
	select {
	case <-retired:
		t.Fatal("retired before lease finished")
	default:
	}
	close(release)
	select {
	case err := <-finished:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("nested call during retirement: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("leased operation deadlocked on handle")
	}
	select {
	case err := <-retired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retirement did not finish")
	}
	r.Close()
}
