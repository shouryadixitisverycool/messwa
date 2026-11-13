package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
)

func (s *Store) boardDB() *sql.DB {
	if s.board != nil {
		return s.board
	}
	return s.db
}

// OpenPrivate isolates all device data while sharing only market tables.
func OpenPrivate(path string, board *Store) (*Store, error) {
	s, err := Open(path)
	if err == nil {
		s.board = board.boardDB()
	}
	return s, err
}

// AccountDir pins the legacy database and session to the first existing admin.
// The mapping survives deletion and role changes. New accounts never inherit it.
func (s *Store) AccountDir(ctx context.Context, root string, id int64) (string, error) {
	if id <= 0 {
		return "", fmt.Errorf("create a messwa user first")
	}
	if err := s.migrateOwnership(ctx); err != nil {
		return "", err
	}

	var owner int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM device_ownership WHERE slot=1`).Scan(&owner)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	if owner == id {
		return root, nil
	}
	return filepath.Join(root, fmt.Sprintf("user-%d", id)), nil
}

func (s *Store) AssignLegacyTasks(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sell_tasks SET user_id=? WHERE user_id=0`, id)
	return err
}

// OpenAccount opens a private database and owns the shared board handle too.
func OpenAccount(path, boardPath string) (*Store, error) {
	if path == boardPath {
		return Open(path)
	}
	board, err := Open(boardPath)
	if err != nil {
		return nil, err
	}
	st, err := OpenPrivate(path, board)
	if err != nil {
		board.Close()
		return nil, err
	}
	st.closeBoard = board
	return st, nil
}

// migrateOwnership pins existing private data before account-management commands can change its owner.
func (s *Store) migrateOwnership(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS device_ownership (slot INTEGER PRIMARY KEY,user_id INTEGER NOT NULL)`); err != nil {
		return err
	}
	result, err := tx.Exec(`INSERT OR IGNORE INTO device_ownership(slot,user_id) SELECT 1,id FROM users WHERE role='admin' AND disabled=0 ORDER BY id LIMIT 1`)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n > 0 {
		if _, err = tx.Exec(`UPDATE sell_tasks SET user_id=(SELECT user_id FROM device_ownership WHERE slot=1)`); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}

	return nil
}
