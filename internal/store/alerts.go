package store

import (
	"context"
	"time"
)

// AlertStatus records the outcome of trying to deliver an attention item on a
// specific notification channel.
type AlertStatus string

const (
	AlertSent    AlertStatus = "sent"
	AlertOffline AlertStatus = "offline"
	AlertFailed  AlertStatus = "failed"
)

// AlertDelivery is one persisted delivery record for (UserID, ItemID, Channel).
type AlertDelivery struct {
	UserID     int64
	ItemID     string
	Channel    string
	Status     AlertStatus
	Attempts   int
	Detail     string
	RetryAfter time.Time
	UpdatedAt  time.Time
}

const alertSchema = `
CREATE TABLE IF NOT EXISTS alert_deliveries (
	user_id     INTEGER NOT NULL,
	item_id     TEXT    NOT NULL,
	channel     TEXT    NOT NULL,
	status      TEXT    NOT NULL,
	attempts    INTEGER NOT NULL DEFAULT 1,
	detail      TEXT    NOT NULL DEFAULT '',
	retry_after INTEGER NOT NULL DEFAULT 0,
	updated_at  INTEGER NOT NULL,
	PRIMARY KEY (user_id, item_id, channel)
);
CREATE TABLE IF NOT EXISTS alert_sends (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL,
	channel TEXT    NOT NULL,
	sent_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS alert_sends_window ON alert_sends(user_id, channel, sent_at);
`

// AlertDeliveries returns all persisted delivery records for user and channel,
// keyed by item ID.
func (s *Store) AlertDeliveries(ctx context.Context, userID int64, channel string) (map[string]AlertDelivery, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT item_id, status, attempts, detail, retry_after, updated_at
		 FROM alert_deliveries WHERE user_id=? AND channel=?`,
		userID, channel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AlertDelivery{}
	for rows.Next() {
		var d AlertDelivery
		var status string
		var retryAfter, updatedAt int64
		if err := rows.Scan(&d.ItemID, &status, &d.Attempts, &d.Detail, &retryAfter, &updatedAt); err != nil {
			return nil, err
		}
		d.UserID = userID
		d.Channel = channel
		d.Status = AlertStatus(status)
		if retryAfter > 0 {
			d.RetryAfter = time.Unix(retryAfter, 0)
		}
		d.UpdatedAt = time.Unix(updatedAt, 0)
		out[d.ItemID] = d
	}
	return out, rows.Err()
}

// AlertDeliveriesByStatus lists all delivery records for user matching status
// across channels. A fallback channel (such as Web Push) can query AlertOffline
// rows to see items that WhatsApp could not deliver while disconnected.
func (s *Store) AlertDeliveriesByStatus(ctx context.Context, userID int64, status AlertStatus) ([]AlertDelivery, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT item_id, channel, attempts, detail, retry_after, updated_at
		 FROM alert_deliveries WHERE user_id=? AND status=? ORDER BY updated_at DESC`,
		userID, string(status))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertDelivery
	for rows.Next() {
		var d AlertDelivery
		var retryAfter, updatedAt int64
		if err := rows.Scan(&d.ItemID, &d.Channel, &d.Attempts, &d.Detail, &retryAfter, &updatedAt); err != nil {
			return nil, err
		}
		d.UserID = userID
		d.Status = status
		if retryAfter > 0 {
			d.RetryAfter = time.Unix(retryAfter, 0)
		}
		d.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, d)
	}
	return out, rows.Err()
}

// RecordAlertSentBatch persists all itemIDs as delivered on channel and logs
// one outbound message send for rate-limiting, in a single transaction.
func (s *Store) RecordAlertSentBatch(ctx context.Context, userID int64, channel string, itemIDs []string, at time.Time) error {
	if len(itemIDs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := at.Unix()
	for _, id := range itemIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO alert_deliveries(user_id, item_id, channel, status, attempts, detail, retry_after, updated_at)
			 VALUES(?, ?, ?, ?, 1, '', 0, ?)
			 ON CONFLICT(user_id, item_id, channel) DO UPDATE SET
				status=excluded.status,
				attempts=alert_deliveries.attempts + 1,
				detail='',
				retry_after=0,
				updated_at=excluded.updated_at`,
			userID, id, channel, string(AlertSent), ts); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO alert_sends(user_id, channel, sent_at) VALUES(?, ?, ?)`,
		userID, channel, ts); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordAlertOutcome persists a non-sent delivery outcome (AlertOffline or
// AlertFailed) for itemIDs on channel. Existing AlertSent rows are left alone.
func (s *Store) RecordAlertOutcome(ctx context.Context, userID int64, channel string, itemIDs []string, status AlertStatus, detail string, retryAfter, at time.Time) error {
	if len(itemIDs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := at.Unix()
	var retryTS int64
	if !retryAfter.IsZero() {
		retryTS = retryAfter.Unix()
	}
	for _, id := range itemIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO alert_deliveries(user_id, item_id, channel, status, attempts, detail, retry_after, updated_at)
			 VALUES(?, ?, ?, ?, 1, ?, ?, ?)
			 ON CONFLICT(user_id, item_id, channel) DO UPDATE SET
				status=excluded.status,
				attempts=alert_deliveries.attempts + 1,
				detail=excluded.detail,
				retry_after=excluded.retry_after,
				updated_at=excluded.updated_at
			 WHERE alert_deliveries.status != ?`,
			userID, id, channel, string(status), detail, retryTS, ts, string(AlertSent)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CountAlertSendsSince returns how many alert messages were sent on channel
// strictly after since (used for the sliding hourly rate limit).
func (s *Store) CountAlertSendsSince(ctx context.Context, userID int64, channel string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM alert_sends WHERE user_id=? AND channel=? AND sent_at > ?`,
		userID, channel, since.Unix()).Scan(&n)
	return n, err
}

// OldestAlertSendSince returns the earliest send timestamp on channel strictly
// after since, or the zero time if none exist. Used to schedule the next
// rate-limit wakeup without polling in a tight loop.
func (s *Store) OldestAlertSendSince(ctx context.Context, userID int64, channel string, since time.Time) (time.Time, error) {
	var ts int64
	err := s.db.QueryRowContext(ctx,
		`SELECT coalesce(min(sent_at), 0) FROM alert_sends WHERE user_id=? AND channel=? AND sent_at > ?`,
		userID, channel, since.Unix()).Scan(&ts)
	if err != nil || ts == 0 {
		return time.Time{}, err
	}
	return time.Unix(ts, 0), nil
}

func (s *Store) pruneAlertSends(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM alert_sends WHERE sent_at < ?`, before.Unix())
	return err
}
