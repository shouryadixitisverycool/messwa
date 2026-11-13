// Package accounts holds messwa's users: who can sign in to the dashboard,
// what role they have, their MCP tokens, and their mess API keys, which are
// stored encrypted with the key in MESSWA_SECRET_KEY.
package accounts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"messwa/internal/portal"
	"messwa/internal/store"
)

// SecretEnv names the env var holding the key that encrypts mess API keys.
const SecretEnv = "MESSWA_SECRET_KEY"

// LegacyKeyEnv is the single mess API key messwa used before accounts.
const LegacyKeyEnv = "MESS_API_KEY"

type Role string

const (
	// Admin sees everything, including what belongs to the linked
	// WhatsApp account: sales, buyers, DMs and analytics.
	Admin Role = "admin"
	// Member sees the board and their own meals.
	Member Role = "member"
)

// ParseRole accepts "admin", "member" and "user".
func ParseRole(s string) (Role, error) {
	switch strings.ToLower(s) {
	case "admin":
		return Admin, nil
	case "member", "user":
		return Member, nil
	}
	return "", fmt.Errorf("role must be admin or member, not %q", s)
}

// Status is where an account is in sign-up. Accounts an admin makes with
// messwa user add start Active; accounts made on the sign-up page start
// Pending, become Requested when their owner asks for approval, and then
// Active or Declined when an admin decides.
type Status string

const (
	Pending   Status = "pending"
	Requested Status = "requested"
	Active    Status = "active"
	Declined  Status = "declined"
)

// Allows reports whether a user with role r may use something that needs
// role need. Admins may use everything.
func (r Role) Allows(need Role) bool { return r == Admin || r == need }

type User struct {
	ID         int64     `json:"-"`
	Name       string    `json:"name"`
	Role       Role      `json:"role"`
	HasMessKey bool      `json:"mess_key_set"`
	MessKeyAt  time.Time `json:"-"`
	Disabled   bool      `json:"-"`
	Status     Status    `json:"status"`
	// StatusAt is when Status last changed: for a Requested account, when
	// they asked for approval.
	StatusAt  time.Time `json:"-"`
	CreatedAt time.Time `json:"-"`
}

func (u *User) IsAdmin() bool { return u != nil && u.Role == Admin && u.Status == Active }

// IsActive reports whether an admin has let this account in.
func (u *User) IsActive() bool { return u != nil && u.Status == Active && !u.Disabled }

type ctxKey struct{}

func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// UserFrom returns the signed-in user, or nil.
func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(ctxKey{}).(*User)
	return u
}

// Session is a signed-in browser.
type Session struct {
	User *User
	CSRF string
}

const (
	sessionTTL    = 30 * 24 * time.Hour
	minPassword   = 10
	maxPassword   = 256
	tokenPrefix   = "mwa_"
	failLimit     = 5
	failLockout   = time.Minute
	maxMessKey    = 4096
	sessionPrefix = "mws_"
)

var (
	ErrBadLogin    = errors.New("wrong name or password")
	ErrLocked      = errors.New("too many failed sign-ins; wait a minute")
	ErrNoSecret    = errors.New("set " + SecretEnv + " (openssl rand -hex 32) to store mess API keys")
	ErrBadPassword = fmt.Errorf("passwords must be %d to %d characters", minPassword, maxPassword)
	ErrSignUpLimit = errors.New("too many sign-ups from here; try again in an hour")
	validName      = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,31}$`)
)

type Accounts struct {
	st  *store.Store
	box *Box // nil without MESSWA_SECRET_KEY
	now func() time.Time

	mu      sync.Mutex
	fails   map[string]failCount
	signups map[string][]time.Time

	dummyOnce sync.Once
	dummy     string

	onDecision func(ctx context.Context, id int64)
}

// OnDecision sets a function that runs after an admin approves or declines
// an account in this process. run uses it to connect or unlink the user's
// WhatsApp.
func (a *Accounts) OnDecision(fn func(ctx context.Context, id int64)) { a.onDecision = fn }

type failCount struct {
	n     int
	until time.Time
}

// New makes the account service. An empty secret means mess API keys can't
// be stored or read; everything else works.
func New(st *store.Store, secret string) (*Accounts, error) {
	a := &Accounts{st: st, now: time.Now, fails: map[string]failCount{}, signups: map[string][]time.Time{}}
	if secret != "" {
		b, err := NewBox(secret)
		if err != nil {
			return nil, err
		}
		a.box = b
	}
	return a, nil
}

// FromEnv is New with the secret from MESSWA_SECRET_KEY.
func FromEnv(st *store.Store) (*Accounts, error) { return New(st, os.Getenv(SecretEnv)) }

func toUser(r *store.UserRecord) *User {
	if r == nil {
		return nil
	}
	return &User{
		ID: r.ID, Name: r.Name, Role: Role(r.Role), HasMessKey: len(r.MessKey) > 0, MessKeyAt: r.MessKeyAt, Disabled: r.Disabled,
		Status: Status(r.Status), StatusAt: r.StatusAt, CreatedAt: r.CreatedAt,
	}
}

func checkNewPassword(pw string) error {
	if n := len([]rune(pw)); n < minPassword || len(pw) > maxPassword {
		return ErrBadPassword
	}
	return nil
}

// Add creates an active user, for admins on the CLI.
func (a *Accounts) Add(ctx context.Context, name, password string, role Role) (*User, error) {
	return a.add(ctx, name, password, role, Active)
}

const (
	signUpLimit  = 5
	signUpWindow = time.Hour
)

// SignUp creates a pending member from the sign-up page. They can sign in
// and set up their account, but use nothing else until an admin approves
// them. from identifies the client (its IP), which gets five sign-ups an
// hour.
func (a *Accounts) SignUp(ctx context.Context, name, password, from string) (*User, error) {
	now := a.now()
	a.mu.Lock()
	recent := a.signups[from][:0:0]
	for _, t := range a.signups[from] {
		if now.Sub(t) < signUpWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= signUpLimit {
		a.mu.Unlock()
		return nil, ErrSignUpLimit
	}
	if len(a.signups) > 10000 {
		a.signups = map[string][]time.Time{}
	}
	a.signups[from] = append(recent, now)
	a.mu.Unlock()
	return a.add(ctx, name, password, Member, Pending)
}

func (a *Accounts) add(ctx context.Context, name, password string, role Role, status Status) (*User, error) {
	if !validName.MatchString(name) {
		return nil, errors.New("names are 1 to 32 letters, digits, dots, dashes or underscores")
	}
	if role != Admin && role != Member {
		return nil, fmt.Errorf("unknown role %q", role)
	}
	if err := checkNewPassword(password); err != nil {
		return nil, err
	}
	h, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	id, err := a.st.AddUser(ctx, name, string(role), h, string(status), a.now())
	if err != nil {
		return nil, err
	}
	return a.User(ctx, id)
}

func (a *Accounts) User(ctx context.Context, id int64) (*User, error) {
	r, err := a.st.User(ctx, id)
	return toUser(r), err
}

// ByName finds a user by name, ignoring case. It errors when there's none.
func (a *Accounts) ByName(ctx context.Context, name string) (*User, error) {
	r, err := a.st.UserByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("no user %q", name)
	}
	return toUser(r), nil
}

// FirstAdmin is the oldest enabled admin, or nil.
func (a *Accounts) FirstAdmin(ctx context.Context) (*User, error) {
	r, err := a.st.FirstAdmin(ctx)
	return toUser(r), err
}

func (a *Accounts) List(ctx context.Context) ([]User, error) {
	rs, err := a.st.Users(ctx)
	out := make([]User, len(rs))
	for i := range rs {
		out[i] = *toUser(&rs[i])
	}
	return out, err
}

func (a *Accounts) SetRole(ctx context.Context, id int64, role Role) error {
	return a.st.SetRole(ctx, id, string(role))
}

// RequestApproval asks the admins to let a pending account in. The account
// needs a mess API key and a linked WhatsApp first; the caller says whether
// WhatsApp is linked.
func (a *Accounts) RequestApproval(ctx context.Context, id int64, whatsappLinked bool) error {
	u, err := a.User(ctx, id)
	if err != nil {
		return err
	}
	if u == nil {
		return errors.New("no such user")
	}
	switch u.Status {
	case Requested:
		return nil
	case Active:
		return errors.New("your account is already approved")
	case Declined:
		return errors.New("an admin declined this account")
	}
	if !u.HasMessKey {
		return errors.New("save your mess API key first")
	}
	if !whatsappLinked {
		return errors.New("link your WhatsApp first")
	}
	ok, err := a.st.SetStatus(ctx, id, string(Requested), []string{string(Pending)}, a.now())
	if err == nil && !ok {
		err = errors.New("your account changed meanwhile; reload the page")
	}
	return err
}

// Approve lets a pending or requested account in.
func (a *Accounts) Approve(ctx context.Context, id int64) error {
	return a.decide(ctx, id, Active)
}

// Decline turns down a pending or requested account. Its owner can still
// sign in to see that, but can't use anything or ask again.
func (a *Accounts) Decline(ctx context.Context, id int64) error {
	return a.decide(ctx, id, Declined)
}

func (a *Accounts) decide(ctx context.Context, id int64, to Status) error {
	ok, err := a.st.SetStatus(ctx, id, string(to), []string{string(Pending), string(Requested)}, a.now())
	if err == nil && !ok {
		err = errors.New("that account isn't waiting for approval")
	}
	if err == nil && a.onDecision != nil {
		a.onDecision(ctx, id)
	}
	return err
}

// Waiting lists accounts that signed up and aren't approved or declined,
// those that asked for approval first.
func (a *Accounts) Waiting(ctx context.Context) ([]User, error) {
	all, err := a.List(ctx)
	if err != nil {
		return nil, err
	}
	out := []User{}
	for _, u := range all {
		if u.Status == Requested && !u.Disabled {
			out = append(out, u)
		}
	}
	for _, u := range all {
		if u.Status == Pending && !u.Disabled {
			out = append(out, u)
		}
	}
	return out, nil
}

// SetDisabled blocks or unblocks a user. Blocking ends their sessions; their
// MCP tokens stop working while they're blocked.
func (a *Accounts) SetDisabled(ctx context.Context, id int64, disabled bool) error {
	if err := a.st.SetDisabled(ctx, id, disabled); err != nil {
		return err
	}
	if disabled {
		return a.st.DeleteSessions(ctx, id, nil)
	}
	return nil
}

func (a *Accounts) Remove(ctx context.Context, id int64) error { return a.st.RemoveUser(ctx, id) }

// ---- passwords and sessions ----

func (a *Accounts) dummyHash() string {
	a.dummyOnce.Do(func() { a.dummy, _ = hashPassword("messwa-no-such-user") })
	return a.dummy
}

// Login checks a password and starts a session. from identifies the client
// (its IP) for the failed-login lockout.
func (a *Accounts) Login(ctx context.Context, name, password, from string) (string, *Session, error) {
	key := strings.ToLower(name) + "|" + from
	now := a.now()
	a.mu.Lock()
	f := a.fails[key]
	a.mu.Unlock()
	if now.Before(f.until) {
		return "", nil, ErrLocked
	}
	r, err := a.st.UserByName(ctx, name)
	if err != nil {
		return "", nil, err
	}
	hash := a.dummyHash()
	if r != nil {
		hash = r.PWHash
	}
	// Hash even for unknown names, so timing doesn't reveal who exists.
	ok := checkPassword(hash, password) && r != nil && !r.Disabled
	if !ok {
		a.mu.Lock()
		f := a.fails[key]
		f.n++
		if f.n >= failLimit {
			f = failCount{until: now.Add(failLockout)}
		}
		if len(a.fails) > 10000 {
			a.fails = map[string]failCount{} // bound memory against made-up names
		}
		a.fails[key] = f
		a.mu.Unlock()
		return "", nil, ErrBadLogin
	}
	a.mu.Lock()
	delete(a.fails, key)
	a.mu.Unlock()
	tok, th, err := newToken(sessionPrefix)
	if err != nil {
		return "", nil, err
	}
	csrf, _, err := newToken("")
	if err != nil {
		return "", nil, err
	}
	if err := a.st.AddSession(ctx, th, r.ID, csrf, now, now.Add(sessionTTL)); err != nil {
		return "", nil, err
	}
	return tok, &Session{User: toUser(r), CSRF: csrf}, nil
}

// SessionTTL is how long a sign-in lasts.
func SessionTTL() time.Duration { return sessionTTL }

// Session returns the session for a cookie value, or nil when it's unknown,
// expired, or its user is blocked.
func (a *Accounts) Session(ctx context.Context, token string) (*Session, error) {
	if !strings.HasPrefix(token, sessionPrefix) {
		return nil, nil
	}
	s, err := a.st.Session(ctx, hashToken(token), a.now())
	if err != nil || s == nil {
		return nil, err
	}
	u, err := a.User(ctx, s.UserID)
	if err != nil || u == nil || u.Disabled {
		return nil, err
	}
	return &Session{User: u, CSRF: s.CSRF}, nil
}

func (a *Accounts) Logout(ctx context.Context, token string) error {
	return a.st.DeleteSession(ctx, hashToken(token))
}

// ChangePassword checks the old password, sets the new one, and ends every
// other session of that user. keep is the current session's cookie value.
func (a *Accounts) ChangePassword(ctx context.Context, id int64, old, new, keep string) error {
	r, err := a.st.User(ctx, id)
	if err != nil {
		return err
	}
	if r == nil || !checkPassword(r.PWHash, old) {
		return errors.New("current password is wrong")
	}
	return a.setPassword(ctx, id, new, hashToken(keep))
}

// SetPassword sets a password without the old one, for the CLI. It ends
// every session of that user.
func (a *Accounts) SetPassword(ctx context.Context, id int64, pw string) error {
	return a.setPassword(ctx, id, pw, nil)
}

func (a *Accounts) setPassword(ctx context.Context, id int64, pw string, keep []byte) error {
	if err := checkNewPassword(pw); err != nil {
		return err
	}
	h, err := hashPassword(pw)
	if err != nil {
		return err
	}
	if err := a.st.SetPassword(ctx, id, h); err != nil {
		return err
	}
	return a.st.DeleteSessions(ctx, id, keep)
}

// ---- mess API keys ----

// CanStoreKeys reports whether MESSWA_SECRET_KEY is set.
func (a *Accounts) CanStoreKeys() bool { return a.box != nil }

func (a *Accounts) SetMessKey(ctx context.Context, id int64, key string) error {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > maxMessKey || strings.ContainsAny(key, "\r\n") {
		return errors.New("that doesn't look like a mess API key")
	}
	if a.box == nil {
		return ErrNoSecret
	}
	sealed, err := a.box.Seal(id, []byte(key))
	if err != nil {
		return err
	}
	return a.st.SetMessKey(ctx, id, sealed, a.now())
}

func (a *Accounts) ClearMessKey(ctx context.Context, id int64) error {
	return a.st.SetMessKey(ctx, id, nil, a.now())
}

// Owner is who the linked WhatsApp account, tokenless loopback MCP and the
// CLI act as: the first admin. Until an admin exists it's a stand-in with ID
// 0 whose portal reads use MESS_API_KEY, so an existing setup keeps working
// until messwa user add -admin moves the key.
func (a *Accounts) Owner(ctx context.Context) (*User, error) {
	u, err := a.FirstAdmin(ctx)
	if err != nil || u != nil {
		return u, err
	}
	return &User{Name: "owner", Role: Admin, Status: Active, HasMessKey: os.Getenv(LegacyKeyEnv) != ""}, nil
}

// Portal returns a portal client that reads with user id's own key. ID 0 is
// Owner's stand-in. Until MESS_API_KEY has been moved onto an account, the
// owner (the stand-in, or a first admin with no stored key) reads with it.
func (a *Accounts) Portal(ctx context.Context, id int64) (*portal.Client, error) {
	envKey := strings.TrimSpace(os.Getenv(LegacyKeyEnv))
	admin, err := a.FirstAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if id == 0 {
		if admin != nil {
			return a.Portal(ctx, admin.ID)
		}
		if envKey == "" {
			return nil, portal.ErrNoKey
		}
		return portal.New(envKey)
	}
	r, err := a.st.User(ctx, id)
	if err != nil {
		return nil, err
	}
	if r != nil && len(r.MessKey) == 0 && envKey != "" && admin != nil && admin.ID == id {
		return portal.New(envKey)
	}
	if r == nil || len(r.MessKey) == 0 {
		return nil, portal.ErrNoKey
	}
	if a.box == nil {
		return nil, errors.New("set " + SecretEnv + " to read the stored mess API key")
	}
	key, err := a.box.Open(id, r.MessKey)
	if err != nil {
		return nil, err
	}
	return portal.New(string(key))
}

// MigrateEnvKey moves MESS_API_KEY onto the first admin when they have no
// key yet. It returns the admin's name when it moved the key, and a warning
// to show when the env var is set but no longer used.
func (a *Accounts) MigrateEnvKey(ctx context.Context) (moved, warning string, err error) {
	key := strings.TrimSpace(os.Getenv(LegacyKeyEnv))
	if key == "" {
		return "", "", nil
	}
	admin, err := a.FirstAdmin(ctx)
	if err != nil {
		return "", "", err
	}
	if admin == nil {
		return "", LegacyKeyEnv + " is set but there is no admin yet; run messwa user add -admin NAME to move it onto your account", nil
	}
	if admin.HasMessKey {
		return "", LegacyKeyEnv + " is set but ignored: every account uses its own stored key. Remove it from .env", nil
	}
	if a.box == nil {
		// Keep reading with the env key until it can be stored.
		return "", fmt.Sprintf("%s can't move onto %s until %s is set; it's still read from the environment", LegacyKeyEnv, admin.Name, SecretEnv), nil
	}
	if err := a.SetMessKey(ctx, admin.ID, key); err != nil {
		return "", "", fmt.Errorf("move %s to %s: %w", LegacyKeyEnv, admin.Name, err)
	}
	return admin.Name, "", nil
}

// ---- MCP tokens ----

type Token struct {
	ID        int64     `json:"id"`
	Label     string    `json:"label"`
	Prefix    string    `json:"prefix"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used"`
	Revoked   bool      `json:"revoked"`
	UserID    int64     `json:"-"`
}

// CreateToken makes an MCP token for a user. The plain token is returned
// once and only its hash is kept.
func (a *Accounts) CreateToken(ctx context.Context, userID int64, label string) (string, *Token, error) {
	label = strings.TrimSpace(label)
	if len(label) > 64 {
		return "", nil, errors.New("labels are at most 64 characters")
	}
	tok, h, err := newToken(tokenPrefix)
	if err != nil {
		return "", nil, err
	}
	prefix := tok[:len(tokenPrefix)+6]
	now := a.now()
	id, err := a.st.AddToken(ctx, userID, label, h, prefix, now)
	if err != nil {
		return "", nil, err
	}
	return tok, &Token{ID: id, Label: label, Prefix: prefix, CreatedAt: now, UserID: userID}, nil
}

// Tokens lists a user's MCP tokens. userID 0 lists everyone's.
func (a *Accounts) Tokens(ctx context.Context, userID int64) ([]Token, error) {
	rs, err := a.st.Tokens(ctx, userID)
	out := make([]Token, len(rs))
	for i, r := range rs {
		out[i] = Token{ID: r.ID, Label: r.Label, Prefix: r.Prefix, CreatedAt: r.CreatedAt, LastUsed: r.LastUsed, Revoked: r.Revoked, UserID: r.UserID}
	}
	return out, err
}

// RevokeToken revokes a token. A userID other than 0 must own it.
func (a *Accounts) RevokeToken(ctx context.Context, userID, id int64) error {
	return a.st.RevokeToken(ctx, userID, id, a.now())
}

// VerifyToken returns the enabled user an MCP token belongs to, or nil.
func (a *Accounts) VerifyToken(ctx context.Context, token string) (*User, error) {
	if !strings.HasPrefix(token, tokenPrefix) {
		return nil, nil
	}
	id, err := a.st.TokenUser(ctx, hashToken(token), a.now())
	if err != nil || id == 0 {
		return nil, err
	}
	u, err := a.User(ctx, id)
	if err != nil || !u.IsActive() {
		return nil, err
	}
	return u, nil
}
