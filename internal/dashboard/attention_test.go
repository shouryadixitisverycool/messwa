package dashboard

import (
	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
	"messwa/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestAttentionPermissions(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := mcpserver.New(config.Default(), st, nil)
	mux := http.NewServeMux()
	attentionRoutes(mux, srv)
	for _, path := range []string{"/api/attention", "/api/attention-sale?id=1"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("anonymous %s: %d", path, w.Code)
		}
		r = r.WithContext(accounts.WithUser(r.Context(), &accounts.User{ID: 2, Role: accounts.Member, Status: accounts.Active}))
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("shared server member %s: %d", path, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/api/attention", nil)
	r = r.WithContext(accounts.WithUser(r.Context(), &accounts.User{ID: 1, Role: accounts.Admin, Status: accounts.Active}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("admin: %d %s", w.Code, w.Body.String())
	}
}
