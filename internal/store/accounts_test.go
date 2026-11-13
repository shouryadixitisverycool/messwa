package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAccountsStore(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	id, err := st.AddUser(ctx, "Asha", "member", "h", "active", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, "asha", "admin", "h", "active", now); !errors.Is(err, ErrNameTaken) {
		t.Errorf("names must be unique ignoring case: %v", err)
	}
	if u, _ := st.UserByName(ctx, "ASHA"); u == nil || u.ID != id {
		t.Errorf("lookup ignoring case: %+v", u)
	}
	if a, _ := st.FirstAdmin(ctx); a != nil {
		t.Errorf("no admin yet, got %+v", a)
	}

	st.AddSession(ctx, []byte("live"), id, "c", now, now.Add(time.Hour))
	st.AddSession(ctx, []byte("old"), id, "c", now.Add(-2*time.Hour), now.Add(-time.Hour))
	if s, _ := st.Session(ctx, []byte("old"), now); s != nil {
		t.Error("expired session returned")
	}
	if err := st.Prune(ctx, now, now.Add(-14*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	st.db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&n)
	if n != 1 {
		t.Errorf("prune left %d sessions, want 1", n)
	}

	st.AddToken(ctx, id, "", []byte("tok"), "mwa_x", now)
	if err := st.RemoveUser(ctx, id); err != nil {
		t.Fatal(err)
	}
	st.db.QueryRow(`SELECT (SELECT count(*) FROM sessions) + (SELECT count(*) FROM mcp_tokens)`).Scan(&n)
	if n != 0 {
		t.Errorf("removing a user left %d sessions and tokens", n)
	}
}
