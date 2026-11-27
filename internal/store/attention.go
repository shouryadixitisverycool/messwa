package store

import (
	"context"
	"time"
)

const attentionSchema = `CREATE TABLE IF NOT EXISTS attention_resolved (
	user_id INTEGER NOT NULL,
	issue TEXT NOT NULL,
	kind TEXT NOT NULL DEFAULT '',
	resolved_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(user_id,issue)
);`

func (s *Store) AttentionResolved(ctx context.Context, user int64) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT issue FROM attention_resolved WHERE user_id=?", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (s *Store) ResolveAttention(ctx context.Context, user int64, id, kind string, at time.Time) error {
	var ts int64
	if !at.IsZero() {
		ts = at.Unix()
	}
	_, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO attention_resolved(user_id,issue,kind,resolved_at) VALUES(?,?,?,?)", user, id, kind, ts)
	return err
}

// ResolvedAttentionCount returns how many items of kind the user resolved after since (unix seconds).
func (s *Store) ResolvedAttentionCount(ctx context.Context, user int64, kind string, since int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		"SELECT count(*) FROM attention_resolved WHERE user_id=? AND kind=? AND resolved_at > ?",
		user, kind, since).Scan(&n)
	return n, err
}
