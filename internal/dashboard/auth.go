package dashboard

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"messwa/internal/accounts"
)

const cookieName = "messwa_session"

// pageRoles is the role each page needs. Paths not listed (the sign-in page,
// styles, scripts, fonts, icons) are public: they hold no data.
var pageRoles = map[string]accounts.Role{
	"/":              accounts.Member,
	"/index.html":    accounts.Member,
	"/sales.html":    accounts.Member,
	"/account.html":  accounts.Member,
	"/settings.html": accounts.Member,
	"/insights.html": accounts.Admin,
}

type sessKey struct{}

// session is the signed-in browser behind a request.
type session struct {
	*accounts.Session
	token string
}

func sessionFrom(ctx context.Context) *session {
	s, _ := ctx.Value(sessKey{}).(*session)
	return s
}

// withSession looks up the session cookie, puts the user in the request
// context, and checks every request that changes something: it must come
// from this site (Origin), and, once signed in, carry the session's CSRF
// token in X-CSRF-Token.
func withSession(acc *accounts.Accounts, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sess *session
		if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
			s, err := acc.Session(r.Context(), c.Value)
			if err != nil {
				jsonError(w, "session lookup failed", http.StatusInternalServerError)
				return
			}
			if s != nil {
				sess = &session{Session: s, token: c.Value}
				ctx := context.WithValue(r.Context(), sessKey{}, sess)
				r = r.WithContext(accounts.WithUser(ctx, s.User))
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !sameOrigin(r) {
				jsonError(w, "cross-site request refused", http.StatusForbidden)
				return
			}
			if sess != nil && r.URL.Path != "/api/login" {
				got := r.Header.Get("X-CSRF-Token")
				if subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRF)) != 1 {
					jsonError(w, "missing or stale CSRF token; reload the page", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin reports whether a request came from a page on this host.
// Browsers send Origin on every POST, PUT and DELETE from fetch.
func sameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		_, host, ok := strings.Cut(o, "://")
		return ok && host == r.Host
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

// requireRole serves h only to signed-in users whose role allows need:
// 401 without a session, 403 with the wrong role. Admins pass every check.
//
//	mux.Handle("GET /api/insights", requireRole(accounts.Admin, insightsHandler(cfg, srv)))
func requireRole(need accounts.Role, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := accounts.UserFrom(r.Context())
		if u == nil {
			jsonError(w, "sign in first", http.StatusUnauthorized)
			return
		}
		if !u.Role.Allows(need) {
			jsonError(w, "admins only", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// pages serves static files, sending visitors without a session to the
// sign-in page and refusing pages their role doesn't allow.
func pages(files http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		need, gated := pageRoles[r.URL.Path]
		if gated {
			u := accounts.UserFrom(r.Context())
			if u == nil {
				next := strings.TrimPrefix(r.URL.Path, "/")
				to := "/login.html"
				if next != "" && next != "index.html" {
					to += "?next=" + next
				}
				http.Redirect(w, r, to, http.StatusSeeOther)
				return
			}
			if !u.Role.Allows(need) {
				http.Error(w, "This page is for admins only.", http.StatusForbidden)
				return
			}
			// Pages differ by who's signed in; never serve one from cache.
			w.Header().Set("Cache-Control", "no-store")
		}
		files.ServeHTTP(w, r)
	})
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		jsonError(w, "bad request body", http.StatusBadRequest)
		return false
	}
	return true
}

type meResp struct {
	*accounts.User
	CSRF         string `json:"csrf"`
	CanStoreKeys bool   `json:"can_store_keys"`
	MCPURL       string `json:"mcp_url"`
}

func setCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil,
	})
}

// authRoutes adds sign-in, the account page's API, and MCP token management.
func authRoutes(mux *http.ServeMux, acc *accounts.Accounts, regs *regCache, mcpURL string) {
	member := func(h http.HandlerFunc) http.Handler { return requireRole(accounts.Member, h) }

	mux.HandleFunc("GET /api/setup", func(w http.ResponseWriter, r *http.Request) {
		admin, err := acc.FirstAdmin(r.Context())
		if err != nil {
			jsonError(w, "lookup failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, r, map[string]bool{"needs_admin": admin == nil})
	})

	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Name, Password string }
		if !readJSON(w, r, &in) {
			return
		}
		from, _, _ := net.SplitHostPort(r.RemoteAddr)
		tok, s, err := acc.Login(r.Context(), strings.TrimSpace(in.Name), in.Password, from)
		switch {
		case errors.Is(err, accounts.ErrBadLogin), errors.Is(err, accounts.ErrLocked):
			jsonError(w, err.Error(), http.StatusUnauthorized)
			return
		case err != nil:
			jsonError(w, "sign-in failed", http.StatusInternalServerError)
			return
		}
		if old := sessionFrom(r.Context()); old != nil {
			acc.Logout(r.Context(), old.token)
		}
		setCookie(w, r, tok, int(accounts.SessionTTL().Seconds()))
		writeJSON(w, r, meResp{User: s.User, CSRF: s.CSRF, CanStoreKeys: acc.CanStoreKeys(), MCPURL: mcpURL})
	})

	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		if s := sessionFrom(r.Context()); s != nil {
			acc.Logout(r.Context(), s.token)
		}
		setCookie(w, r, "", -1)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.Handle("GET /api/me", member(func(w http.ResponseWriter, r *http.Request) {
		s := sessionFrom(r.Context())
		writeJSON(w, r, meResp{User: s.User, CSRF: s.CSRF, CanStoreKeys: acc.CanStoreKeys(), MCPURL: mcpURL})
	}))

	mux.Handle("POST /api/password", member(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Current, New string }
		if !readJSON(w, r, &in) {
			return
		}
		s := sessionFrom(r.Context())
		if err := acc.ChangePassword(r.Context(), s.User.ID, in.Current, in.New, s.token); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.Handle("PUT /api/mess-key", member(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Key string }
		if !readJSON(w, r, &in) {
			return
		}
		u := accounts.UserFrom(r.Context())
		if err := acc.SetMessKey(r.Context(), u.ID, in.Key); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		regs.forget(u.ID)
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.Handle("DELETE /api/mess-key", member(func(w http.ResponseWriter, r *http.Request) {
		u := accounts.UserFrom(r.Context())
		if err := acc.ClearMessKey(r.Context(), u.ID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		regs.forget(u.ID)
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.Handle("GET /api/mcp-tokens", member(func(w http.ResponseWriter, r *http.Request) {
		ts, err := acc.Tokens(r.Context(), accounts.UserFrom(r.Context()).ID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, r, ts)
	}))

	mux.Handle("POST /api/mcp-tokens", member(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Label string }
		if !readJSON(w, r, &in) {
			return
		}
		plain, t, err := acc.CreateToken(r.Context(), accounts.UserFrom(r.Context()).ID, in.Label)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		// The plain token is shown this once; only its hash is stored.
		writeJSON(w, r, struct {
			*accounts.Token
			Plain string `json:"token"`
		}{t, plain})
	}))

	mux.Handle("DELETE /api/mcp-tokens/{id}", member(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			jsonError(w, "bad id", http.StatusBadRequest)
			return
		}
		if err := acc.RevokeToken(r.Context(), accounts.UserFrom(r.Context()).ID, id); err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}
