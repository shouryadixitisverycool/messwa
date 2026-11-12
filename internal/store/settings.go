package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// settingsSchema holds what admins saved on the dashboard's settings page,
// one row per key. config.toml supplies every key that has no row.
const settingsSchema = `
CREATE TABLE IF NOT EXISTS settings (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL,
	updated_at INTEGER NOT NULL,
	updated_by INTEGER
);
CREATE TABLE IF NOT EXISTS settings_version (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	v  INTEGER NOT NULL
);
`

// Settings returns every saved setting by key, and a version that changes
// whenever one is saved or reset.
func (s *Store) Settings(ctx context.Context) (map[string]json.RawMessage, int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, 0, err
		}
		out[k] = json.RawMessage(v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	v, err := s.SettingsVersion(ctx)
	return out, v, err
}

// SettingsVersion changes whenever a setting is saved or reset, so run can
// pick up a save made by another process, such as messwa dashboard.
func (s *Store) SettingsVersion(ctx context.Context) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT v FROM settings_version WHERE id=1), 0)`).Scan(&v)
	return v, err
}

// SaveSettings replaces the saved settings with all, in one transaction,
// recording who saved them. Keys missing from all go back to config.toml.
func (s *Store) SaveSettings(ctx context.Context, all map[string]json.RawMessage, by int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old := map[string]string{}
	rows, err := tx.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return err
		}
		old[k] = v
	}
	rows.Close()
	now := time.Now().Unix()
	var who sql.NullInt64
	if by > 0 {
		who = sql.NullInt64{Int64: by, Valid: true}
	}
	for k, v := range all {
		if old[k] == string(v) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at, updated_by) VALUES(?,?,?,?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at, updated_by=excluded.updated_by`,
			k, string(v), now, who); err != nil {
			return err
		}
	}
	for k := range old {
		if _, ok := all[k]; !ok {
			if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, k); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings_version(id, v) VALUES(1, 1) ON CONFLICT(id) DO UPDATE SET v = v + 1`); err != nil {
		return err
	}
	return tx.Commit()
}
