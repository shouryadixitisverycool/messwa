package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const visitSchema = `CREATE TABLE IF NOT EXISTS home_visits (
	user_id INTEGER PRIMARY KEY,
	last_seen INTEGER NOT NULL DEFAULT 0,
	prev_seen INTEGER NOT NULL DEFAULT 0
);`

// HomeVisitGap is how long a user must be away before their next ping starts a new visit.
const HomeVisitGap = 30 * time.Minute

// PingHomeVisit records a visit ping for user at now and returns the previous
// visit's end timestamp (unix seconds), or 0 on the user's first visit.
func (s *Store) PingHomeVisit(ctx context.Context, user int64, now time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var lastSeen, prevSeen int64
	err = tx.QueryRowContext(ctx, "SELECT last_seen, prev_seen FROM home_visits WHERE user_id=?", user).Scan(&lastSeen, &prevSeen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	nowUnix := now.Unix()
	if lastSeen == 0 || nowUnix-lastSeen > int64(HomeVisitGap/time.Second) {
		prevSeen = lastSeen
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO home_visits(user_id, last_seen, prev_seen) VALUES(?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET last_seen=excluded.last_seen, prev_seen=excluded.prev_seen`,
		user, nowUnix, prevSeen)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return prevSeen, nil
}

// HomeVisitSince returns the user's stored previous-visit timestamp (unix seconds)
// without updating last_seen. It returns 0 if the user has no recorded visit.
func (s *Store) HomeVisitSince(ctx context.Context, user int64) (int64, error) {
	var prevSeen int64
	err := s.db.QueryRowContext(ctx, "SELECT prev_seen FROM home_visits WHERE user_id=?", user).Scan(&prevSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return prevSeen, err
}
