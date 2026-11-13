package dashboard

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Hub tells open dashboards that the listings changed, so they fetch the
// board right away instead of on a timer.
type Hub struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[chan struct{}]struct{}{}} }

// Notify wakes every subscriber. It never blocks: a subscriber that hasn't
// handled the last signal yet just gets one signal for both.
func (h *Hub) Notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

func (h *Hub) subscribe() (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[c] = struct{}{}
	h.mu.Unlock()
	return c, func() {
		h.mu.Lock()
		delete(h.subs, c)
		h.mu.Unlock()
	}
}

// events streams a "change" event each time the hub fires.
func (h *Hub) events(w http.ResponseWriter, r *http.Request) {
	c, done := h.subscribe()
	defer done()
	stream(w, r, c, "change")
}

// stream sends an SSE event named name each time c signals, and a ping
// every 20 seconds so proxies keep the connection open. Signals that land
// close together, such as a post and its copies in other groups, go out as
// one event.
func stream(w http.ResponseWriter, r *http.Request, c <-chan struct{}, name string) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, "retry: 3000\n\n")
	if err := rc.Flush(); err != nil {
		return
	}
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		msg := ": ping\n\n"
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
		case <-c:
			select {
			case <-time.After(200 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
			select {
			case <-c:
			default:
			}
			msg = "event: " + name + "\ndata: \n\n"
		}
		if _, err := fmt.Fprint(w, msg); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}
