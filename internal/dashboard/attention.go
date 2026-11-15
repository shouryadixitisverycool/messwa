package dashboard

import (
	"messwa/internal/accounts"
	"messwa/internal/mcpserver"
	"net/http"
	"strconv"
)

func attentionRoutes(mux *http.ServeMux, srv *mcpserver.Server) {
	wrap := func(fn http.HandlerFunc) http.Handler {
		return requireRole(accounts.Member, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !srv.Scoped() && !accounts.UserFrom(r.Context()).IsAdmin() {
				jsonError(w, "admins only", 403)
				return
			}
			fn(w, r)
		}))
	}
	mux.Handle("GET /api/attention", wrap(func(w http.ResponseWriter, r *http.Request) {
		items, err := srv.Attention(r.Context(), accounts.UserFrom(r.Context()).ID)
		if err != nil {
			jsonError(w, "Could not load attention items", 500)
			return
		}
		writeJSON(w, r, items)
	}))
	mux.Handle("POST /api/attention", wrap(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID string `json:"id"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		items, err := srv.Attention(r.Context(), accounts.UserFrom(r.Context()).ID)
		if err != nil {
			jsonError(w, "Could not load attention items", 500)
			return
		}
		for _, item := range items {
			if item.ID == in.ID && item.Resolvable {
				if err := srv.ResolveAttention(r.Context(), accounts.UserFrom(r.Context()).ID, in.ID); err != nil {
					jsonError(w, "Could not save resolution", 500)
					return
				}
				writeJSON(w, r, struct{}{})
				return
			}
		}
		jsonError(w, "Issue changed. Refresh and try again.", 409)
	}))
	mux.Handle("GET /api/attention-sale", wrap(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		v, err := srv.AttentionSale(r.Context(), id)
		if err != nil {
			jsonError(w, "Could not load sale", 500)
			return
		}
		if v == nil {
			jsonError(w, "Sale not found", 404)
			return
		}
		writeJSON(w, r, v)
	}))
}
