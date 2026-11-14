package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
)

// ErrConnecting is what a groupLister returns while the WhatsApp it asks
// is still connecting. The welcome page asks again a few seconds later.
var ErrConnecting = errors.New("WhatsApp is still connecting")

// groupLister is the part of a DeviceAccount that lists the groups its
// WhatsApp has joined, JID to name.
type groupLister interface {
	JoinedGroups(ctx context.Context) (map[string]string, error)
}

type setupGroup struct {
	JID    string `json:"jid"`
	Name   string `json:"name"`
	Joined bool   `json:"joined"`
	Picked bool   `json:"picked"`
}

// setupSell is the welcome page's selling step: the configured mess
// groups, which ones this WhatsApp is in, and the two choices that shape a
// first sale.
type setupSell struct {
	Groups []setupGroup `json:"groups"`
	// Checked is false while the joined list couldn't be read; Joined is
	// then false for every group. Connecting says to ask again soon.
	Checked    bool   `json:"checked"`
	Connecting bool   `json:"connecting,omitempty"`
	Error      string `json:"error,omitempty"`
	// Saved is true once the account picked its groups.
	Saved   bool   `json:"saved"`
	Handoff string `json:"handoff"`
	Buyers  string `json:"buyers"`
}

// picksGroups reports whether c names the groups an offer goes to. Without
// that, a sell task falls back to the first configured group, which a new
// account may not be in.
func picksGroups(c *config.Config) bool {
	return len(c.Sell.Groups) > 0 || c.Sell.GroupCount > 0
}

// onboardingRoutes adds /api/setup-sell, which the welcome page uses to
// pick groups, handoff and buyers before an admin approves the account.
// Any signed-in user may use it: it only touches their own selling
// settings.
func onboardingRoutes(mux *http.ServeMux, cfg *config.Config, srv *mcpserver.Server) {
	state := func(w http.ResponseWriter, r *http.Request) {
		live := cfg.Live()
		out := setupSell{Groups: []setupGroup{}, Saved: picksGroups(live),
			Handoff: string(live.Sell.Handoff), Buyers: string(live.Sell.Buyers)}
		if out.Handoff == "" {
			out.Handoff = string(config.HandoffNever)
		}
		if out.Buyers == "" {
			out.Buyers = string(config.BuyersAnyone)
		}
		names, err := srv.GroupNames(r.Context())
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var joined map[string]string
		if gl, ok := srv.Device.(groupLister); ok && srv.Device.Linked() {
			joined, err = gl.JoinedGroups(r.Context())
			switch {
			case errors.Is(err, ErrConnecting):
				out.Connecting = true
			case err != nil:
				out.Error = err.Error()
			default:
				out.Checked = true
			}
		}
		picked := map[string]bool{}
		for _, g := range live.Sell.Groups {
			picked[g] = true
		}
		for i, g := range cfg.Groups {
			name, in := joined[g]
			if name == "" {
				name = names[g]
			}
			p := picked[g] || picked[name] || (len(live.Sell.Groups) == 0 && i < live.Sell.GroupCount)
			out.Groups = append(out.Groups, setupGroup{JID: g, Name: name, Joined: in, Picked: p})
		}
		writeJSON(w, r, out)
	}
	mux.Handle("GET /api/setup-sell", signedIn(state))

	// PUT saves the picked groups (JIDs from the configured list, in
	// posting order) with handoff and buyers, keeping the account's other
	// saved settings.
	mux.Handle("PUT /api/setup-sell", signedIn(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Groups  []string `json:"groups"`
			Handoff string   `json:"handoff"`
			Buyers  string   `json:"buyers"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if len(in.Groups) == 0 {
			jsonError(w, "pick at least one group to post in", http.StatusBadRequest)
			return
		}
		known := map[string]bool{}
		for _, g := range cfg.Groups {
			known[g] = true
		}
		for _, g := range in.Groups {
			if !known[g] {
				jsonError(w, "unknown group "+g, http.StatusBadRequest)
				return
			}
		}
		saved, err := srv.Settings(r.Context())
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		all := config.Settings{}
		for k, v := range saved {
			all[k] = v
		}
		raw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
		all["sell.groups"] = raw(in.Groups)
		// Groups and group_count can't both be set, and config.toml may
		// give a count.
		all["sell.group_count"] = raw(0)
		all["sell.handoff"] = raw(in.Handoff)
		all["sell.buyers"] = raw(in.Buyers)
		if err := srv.SaveSettings(r.Context(), all, accounts.UserFrom(r.Context()).ID); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		state(w, r)
	}))
}
