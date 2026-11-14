package dashboard

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"messwa/internal/accounts"
	marketask "messwa/internal/market"
	"messwa/internal/mcpserver"
	"messwa/internal/meals"
	"messwa/internal/store"
)

// Live now on the usage page: every meal users are selling through messwa
// at this moment. It only reads each user's own database, never a portal,
// so it is cheap enough to rebuild whenever /api/usage/events fires.

// liveReuse collapses rebuilds from several open admin tabs that all
// refetch on the same event.
const liveReuse = time.Second

// liveMarketTTL is how long a meal's median ask is reused. It reads four
// weeks of prices, and moves slowly.
const liveMarketTTL = 2 * time.Minute

type liveState struct {
	Now   int64        `json:"now"`
	Meals []meals.Slot `json:"meals"`
	Tasks []liveTask   `json:"tasks"`
}

// liveTask is one active sell task. Buyers are counts only.
type liveTask struct {
	User      string `json:"user"`
	Linked    bool   `json:"linked"`
	Connected bool   `json:"connected"`
	Date      string `json:"date"`
	Meal      string `json:"meal"`
	Mess      string `json:"mess"`
	Diet      string `json:"diet"`
	// Min and Max are rupees; group posts ask Max.
	Min     float64  `json:"min"`
	Max     float64  `json:"max"`
	Market  *float64 `json:"market,omitempty"`
	Handoff string   `json:"handoff"`
	Created int64    `json:"created"`
	Ends    int64    `json:"ends"`
	// FirstPost is the first group post; zero while nothing has gone up.
	FirstPost int64 `json:"first_post,omitempty"`
	// Groups have a live post, Failed only failed ones.
	Groups  int `json:"groups"`
	Failed  int `json:"failed"`
	Reposts int `json:"reposts"`
	// InLine buyers replied and are waiting or haggling; Paying have the
	// payment prompt, the first of them due by PayUntil.
	InLine    int    `json:"in_line"`
	Paying    int    `json:"paying"`
	PayUntil  int64  `json:"pay_until,omitempty"`
	LastReply int64  `json:"last_reply,omitempty"`
	Error     string `json:"error,omitempty"`
}

type liveMedian struct {
	at time.Time
	v  float64
	ok bool
}

type liveSource struct {
	root   *mcpserver.Server
	acc    *accounts.Accounts
	lookup func(context.Context, int64) (*mcpserver.Server, error)

	mu     sync.Mutex
	at     time.Time
	state  *liveState
	median map[string]liveMedian
}

func newLive(root *mcpserver.Server, acc *accounts.Accounts, lookup func(context.Context, int64) (*mcpserver.Server, error)) *liveSource {
	return &liveSource{root: root, acc: acc, lookup: lookup, median: map[string]liveMedian{}}
}

func (l *liveSource) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state == nil || time.Since(l.at) >= liveReuse {
		st, err := l.build(r.Context())
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		l.state, l.at = st, time.Now()
	}
	writeJSON(w, r, l.state)
}

// build runs with l.mu held.
func (l *liveSource) build(ctx context.Context) (*liveState, error) {
	now := l.root.Now()
	out := &liveState{Now: now.Unix(), Meals: l.root.Schedule().Today, Tasks: []liveTask{}}
	users, err := l.acc.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range users {
		us := &users[i]
		if us.Disabled || us.Status != accounts.Active {
			continue
		}
		srv, err := l.lookup(ctx, us.ID)
		if err != nil {
			continue
		}
		tasks, err := srv.SellTasksSince(ctx, now)
		if err != nil {
			continue
		}
		var linked, connected bool
		if srv.Device != nil {
			linked = srv.Device.Linked()
			if st, ok := srv.Device.Status().(map[string]any); ok {
				connected, _ = st["connected"].(bool)
			}
		}
		for _, t := range tasks {
			if t.State != store.TaskActive {
				continue
			}
			lt := l.task(ctx, srv, us.ID, t)
			lt.User, lt.Linked, lt.Connected = us.Name, linked, connected
			out.Tasks = append(out.Tasks, lt)
		}
	}
	return out, nil
}

func (l *liveSource) task(ctx context.Context, srv *mcpserver.Server, uid int64, t store.SellTask) liveTask {
	lt := liveTask{Date: t.Date, Meal: t.Meal, Mess: t.Mess, Diet: t.Diet, Min: float64(t.MinPaise) / 100, Max: float64(t.MaxPaise) / 100,
		Handoff: t.Handoff, Created: unix(t.CreatedAt), Ends: unix(t.EndsAt)}
	if v, ok := l.marketAsk(ctx, srv, uid, t); ok {
		lt.Market = &v
	}
	offers, err := srv.SellTaskOffers(ctx, t.ID)
	if err != nil {
		lt.Error = err.Error()
		return lt
	}
	posted, failed := map[string]bool{}, map[string]bool{}
	for _, o := range offers {
		switch o.State {
		case store.OfferPosted:
			posted[o.Group] = true
			if o.Seq > 0 {
				lt.Reposts++
			}
			if p := unix(o.PostedAt); p != 0 && (lt.FirstPost == 0 || p < lt.FirstPost) {
				lt.FirstPost = p
			}
		case store.OfferFailed:
			failed[o.Group] = true
		}
	}
	lt.Groups = len(posted)
	for g := range failed {
		if !posted[g] {
			lt.Failed++
		}
	}
	buyers, err := srv.SellTaskBuyers(ctx, t.ID)
	if err != nil {
		lt.Error = err.Error()
		return lt
	}
	for _, b := range buyers {
		lt.LastReply = max(lt.LastReply, unix(b.JoinedAt))
		switch b.State {
		case store.BuyerOffered:
			lt.Paying++
			if u := unix(b.PayUntil); u != 0 && (lt.PayUntil == 0 || u < lt.PayUntil) {
				lt.PayUntil = u
			}
		case store.BuyerQueued:
			lt.InLine++
		}
	}
	return lt
}

// marketAsk is the median ask the task's own negotiation would use.
func (l *liveSource) marketAsk(ctx context.Context, srv *mcpserver.Server, uid int64, t store.SellTask) (float64, bool) {
	meal, _, _ := strings.Cut(t.Meal, "#")
	key := strings.Join([]string{strconv.FormatInt(uid, 10), t.Date, meal, t.Mess, t.Diet}, "|")
	if m, ok := l.median[key]; ok && time.Since(m.at) < liveMarketTTL {
		return m.v, m.ok
	}
	m := liveMedian{at: time.Now()}
	if day, err := time.Parse("2006-01-02", t.Date); err == nil {
		if ps, err := srv.Prices(ctx, day.Add(-marketask.Window).Format("2006-01-02"), t.Date); err == nil {
			m.v, m.ok = marketask.MedianAsk(ps, t.Date, meal, t.Mess, t.Diet)
		}
	}
	l.median[key] = m
	return m.v, m.ok
}

// liveEvents streams a "live" event when listings, a sale, a WhatsApp
// connection or an account changes. The page refetches /api/usage/live.
func liveEvents(hub *Hub, acc *accounts.Accounts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hc, hdone := hub.subscribe()
		defer hdone()
		ac, adone := acc.Watch(func(int64) bool { return true })
		defer adone()
		c := make(chan struct{}, 1)
		go func() {
			for {
				select {
				case <-r.Context().Done():
					return
				case <-hc:
				case <-ac:
				}
				select {
				case c <- struct{}{}:
				default:
				}
			}
		}()
		stream(w, r, c, "live")
	}
}
