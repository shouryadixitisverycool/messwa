package accounts

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"messwa/internal/portal"
	"messwa/internal/store"
)

const secret = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

func open(t *testing.T, secret string) *Accounts {
	t.Helper()
	CheapHashing()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a, err := New(st, secret)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestPasswordHash(t *testing.T) {
	CheapHashing()
	h, err := hashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !checkPassword(h, "correct horse") || checkPassword(h, "correct horsf") || checkPassword("garbage", "correct horse") {
		t.Error("password check is wrong")
	}
	h2, _ := hashPassword("correct horse")
	if h == h2 {
		t.Error("salt not random")
	}
}

func TestBox(t *testing.T) {
	b, err := NewBox(secret)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b.Seal(7, []byte("mess-key"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("mess-key")) {
		t.Fatal("key stored in the clear")
	}
	if got, err := b.Open(7, sealed); err != nil || string(got) != "mess-key" {
		t.Fatalf("open: %q %v", got, err)
	}
	if _, err := b.Open(8, sealed); err == nil {
		t.Error("a key copied to another user decrypted")
	}
	other, _ := NewBox(strings.Repeat("ab", 32))
	if _, err := other.Open(7, sealed); err == nil {
		t.Error("wrong secret decrypted")
	}
	for _, bad := range []string{"", "abc", strings.Repeat("zz", 32)} {
		if _, err := NewBox(bad); err == nil {
			t.Errorf("secret %q accepted", bad)
		}
	}
}

func TestLoginAndSessions(t *testing.T) {
	ctx := context.Background()
	a := open(t, secret)
	if _, err := a.Add(ctx, "asha", "short", Member); err == nil {
		t.Error("short password accepted")
	}
	if _, err := a.Add(ctx, "bad name!", "long enough pw", Member); err == nil {
		t.Error("bad name accepted")
	}
	u, err := a.Add(ctx, "Asha", "long enough pw", Member)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Add(ctx, "asha", "long enough pw", Admin); !errors.Is(err, store.ErrNameTaken) {
		t.Errorf("duplicate name: %v", err)
	}

	tok, s, err := a.Login(ctx, "asha", "long enough pw", "1.2.3.4")
	if err != nil || s.User.ID != u.ID || s.CSRF == "" {
		t.Fatalf("login: %v %+v", err, s)
	}
	if got, _ := a.Session(ctx, tok); got == nil || got.User.Name != "Asha" {
		t.Fatalf("session: %+v", got)
	}
	if got, _ := a.Session(ctx, tok+"x"); got != nil {
		t.Error("forged session accepted")
	}

	// A second device, then a password change from the first ends it.
	tok2, _, _ := a.Login(ctx, "asha", "long enough pw", "1.2.3.4")
	if err := a.ChangePassword(ctx, u.ID, "wrong", "another long pw", tok); err == nil {
		t.Error("wrong current password accepted")
	}
	if err := a.ChangePassword(ctx, u.ID, "long enough pw", "another long pw", tok); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.Session(ctx, tok); got == nil {
		t.Error("password change ended the current session")
	}
	if got, _ := a.Session(ctx, tok2); got != nil {
		t.Error("password change kept the other session")
	}

	// Blocking ends sessions and refuses sign-in.
	a.SetDisabled(ctx, u.ID, true)
	if got, _ := a.Session(ctx, tok); got != nil {
		t.Error("blocked user kept a session")
	}
	if _, _, err := a.Login(ctx, "asha", "another long pw", "1.2.3.4"); !errors.Is(err, ErrBadLogin) {
		t.Errorf("blocked login: %v", err)
	}

	// Expiry.
	a.SetDisabled(ctx, u.ID, false)
	tok3, _, _ := a.Login(ctx, "asha", "another long pw", "1.2.3.4")
	a.now = func() time.Time { return time.Now().Add(SessionTTL() + time.Hour) }
	if got, _ := a.Session(ctx, tok3); got != nil {
		t.Error("expired session accepted")
	}
}

func TestLockout(t *testing.T) {
	ctx := context.Background()
	a := open(t, "")
	a.Add(ctx, "asha", "long enough pw", Member)
	for i := 0; i < failLimit; i++ {
		if _, _, err := a.Login(ctx, "asha", "nope", "9.9.9.9"); !errors.Is(err, ErrBadLogin) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, _, err := a.Login(ctx, "asha", "long enough pw", "9.9.9.9"); !errors.Is(err, ErrLocked) {
		t.Errorf("not locked out: %v", err)
	}
	if _, _, err := a.Login(ctx, "asha", "long enough pw", "8.8.8.8"); err != nil {
		t.Errorf("another client was locked out too: %v", err)
	}
	a.now = func() time.Time { return time.Now().Add(failLockout + time.Second) }
	if _, _, err := a.Login(ctx, "asha", "long enough pw", "9.9.9.9"); err != nil {
		t.Errorf("lockout never lifted: %v", err)
	}
}

func TestMessKeys(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MESS_API_URL", "")
	t.Setenv(LegacyKeyEnv, "")

	none := open(t, "")
	u, _ := none.Add(ctx, "asha", "long enough pw", Member)
	if err := none.SetMessKey(ctx, u.ID, "k"); !errors.Is(err, ErrNoSecret) {
		t.Errorf("stored a key without a secret: %v", err)
	}

	a := open(t, secret)
	u, _ = a.Add(ctx, "asha", "long enough pw", Member)
	if _, err := a.Portal(ctx, u.ID); !errors.Is(err, portal.ErrNoKey) {
		t.Errorf("portal without a key: %v", err)
	}
	if err := a.SetMessKey(ctx, u.ID, "secret\nkey"); err == nil {
		t.Error("multi-line key accepted")
	}
	if err := a.SetMessKey(ctx, u.ID, "  secret-key "); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.User(ctx, u.ID); !got.HasMessKey {
		t.Error("key not recorded")
	}
	if _, err := a.Portal(ctx, u.ID); err != nil {
		t.Errorf("portal with a key: %v", err)
	}
	a.ClearMessKey(ctx, u.ID)
	if got, _ := a.User(ctx, u.ID); got.HasMessKey {
		t.Error("key not cleared")
	}
}

func TestMigrateEnvKey(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MESS_API_URL", "")
	a := open(t, secret)

	t.Setenv(LegacyKeyEnv, "")
	if moved, warn, err := a.MigrateEnvKey(ctx); moved != "" || warn != "" || err != nil {
		t.Errorf("nothing to move: %q %q %v", moved, warn, err)
	}

	t.Setenv(LegacyKeyEnv, "env-key")
	// Before any admin exists, the owner stand-in reads with the env key.
	owner, _ := a.Owner(ctx)
	if owner.ID != 0 || !owner.IsAdmin() || !owner.HasMessKey {
		t.Errorf("owner stand-in: %+v", owner)
	}
	if _, err := a.Portal(ctx, 0); err != nil {
		t.Errorf("stand-in portal: %v", err)
	}
	if _, warn, _ := a.MigrateEnvKey(ctx); !strings.Contains(warn, "no admin") {
		t.Errorf("warning without an admin: %q", warn)
	}

	member, _ := a.Add(ctx, "member", "long enough pw", Member)
	admin, _ := a.Add(ctx, "boss", "long enough pw", Admin)
	// Before the move, the first admin still reads with the env key, and
	// nobody else does.
	if _, err := a.Portal(ctx, admin.ID); err != nil {
		t.Errorf("admin before the move: %v", err)
	}
	if _, err := a.Portal(ctx, member.ID); !errors.Is(err, portal.ErrNoKey) {
		t.Errorf("member read with the env key: %v", err)
	}
	moved, _, err := a.MigrateEnvKey(ctx)
	if err != nil || moved != "boss" {
		t.Fatalf("moved %q: %v", moved, err)
	}
	if got, _ := a.User(ctx, admin.ID); !got.HasMessKey {
		t.Error("admin has no key after the move")
	}
	if owner, _ := a.Owner(ctx); owner.ID != admin.ID {
		t.Errorf("owner is %+v, want the admin", owner)
	}
	if _, warn, _ := a.MigrateEnvKey(ctx); !strings.Contains(warn, "ignored") {
		t.Errorf("second run should only warn: %q", warn)
	}
}

func TestMigrateWithoutSecret(t *testing.T) {
	// Adding the admin before setting MESSWA_SECRET_KEY must not break
	// anything: the key stays in the environment and still works.
	ctx := context.Background()
	t.Setenv("MESS_API_URL", "")
	t.Setenv(LegacyKeyEnv, "env-key")
	a := open(t, "")
	admin, _ := a.Add(ctx, "boss", "long enough pw", Admin)
	moved, warn, err := a.MigrateEnvKey(ctx)
	if err != nil || moved != "" || !strings.Contains(warn, SecretEnv) {
		t.Errorf("migrate without a secret: %q %q %v", moved, warn, err)
	}
	if _, err := a.Portal(ctx, admin.ID); err != nil {
		t.Errorf("admin lost the env key: %v", err)
	}
}

func TestTokens(t *testing.T) {
	ctx := context.Background()
	a := open(t, "")
	asha, _ := a.Add(ctx, "asha", "long enough pw", Member)
	ravi, _ := a.Add(ctx, "ravi", "long enough pw", Member)
	plain, tok, err := a.CreateToken(ctx, asha.ID, "laptop")
	if err != nil || !strings.HasPrefix(plain, tokenPrefix) || !strings.HasPrefix(plain, tok.Prefix) {
		t.Fatalf("create: %q %+v %v", plain, tok, err)
	}
	if u, _ := a.VerifyToken(ctx, plain); u == nil || u.ID != asha.ID {
		t.Fatalf("verify: %+v", u)
	}
	if u, _ := a.VerifyToken(ctx, plain[:len(plain)-1]); u != nil {
		t.Error("truncated token accepted")
	}
	if err := a.RevokeToken(ctx, ravi.ID, tok.ID); err == nil {
		t.Error("revoked someone else's token")
	}
	ts, _ := a.Tokens(ctx, asha.ID)
	if len(ts) != 1 || ts[0].LastUsed.IsZero() {
		t.Errorf("tokens: %+v", ts)
	}
	if err := a.RevokeToken(ctx, asha.ID, tok.ID); err != nil {
		t.Fatal(err)
	}
	if u, _ := a.VerifyToken(ctx, plain); u != nil {
		t.Error("revoked token accepted")
	}

	plain2, _, _ := a.CreateToken(ctx, ravi.ID, "")
	a.SetDisabled(ctx, ravi.ID, true)
	if u, _ := a.VerifyToken(ctx, plain2); u != nil {
		t.Error("blocked user's token accepted")
	}
	a.Remove(ctx, ravi.ID)
	if ts, _ := a.Tokens(ctx, ravi.ID); len(ts) != 0 {
		t.Error("removing a user kept their tokens")
	}
}

func TestSignUp(t *testing.T) {
	a := open(t, secret)
	ctx := context.Background()
	u, err := a.SignUp(ctx, "asha", "asha's password", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if u.Status != Pending || u.Role != Member || u.IsActive() {
		t.Fatalf("new sign-up: %+v", u)
	}

	// A pending account signs in, but its MCP tokens don't work.
	if _, _, err := a.Login(ctx, "asha", "asha's password", "x"); err != nil {
		t.Fatalf("pending sign-in: %v", err)
	}
	tok, _, _ := a.CreateToken(ctx, u.ID, "")
	if v, _ := a.VerifyToken(ctx, tok); v != nil {
		t.Error("pending account's token works")
	}

	if err := a.RequestApproval(ctx, u.ID, true); err == nil {
		t.Error("asked without a mess key")
	}
	a.SetMessKey(ctx, u.ID, "key")
	if err := a.RequestApproval(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if w, _ := a.Waiting(ctx); len(w) != 1 || w[0].Status != Requested {
		t.Fatalf("waiting: %+v", w)
	}
	if err := a.Approve(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.Decline(ctx, u.ID); err == nil {
		t.Error("declined an approved account")
	}
	if v, _ := a.VerifyToken(ctx, tok); v == nil {
		t.Error("approved account's token doesn't work")
	}

	// A pending admin made by role change still isn't an owner.
	p, _ := a.SignUp(ctx, "ravi", "ravi's password", "10.0.0.1")
	a.SetRole(ctx, p.ID, Admin)
	if p, _ = a.User(ctx, p.ID); p.IsAdmin() {
		t.Error("pending admin counts as admin")
	}

	for i := 0; i < 3; i++ {
		a.SignUp(ctx, "x"+string(rune('a'+i)), "some password", "10.0.0.1")
	}
	if _, err := a.SignUp(ctx, "toomany", "some password", "10.0.0.1"); !errors.Is(err, ErrSignUpLimit) {
		t.Errorf("sixth sign-up from one address: %v", err)
	}
	if _, err := a.SignUp(ctx, "elsewhere", "some password", "10.0.0.2"); err != nil {
		t.Errorf("other address: %v", err)
	}
}
