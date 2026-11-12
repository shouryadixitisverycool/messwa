// Package dashboard serves a read-only web view of the merged listings,
// laid out like an auction house: messes down the left, listings in a grid,
// sort and filter buttons along the bottom.
//
// It shows sellers' names and phone numbers from the groups. From DMs it
// shows admins only their own sell tasks' buyers: names, numbers, and what
// the bot read from their messages, never the messages themselves. People
// sign in with a messwa account; the only write endpoints are for their own
// account (password, mess API key, MCP tokens) and, for admins, the settings
// page.
package dashboard

import (
	"compress/gzip"
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
	"messwa/internal/meals"
)

//go:embed static
var static embed.FS

type mess struct {
	Name    string `json:"name"`
	VegOnly bool   `json:"veg_only"`
}

type slot struct {
	Meal  string `json:"meal"`
	Date  string `json:"date"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
}

type state struct {
	Now      int64                `json:"now"`
	TZ       string               `json:"tz"`
	Messes   []mess               `json:"messes"`
	Today    []slot               `json:"today"`
	Current  *slot                `json:"current,omitempty"`
	Next     *slot                `json:"next,omitempty"`
	Listings []mcpserver.FeedItem `json:"listings"`
	Market   market               `json:"market"`
	// Mine is today's meals you armed or sold with deliver-qr, for the coins
	// on the day track.
	Mine []mine `json:"mine"`
	// Sales are your running sell tasks, and ones that just ended, for the
	// "my sale" chest. Admins only, like the rest of the linked account.
	Sales []mcpserver.SaleView `json:"sales,omitempty"`
}

type mine struct {
	Meal   string  `json:"meal"`
	State  string  `json:"state"` // sold, sending, armed, handoff
	Amount float64 `json:"amount"`
}

// marketSpan is how far back the stats panel looks.
const marketSpan = 2 * time.Hour

// market is raw activity for the stats panel. The page buckets and rates it,
// so the stats move with the clock between fetches.
type market struct {
	Since int64        `json:"since"`
	Posts []marketPost `json:"posts"`
	Sales []marketSale `json:"sales"`
}

type marketPost struct {
	T     int64  `json:"t"`
	ID    int64  `json:"id"`
	Mess  string `json:"mess"`
	Kind  string `json:"kind"`
	Price *int   `json:"price,omitempty"`
}

type marketSale struct {
	T     int64  `json:"t"`
	First int64  `json:"first"`
	ID    int64  `json:"id"`
	Mess  string `json:"mess"`
	Price *int   `json:"price,omitempty"`
}

// Handler serves the page at / and its data at /api/state and /api/listing.
// /api/events is a server-sent event stream that fires when hub does.
// Everything except the sign-in page needs a session from acc; see
// pageRoles and requireRole.
func Handler(cfg *config.Config, srv *mcpserver.Server, hub *Hub, acc *accounts.Accounts) http.Handler {
	mux := http.NewServeMux()
	files, _ := fs.Sub(static, "static")
	mux.Handle("GET /", pages(http.FileServerFS(files)))
	member := func(h http.HandlerFunc) http.Handler { return requireRole(accounts.Member, h) }
	regs := newRegCache(acc)
	authRoutes(mux, acc, regs, "http://"+cfg.MCP.Listen+"/mcp")
	settingsRoutes(mux, cfg, srv)

	mux.Handle("GET /api/events", member(hub.events))

	mux.Handle("GET /api/state", member(func(w http.ResponseWriter, r *http.Request) {
		now := srv.Now()
		items, err := srv.Feed(r.Context(), now.Add(-30*time.Hour))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		st := state{Now: now.Unix(), TZ: cfg.Timezone, Listings: items}
		if st.Market, err = marketState(r.Context(), srv, now.Add(-marketSpan)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, m := range cfg.Messes {
			st.Messes = append(st.Messes, mess{m.Name, m.VegOnly})
		}
		// Meals armed with deliver-qr belong to the linked WhatsApp account.
		st.Mine = []mine{}
		if accounts.UserFrom(r.Context()).IsAdmin() {
			if st.Mine, err = mineToday(r.Context(), cfg, srv); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if st.Sales, err = srv.SaleViews(r.Context()); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		sc := srv.Schedule()
		for _, s := range sc.Today {
			st.Today = append(st.Today, toSlot(&s))
		}
		if sc.Current != nil {
			c := toSlot(sc.Current)
			st.Current = &c
		}
		if sc.Next != nil {
			n := toSlot(sc.Next)
			st.Next = &n
		}
		writeJSON(w, r, st)
	}))

	mux.Handle("GET /api/sales", member(salesHandler(cfg, srv, regs)))
	mux.Handle("GET /api/insights", requireRole(accounts.Admin, insightsHandler(cfg, srv)))

	beeper := newBeeperIndex(cfg.Dash.BeeperDB)
	mux.Handle("GET /api/listing", member(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		out, err := srv.Get(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		resp := listingResp{GetOut: out}
		// Only browsers set to Beeper ask for links, so WhatsApp users
		// never touch Beeper's database.
		if r.URL.Query().Get("beeper") == "1" && beeper != nil {
			posts := make([]beeperPost, len(out.Copies))
			for i, c := range out.Copies {
				posts[i] = beeperPost{Chat: c.Chat, TS: c.TS}
			}
			// A failed lookup only loses the links, so the listing
			// still goes out.
			if links, err := beeper.Links(r.Context(), posts, out.Senders); err == nil {
				resp.Beeper = make([]string, len(out.Copies))
				for i, l := range links {
					resp.Beeper[i] = l
				}
			} else {
				w.Header().Set("X-Beeper-Error", err.Error())
			}
		}
		writeJSON(w, r, resp)
	}))

	return headers(withSession(acc, mux))
}

// listingResp is /api/listing's answer. Beeper, when asked for, holds a
// beeper:// link per copy, in the same order, or "" for copies Beeper
// doesn't have.
type listingResp struct {
	mcpserver.GetOut
	Beeper []string `json:"beeper,omitempty"`
}

func mineToday(ctx context.Context, cfg *config.Config, srv *mcpserver.Server) ([]mine, error) {
	ds, err := srv.Deliveries(ctx)
	out := []mine{}
	if err != nil {
		return out, err
	}
	now := srv.Now()
	today := now.In(cfg.Location()).Format("2006-01-02")
	for _, d := range ds {
		if d.Date != today {
			continue
		}
		state := map[string]string{"sent": "sold", "sending": "sending", "armed": "armed", "handed_off": "handoff"}[d.State]
		if state == "" {
			continue
		}
		amount, _ := strconv.ParseFloat(d.Amount, 64)
		out = append(out, mine{Meal: d.Meal, State: state, Amount: amount})
	}
	return out, nil
}

func marketState(ctx context.Context, srv *mcpserver.Server, since time.Time) (market, error) {
	posts, sales, err := srv.Market(ctx, since)
	m := market{Since: since.Unix(), Posts: []marketPost{}, Sales: []marketSale{}}
	if err != nil {
		return m, err
	}
	for _, p := range posts {
		m.Posts = append(m.Posts, marketPost{T: p.TS.Unix(), ID: p.ListingID, Mess: p.Mess, Kind: p.Kind, Price: price(p.Price)})
	}
	for _, s := range sales {
		m.Sales = append(m.Sales, marketSale{T: s.TS.Unix(), First: s.FirstSeen.Unix(), ID: s.ListingID, Mess: s.Mess, Price: price(s.Price)})
	}
	return m, nil
}

func price(p int) *int {
	if p < 0 {
		return nil
	}
	return &p
}

func toSlot(s *meals.Slot) slot {
	return slot{Meal: s.Meal, Date: s.Date, Start: s.Start.Unix(), End: s.End.Unix()}
}

func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Accept-Encoding")
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		json.NewEncoder(w).Encode(v)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	gz := gzip.NewWriter(w)
	json.NewEncoder(gz).Encode(v)
	gz.Close()
}

// headers locks the page down: no framing, no third-party scripts, and
// nothing the posts contain can load or run code.
func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
