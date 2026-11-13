package dashboard

import (
	"context"
	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
	"net/http"
	"strings"
	"sync"
)

// AccountHandler selects the private server after authenticating the session.
func AccountHandler(cfg *config.Config, root *mcpserver.Server, hub *Hub, acc *accounts.Accounts, lookup func(context.Context, int64) (*mcpserver.Server, error)) http.Handler {
	fallback := Handler(cfg, root, hub, acc)
	var mu sync.Mutex
	handlers := map[int64]http.Handler{}
	return withSession(acc, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := accounts.UserFrom(r.Context())
		if u == nil {
			fallback.ServeHTTP(w, r)
			return
		}
		srv, err := lookup(r.Context(), u.ID)
		if err != nil {
			jsonError(w, err.Error(), 503)
			return
		}
		mu.Lock()
		h := handlers[u.ID]
		if h == nil {
			h = Handler(srv.Config(), srv, hub, acc)
			handlers[u.ID] = h
		}
		mu.Unlock()
		h.ServeHTTP(w, r)
	}))
}

// perUserSetting reports whether setting k belongs to one user's WhatsApp
// rather than to everyone. Members may only save these.
func perUserSetting(k string) bool {
	return strings.HasPrefix(k, "sell.") || k == "mcp.sell_tasks" || k == "sales.monthly_fee" || k == "dms.retention"
}

func deviceRoutes(mux *http.ServeMux, srv *mcpserver.Server) {
	// New accounts link WhatsApp before an admin approves them, so these
	// take any signed-in user.
	member := signedIn
	mux.Handle("GET /api/whatsapp", member(func(w http.ResponseWriter, r *http.Request) {
		if srv.Device == nil {
			jsonError(w, "WhatsApp linking requires the run service", 503)
			return
		}
		writeJSON(w, r, srv.Device.Status())
	}))
	mux.Handle("POST /api/whatsapp", member(func(w http.ResponseWriter, r *http.Request) {
		if srv.Device == nil {
			jsonError(w, "WhatsApp linking requires the run service", 503)
			return
		}
		if err := srv.Device.Link(r.Context()); err != nil {
			jsonError(w, err.Error(), 400)
			return
		}
		writeJSON(w, r, srv.Device.Status())
	}))
	mux.Handle("POST /api/whatsapp/code", member(func(w http.ResponseWriter, r *http.Request) {
		if srv.Device == nil {
			jsonError(w, "WhatsApp linking requires the run service", 503)
			return
		}
		var in struct{ Phone string }
		if !readJSON(w, r, &in) {
			return
		}
		code, err := srv.Device.LinkCode(r.Context(), in.Phone)
		if err != nil {
			jsonError(w, err.Error(), 400)
			return
		}
		writeJSON(w, r, map[string]string{"code": code})
	}))
	mux.Handle("DELETE /api/whatsapp", member(func(w http.ResponseWriter, r *http.Request) {
		if srv.Device == nil {
			jsonError(w, "WhatsApp linking requires the run service", 503)
			return
		}
		if err := srv.Device.Unlink(r.Context()); err != nil {
			jsonError(w, err.Error(), 400)
			return
		}
		w.WriteHeader(204)
	}))
}
