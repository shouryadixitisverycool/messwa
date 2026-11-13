package wa

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	"messwa/internal/ingest"
	mstore "messwa/internal/store"
)

var ErrUnavailable = errors.New("WhatsApp client is being replaced or closed")

type runtimeHandler struct {
	fn     whatsmeow.EventHandler
	active atomic.Bool
}

// Runtime is the stable per-account handle. Raw clients never escape Do;
// each call leases one generation until it returns. Retirement first removes
// the generation from service and drains leases, then tears it down outside
// mu (event callbacks may themselves use the handle).
type Runtime struct {
	lifecycle sync.Mutex
	mu        sync.RWMutex
	leases    sync.WaitGroup
	client    *Client
	ctx       context.Context
	in        *ingest.Ingester
	st        *mstore.Store
	handlers  map[uint32]*runtimeHandler
	nextID    uint32
}

func NewRuntime(ctx context.Context, c *Client, in *ingest.Ingester, st *mstore.Store) *Runtime {
	r := &Runtime{ctx: ctx, client: c, in: in, st: st, handlers: make(map[uint32]*runtimeHandler)}
	c.Ingest(in, st)
	r.install(c)
	return r
}

func (r *Runtime) Do(fn func(*Client) error) error {
	r.mu.RLock()
	if r.client == nil {
		r.mu.RUnlock()
		return ErrUnavailable
	}
	c := r.client
	r.leases.Add(1)
	r.mu.RUnlock()
	defer r.leases.Done()
	return fn(c)
}

func (r *Runtime) LoggedIn() (linked bool) {
	_ = r.Do(func(c *Client) error { linked = c.LoggedIn(); return nil })
	return
}
func (r *Runtime) IsConnected() (connected bool) {
	_ = r.Do(func(c *Client) error { connected = c.WA.IsConnected(); return nil })
	return
}
func (r *Runtime) Saved(ctx context.Context, jids ...string) (saved bool, name string) {
	_ = r.Do(func(c *Client) error {
		if c.LoggedIn() {
			saved, name = c.Saved(ctx, jids...)
		}
		return nil
	})
	return
}
func (r *Runtime) Self(ctx context.Context) (jid, alt, name string) {
	_ = r.Do(func(c *Client) error { jid, alt, name = c.Self(ctx); return nil })
	return
}
func (r *Runtime) WaitForConnection(timeout time.Duration) (ok bool) {
	_ = r.Do(func(c *Client) error { ok = c.WA.WaitForConnection(timeout); return nil })
	return
}

func (r *Runtime) AddEventHandler(fn whatsmeow.EventHandler) uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	h := &runtimeHandler{fn: fn}
	h.active.Store(true)
	r.handlers[r.nextID] = h
	return r.nextID
}

// One dispatcher per generation avoids taking whatsmeow's handler-list lock
// while holding mu. A callback is allowed to call back into this runtime.
func (r *Runtime) install(c *Client) {
	c.WA.AddEventHandler(func(evt any) { r.dispatch(c, evt) })
}
func (r *Runtime) dispatch(c *Client, evt any) {
	r.mu.RLock()
	ids := make([]uint32, 0, len(r.handlers))
	for id := range r.handlers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	handlers := make([]*runtimeHandler, 0, len(ids))
	for _, id := range ids {
		handlers = append(handlers, r.handlers[id])
	}
	r.mu.RUnlock()
	for _, h := range handlers {
		if c.ctx.Err() == nil && h.active.Load() {
			h.fn(evt)
		}
	}
}
func (r *Runtime) RemoveEventHandler(id uint32) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.handlers[id]
	if h == nil {
		return false
	}
	h.active.Store(false)
	delete(r.handlers, id)
	return true
}

// Reconnect owns generation-scoped retry work; unlink cancels and joins it.
func (r *Runtime) Reconnect() {
	_ = r.Do(func(c *Client) error {
		c.background(func() {
			for c.ctx.Err() == nil {
				if err := c.WA.ConnectContext(c.ctx); err == nil {
					return
				}
				select {
				case <-c.ctx.Done():
					return
				case <-time.After(30 * time.Second):
				}
			}
		})
		return nil
	})
}

func (r *Runtime) Unlink(ctx context.Context) error {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	r.mu.Lock()
	old := r.client
	r.client = nil
	r.mu.Unlock()
	if old == nil {
		return ErrUnavailable
	}
	r.leases.Wait()
	if old.LoggedIn() {
		if err := old.WA.Logout(ctx); err != nil {
			r.mu.Lock()
			r.client = old
			r.mu.Unlock()
			return err
		}
	}
	old.stop()
	// Logout deletes a linked device. A previously remotely logged-out device
	// is also replaced. NewClient rebuilds app-state and every internal cache.
	next := newClient(r.ctx, old.container, old.container.NewDevice(), old.cfg, old.log)
	next.Ingest(r.in, r.st)
	r.install(next)
	r.mu.Lock()
	r.client = next
	r.mu.Unlock()
	return nil
}

func (r *Runtime) Close() {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	r.mu.Lock()
	old := r.client
	r.client = nil
	r.mu.Unlock()
	if old != nil {
		r.leases.Wait()
		old.Close()
	}
}
