package dashboard

import (
	"encoding/json"
	"net/http"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
)

type settingsGroup struct {
	JID  string `json:"jid"`
	Name string `json:"name"`
}

// settingsState is what the settings page shows. Values are the settings
// in use now and Defaults the ones config.toml gives; Saved lists the keys
// the page overrides. Restart lists keys saved with a value run won't use
// until it restarts.
type settingsState struct {
	Values   map[string]any  `json:"values"`
	Defaults map[string]any  `json:"defaults"`
	Saved    []string        `json:"saved"`
	Restart  []string        `json:"restart"`
	Groups   []settingsGroup `json:"groups"`
	Meals    []string        `json:"meals"`
	Messes   []string        `json:"messes"`
}

// settingsRoutes adds the settings API. srv and cfg are the signed-in
// user's own, so the selling settings it saves (see perUserSetting) apply
// to their WhatsApp only. Members see and save only those; admins also get
// the shared ones.
func settingsRoutes(mux *http.ServeMux, cfg *config.Config, srv *mcpserver.Server) {
	member := func(h http.HandlerFunc) http.Handler { return requireRole(accounts.Member, h) }
	mine := func(r *http.Request, k string) bool {
		return perUserSetting(k) || accounts.UserFrom(r.Context()).IsAdmin()
	}
	only := func(r *http.Request, m map[string]any) map[string]any {
		for k := range m {
			if !mine(r, k) {
				delete(m, k)
			}
		}
		return m
	}

	state := func(w http.ResponseWriter, r *http.Request) {
		saved, err := srv.Settings(r.Context())
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		names, err := srv.GroupNames(r.Context())
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		live := cfg.Live()
		out := settingsState{Values: only(r, live.Values()), Defaults: only(r, cfg.File().Values()),
			Saved: []string{}, Restart: []string{}, Groups: []settingsGroup{}, Meals: []string{}, Messes: []string{}}
		for _, k := range config.SettingKeys() {
			if !mine(r, k) {
				continue
			}
			if _, ok := saved[k]; ok {
				out.Saved = append(out.Saved, k)
			}
			// cfg is what run started with; live has every save since.
			if config.NeedsRestart(k) && !config.SameValue(k, cfg, live) {
				out.Restart = append(out.Restart, k)
			}
		}
		for _, g := range cfg.Groups {
			out.Groups = append(out.Groups, settingsGroup{JID: g, Name: names[g]})
		}
		for _, m := range cfg.Meals {
			out.Meals = append(out.Meals, m.Name)
		}
		for _, m := range cfg.Messes {
			out.Messes = append(out.Messes, m.Name)
		}
		writeJSON(w, r, out)
	}

	mux.Handle("GET /api/settings", member(state))

	// PUT replaces every saved setting: keys left out go back to
	// config.toml.
	mux.Handle("PUT /api/settings", member(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Values map[string]json.RawMessage `json:"values"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if in.Values == nil {
			in.Values = map[string]json.RawMessage{}
		}
		for k := range in.Values {
			if !mine(r, k) {
				jsonError(w, "only admins change "+k, http.StatusForbidden)
				return
			}
		}
		if err := srv.SaveSettings(r.Context(), config.Settings(in.Values), accounts.UserFrom(r.Context()).ID); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		state(w, r)
	}))
}
