package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const accountSchema = `
CREATE TABLE IF NOT EXISTS account_ids (id INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS users (
	id          INTEGER PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
	role        TEXT NOT NULL,
	pw_hash     TEXT NOT NULL,
	mess_key    BLOB,
	mess_key_at INTEGER,
	created_at  INTEGER NOT NULL,
	disabled    INTEGER NOT NULL DEFAULT 0
);
CREATE TRIGGER IF NOT EXISTS remember_account_id AFTER INSERT ON users BEGIN INSERT OR IGNORE INTO account_ids(id) VALUES(new.id); END;
CREATE TABLE IF NOT EXISTS sessions (
	token_hash BLOB PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	csrf       TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);
CREATE TABLE IF NOT EXISTS mcp_tokens (
	id         INTEGER PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	label      TEXT NOT NULL DEFAULT '',
	token_hash BLOB NOT NULL UNIQUE,
	prefix     TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	last_used  INTEGER,
	revoked_at INTEGER
);
`

// ErrNameTaken is returned when a user name is already in use.
var ErrNameTaken = errors.New("that name is taken")

// UserRecord is a row of users. MessKey is the sealed mess API key, never
// the plain one.
type UserRecord struct {
	ID        int64
	Name      string
	Role      string
	PWHash    string
	MessKey   []byte
	MessKeyAt time.Time
	CreatedAt time.Time
	Disabled  bool
	// Status is where the account is in sign-up: pending, requested,
	// active or declined. See accounts.Status.
	Status   string
	StatusAt time.Time
}

const userCols = `id,name,role,pw_hash,mess_key,mess_key_at,created_at,disabled,status,status_at`

func scanUser(row interface{ Scan(...any) error }) (*UserRecord, error) {
	var u UserRecord
	var keyAt, statusAt sql.NullInt64
	var created int64
	err := row.Scan(&u.ID, &u.Name, &u.Role, &u.PWHash, &u.MessKey, &keyAt, &created, &u.Disabled, &u.Status, &statusAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.Unix(created, 0)
	if keyAt.Valid {
		u.MessKeyAt = time.Unix(keyAt.Int64, 0)
	}
	if statusAt.Valid {
		u.StatusAt = time.Unix(statusAt.Int64, 0)
	}
	return &u, nil
}

// AddUser creates a user with a sign-up status and returns its ID.
func (s *Store) AddUser(ctx context.Context, name, role, pwHash, status string, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO users(id,name,role,pw_hash,created_at,status,status_at) VALUES((SELECT coalesce(max(id),0)+1 FROM (SELECT id FROM users UNION ALL SELECT id FROM account_ids)),?,?,?,?,?,?)`, name, role, pwHash, now.Unix(), status, now.Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrNameTaken
		}
		return 0, err
	}
	return res.LastInsertId()
}

// User returns the user with that ID, or nil.
func (s *Store) User(ctx context.Context, id int64) (*UserRecord, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, id))
}

// UserByName returns the user with that name, ignoring case, or nil.
func (s *Store) UserByName(ctx context.Context, name string) (*UserRecord, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE name=?`, name))
}

// FirstAdmin is the oldest enabled, active admin, or nil when there is none.
func (s *Store) FirstAdmin(ctx context.Context) (*UserRecord, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE role='admin' AND disabled=0 AND status='active' ORDER BY id LIMIT 1`))
}

// Users lists every user, oldest first.
func (s *Store) Users(ctx context.Context) ([]UserRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserRecord
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (s *Store) updateUser(ctx context.Context, q string, args ...any) error {
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("no such user")
	}
	return nil
}

func (s *Store) SetPassword(ctx context.Context, id int64, pwHash string) error {
	return s.updateUser(ctx, `UPDATE users SET pw_hash=? WHERE id=?`, pwHash, id)
}

func (s *Store) SetRole(ctx context.Context, id int64, role string) error {
	return s.updateUser(ctx, `UPDATE users SET role=? WHERE id=?`, role, id)
}

// SetStatus moves a user to status, but only from one of from. It reports
// whether the user was in one of those states.
func (s *Store) SetStatus(ctx context.Context, id int64, status string, from []string, now time.Time) (bool, error) {
	q := `UPDATE users SET status=?, status_at=? WHERE id=? AND status IN (` + strings.Repeat("?,", len(from)-1) + `?)`
	args := []any{status, now.Unix(), id}
	for _, f := range from {
		args = append(args, f)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) SetDisabled(ctx context.Context, id int64, disabled bool) error {
	return s.updateUser(ctx, `UPDATE users SET disabled=? WHERE id=?`, disabled, id)
}

// SetMessKey stores a sealed mess API key. Nil clears it.
func (s *Store) SetMessKey(ctx context.Context, id int64, sealed []byte, now time.Time) error {
	if sealed == nil {
		return s.updateUser(ctx, `UPDATE users SET mess_key=NULL, mess_key_at=NULL WHERE id=?`, id)
	}
	return s.updateUser(ctx, `UPDATE users SET mess_key=?, mess_key_at=? WHERE id=?`, sealed, now.Unix(), id)
}

// RemoveUser deletes a user with their sessions and MCP tokens.
func (s *Store) RemoveUser(ctx context.Context, id int64) error {
	return s.updateUser(ctx, `DELETE FROM users WHERE id=?`, id)
}

// ---- sessions ----

type SessionRecord struct {
	UserID    int64
	CSRF      string
	ExpiresAt time.Time
}

func (s *Store) AddSession(ctx context.Context, tokenHash []byte, userID int64, csrf string, now, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(token_hash,user_id,csrf,created_at,expires_at) VALUES(?,?,?,?,?)`,
		tokenHash, userID, csrf, now.Unix(), expires.Unix())
	return err
}

// Session returns an unexpired session, or nil.
func (s *Store) Session(ctx context.Context, tokenHash []byte, now time.Time) (*SessionRecord, error) {
	var r SessionRecord
	var exp int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id,csrf,expires_at FROM sessions WHERE token_hash=? AND expires_at>?`, tokenHash, now.Unix()).Scan(&r.UserID, &r.CSRF, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.ExpiresAt = time.Unix(exp, 0)
	return &r, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, tokenHash)
	return err
}

// DeleteSessions ends every session of a user except keep, which may be nil.
func (s *Store) DeleteSessions(ctx context.Context, userID int64, keep []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=? AND token_hash IS NOT ?`, userID, keep)
	return err
}

// ---- MCP tokens ----

type TokenRecord struct {
	ID        int64
	UserID    int64
	Label     string
	Prefix    string
	CreatedAt time.Time
	LastUsed  time.Time
	Revoked   bool
}

func (s *Store) AddToken(ctx context.Context, userID int64, label string, tokenHash []byte, prefix string, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO mcp_tokens(user_id,label,token_hash,prefix,created_at) VALUES(?,?,?,?,?)`,
		userID, label, tokenHash, prefix, now.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// TokenUser returns the user ID of an unrevoked token and records its use.
// It returns 0 when the token is unknown or revoked.
func (s *Store) TokenUser(ctx context.Context, tokenHash []byte, now time.Time) (int64, error) {
	var id, user int64
	err := s.db.QueryRowContext(ctx, `SELECT id,user_id FROM mcp_tokens WHERE token_hash=? AND revoked_at IS NULL`, tokenHash).Scan(&id, &user)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	// A failed timestamp write only loses last_used.
	s.db.ExecContext(ctx, `UPDATE mcp_tokens SET last_used=? WHERE id=?`, now.Unix(), id)
	return user, nil
}

// Tokens lists a user's MCP tokens, newest first. userID 0 lists everyone's.
func (s *Store) Tokens(ctx context.Context, userID int64) ([]TokenRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,user_id,label,prefix,created_at,last_used,revoked_at FROM mcp_tokens WHERE ?=0 OR user_id=? ORDER BY id DESC`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TokenRecord{}
	for rows.Next() {
		var t TokenRecord
		var created int64
		var used, revoked sql.NullInt64
		if err := rows.Scan(&t.ID, &t.UserID, &t.Label, &t.Prefix, &created, &used, &revoked); err != nil {
			return nil, err
		}
		t.CreatedAt = time.Unix(created, 0)
		if used.Valid {
			t.LastUsed = time.Unix(used.Int64, 0)
		}
		t.Revoked = revoked.Valid
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken revokes token id. A userID other than 0 must own it.
func (s *Store) RevokeToken(ctx context.Context, userID, id int64, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE mcp_tokens SET revoked_at=? WHERE id=? AND revoked_at IS NULL AND (?=0 OR user_id=?)`, now.Unix(), id, userID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("no such active token")
	}
	return nil
}
