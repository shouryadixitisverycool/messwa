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
	"time"

	"messwa/internal/accounts"
	"messwa/internal/mcpserver"
	"messwa/internal/portal"
)

const cookieName = "messwa_session"

// pageRoles is the role each page needs. Paths not listed (the sign-in and
// sign-up pages, styles, scripts, fonts, icons) are public: they hold no
// data. Signed-in accounts an admin hasn't approved yet only get
// welcomePage.
var pageRoles = map[string]accounts.Role{
	"/":              accounts.Member,
	"/index.html":    accounts.Member,
	"/sales.html":    accounts.Member,
	"/account.html":  accounts.Member,
	"/settings.html": accounts.Member,
	"/insights.html": accounts.Admin,
	"/usage.html":    accounts.Admin,
}

// welcomePage is where a new account sets itself up and asks for approval.
const welcomePage = "/welcome.html"

// checkMessKey makes one portal read with key, to catch a mistyped key
// before it's saved. Tests replace it.
var checkMessKey = func(ctx context.Context, key string) error {
	c, err := portal.New(strings.TrimSpace(key))
	if err != nil {
		return err
	}
	today := time.Now().Format("2006-01-02")
	_, err = c.Registrations(ctx, today, today)
	return err
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
			// Signing in or up replaces the session, and the pages that do it
			// have no token to send.
			if sess != nil && r.URL.Path != "/api/login" && r.URL.Path != "/api/signup" {
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
		if !u.IsActive() {
			jsonError(w, "your account is waiting for an admin to approve it", http.StatusForbidden)
			return
		}
		if !u.Role.Allows(need) {
			jsonError(w, "admins only", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// signedIn serves h to any signed-in user, approved or not. Only the
// endpoints a new account needs to set itself up use it.
func signedIn(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if accounts.UserFrom(r.Context()) == nil {
			jsonError(w, "sign in first", http.StatusUnauthorized)
			return
		}
		h(w, r)
	})
}

// pages serves static files, sending visitors without a session to the
// sign-in page, unapproved accounts to the welcome page, and refusing pages
// their role doesn't allow.
func pages(files http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := accounts.UserFrom(r.Context())
		if r.URL.Path == welcomePage {
			switch {
			case u == nil:
				http.Redirect(w, r, "/login.html", http.StatusSeeOther)
				return
			case u.IsActive():
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
		}
		need, gated := pageRoles[r.URL.Path]
		if gated {
			if u == nil {
				next := strings.TrimPrefix(r.URL.Path, "/")
				to := "/login.html"
				if next != "" && next != "index.html" {
					to += "?next=" + next
				}
				http.Redirect(w, r, to, http.StatusSeeOther)
				return
			}
			if !u.IsActive() {
				http.Redirect(w, r, welcomePage, http.StatusSeeOther)
				return
			}
			if !u.Role.Allows(need) {
				http.Error(w, "This page is for admins only.", http.StatusForbidden)
				return
			}
			// The page is the same for everyone, but who may see it isn't:
			// keep it out of shared caches and check back each load, so a
			// signed-out browser gets the redirect, not a cached copy.
			w.Header().Set("Cache-Control", "private, no-cache")
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
// srv is the signed-in user's own server, whose Device is their WhatsApp.
func authRoutes(mux *http.ServeMux, acc *accounts.Accounts, srv *mcpserver.Server, regs *regCache, mcpURL string) {
	member := func(h http.HandlerFunc) http.Handler { return requireRole(accounts.Member, h) }
	admin := func(h http.HandlerFunc) http.Handler { return requireRole(accounts.Admin, h) }
	startSession := func(w http.ResponseWriter, r *http.Request, tok string, s *accounts.Session) {
		if old := sessionFrom(r.Context()); old != nil {
			acc.Logout(r.Context(), old.token)
		}
		setCookie(w, r, tok, int(accounts.SessionTTL().Seconds()))
		writeJSON(w, r, meResp{User: s.User, CSRF: s.CSRF, CanStoreKeys: acc.CanStoreKeys(), MCPURL: mcpURL})
	}

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
		startSession(w, r, tok, s)
	})

	// Anyone who can reach the dashboard can sign up. The account can't
	// use anything until an admin approves it.
	mux.HandleFunc("POST /api/signup", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Name, Password string }
		if !readJSON(w, r, &in) {
			return
		}
		from, _, _ := net.SplitHostPort(r.RemoteAddr)
		name := strings.TrimSpace(in.Name)
		if _, err := acc.SignUp(r.Context(), name, in.Password, from); err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, accounts.ErrSignUpLimit) {
				code = http.StatusTooManyRequests
			}
			jsonError(w, err.Error(), code)
			return
		}
		tok, s, err := acc.Login(r.Context(), name, in.Password, from)
		if err != nil {
			jsonError(w, "account created, but signing in failed; try the sign-in page", http.StatusInternalServerError)
			return
		}
		startSession(w, r, tok, s)
	})

	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		if s := sessionFrom(r.Context()); s != nil {
			acc.Logout(r.Context(), s.token)
		}
		setCookie(w, r, "", -1)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.Handle("GET /api/me", signedIn(func(w http.ResponseWriter, r *http.Request) {
		s := sessionFrom(r.Context())
		writeJSON(w, r, meResp{User: s.User, CSRF: s.CSRF, CanStoreKeys: acc.CanStoreKeys(), MCPURL: mcpURL})
	}))

	mux.Handle("POST /api/password", signedIn(func(w http.ResponseWriter, r *http.Request) {
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

	mux.Handle("PUT /api/mess-key", signedIn(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Key string }
		if !readJSON(w, r, &in) {
			return
		}
		u := accounts.UserFrom(r.Context())
		if !acc.CanStoreKeys() {
			jsonError(w, accounts.ErrNoSecret.Error(), http.StatusBadRequest)
			return
		}
		if err := checkMessKey(r.Context(), in.Key); err != nil {
			jsonError(w, "the mess portal didn't accept that key: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := acc.SetMessKey(r.Context(), u.ID, in.Key); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		regs.forget(u.ID)
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.Handle("DELETE /api/mess-key", signedIn(func(w http.ResponseWriter, r *http.Request) {
		u := accounts.UserFrom(r.Context())
		if err := acc.ClearMessKey(r.Context(), u.ID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		regs.forget(u.ID)
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.Handle("POST /api/approval", signedIn(func(w http.ResponseWriter, r *http.Request) {
		linked := srv.Device != nil && srv.Device.Linked()
		if err := acc.RequestApproval(r.Context(), accounts.UserFrom(r.Context()).ID, linked); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	// account-events streams an "account" event when the sign-up queue
	// changes (for admins) or the signed-in user's own status does (for
	// everyone else), so the account and welcome pages update at once.
	mux.Handle("GET /api/account-events", signedIn(func(w http.ResponseWriter, r *http.Request) {
		u := accounts.UserFrom(r.Context())
		want := func(id int64) bool { return id == u.ID }
		if u.IsAdmin() {
			want = func(int64) bool { return true }
		}
		c, stop := acc.Watch(want)
		defer stop()
		stream(w, r, c, "account")
	}))

	mux.Handle("GET /api/signups", admin(func(w http.ResponseWriter, r *http.Request) {
		us, err := acc.Waiting(r.Context())
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		type signup struct {
			Name       string    `json:"name"`
			Status     string    `json:"status"`
			HasMessKey bool      `json:"mess_key_set"`
			SignedUp   time.Time `json:"signed_up"`
			Since      time.Time `json:"since"`
		}
		out := make([]signup, len(us))
		for i, u := range us {
			out[i] = signup{u.Name, string(u.Status), u.HasMessKey, u.CreatedAt, u.StatusAt}
		}
		writeJSON(w, r, out)
	}))

	mux.Handle("POST /api/signups/{name}/{decision}", admin(func(w http.ResponseWriter, r *http.Request) {
		u, err := acc.ByName(r.Context(), r.PathValue("name"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		switch r.PathValue("decision") {
		case "approve":
			err = acc.Approve(r.Context(), u.ID)
		case "decline":
			err = acc.Decline(r.Context(), u.ID)
		default:
			jsonError(w, "decide approve or decline", http.StatusNotFound)
			return
		}
		if err != nil {
			jsonError(w, err.Error(), http.StatusConflict)
			return
		}
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
