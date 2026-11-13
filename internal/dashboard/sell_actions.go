package dashboard

import (
	"net/http"

	"messwa/internal/accounts"
	"messwa/internal/mcpserver"
)

// sellActionRoutes shares the task operations with MCP. Session middleware
// applies the same origin and CSRF checks as every other dashboard write.
func sellActionRoutes(mux *http.ServeMux, srv *mcpserver.Server) {
	mux.Handle("POST /api/sell-action", requireRole(accounts.Member, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Action string `json:"action"`
			ID     int64  `json:"id"`
			Buyer  string `json:"buyer"`
			Name   string `json:"name"`
			Price  int    `json:"price"`
			Note   string `json:"note"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if !srv.Scoped() && !accounts.UserFrom(r.Context()).IsAdmin() {
			jsonError(w, "admins only", http.StatusForbidden)
			return
		}
		op, err := srv.SellOperator()
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch in.Action {
		case "sold":
			err = op.MarkSold(r.Context(), in.ID, in.Buyer, in.Name, in.Price, in.Note)
		case "add":
			err = op.AddBuyer(r.Context(), in.ID, in.Buyer, in.Name)
		case "mute", "unmute":
			err = op.MuteBuyer(r.Context(), in.ID, in.Buyer, in.Action == "mute")
		default:
			jsonError(w, "unknown sell action", http.StatusBadRequest)
			return
		}
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, r, struct{}{})
	})))
}
