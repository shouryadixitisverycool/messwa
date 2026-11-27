package store

import (
	"context"
	"database/sql"
	"errors"
)

const conversationSchema = `CREATE TABLE IF NOT EXISTS sale_messages (
 task_id INTEGER NOT NULL, peer TEXT NOT NULL, id TEXT NOT NULL, at INTEGER NOT NULL,
 from_me INTEGER NOT NULL, text TEXT NOT NULL, kind TEXT NOT NULL, checking TEXT NOT NULL,
 media BLOB, mime TEXT NOT NULL DEFAULT '', PRIMARY KEY(task_id,peer,id));`

type SaleMessage struct {
	ID       string `json:"id"`
	At       int64  `json:"at"`
	FromMe   bool   `json:"from_me"`
	Text     string `json:"text"`
	Kind     string `json:"kind"`
	Checking string `json:"checking,omitempty"`
	Media    bool   `json:"media"`
}

// ConversationTask limits recording to a buyer's current sale, or a reply
// explicitly quoting one of our offers. It never records unrelated DMs.
// A sale that passed the buyer to another sale in its pool keeps a moved
// row, which is skipped so their messages follow them.
func (s *Store) ConversationTask(ctx context.Context, peer, alt, quote string, at int64) (int64, string, error) {
	var id int64
	var canonical string
	err := s.db.QueryRowContext(ctx, `SELECT t.id,coalesce(b.peer,?) FROM sell_tasks t
 LEFT JOIN sell_task_buyers b ON b.task_id=t.id AND b.state!=? AND (b.peer IN (?,?) OR (b.alt!='' AND b.alt IN (?,?)))
 WHERE t.created_at<=? AND t.ends_at>=? AND
 (b.peer IS NOT NULL OR (?!='' AND EXISTS(SELECT 1 FROM sell_task_offers o WHERE o.task_id=t.id AND o.offer_id=?)))
 ORDER BY t.created_at DESC,t.id DESC LIMIT 1`, peer, BuyerMoved, peer, alt, peer, alt, at, at, quote, quote).Scan(&id, &canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return id, canonical, err
}

// OldPaymentText rewrites the text of a payment message recorded before the
// payment card: its amount line, "\n\nRetained payment details:\n" and a
// JSON dump of the payment. Package main sets it, since decoding the
// payment lives there; Open then rewrites those rows to the current form.
var OldPaymentText func(old string) string

// migratePaymentText rewrites old payment rows with OldPaymentText. Only
// rows in the old shape match, so a new row whose note a buyer typed to
// look like the dump is never touched, and a second run changes nothing.
func migratePaymentText(db *sql.DB) error {
	if OldPaymentText == nil {
		return nil
	}
	rows, err := db.Query(`SELECT rowid,text FROM sale_messages WHERE kind='payment'
 AND (text LIKE 'Reported amount: % (unverified).'||char(10)||char(10)||'Retained payment details:'||char(10)||'%'
  OR text LIKE 'Amount unavailable.'||char(10)||char(10)||'Retained payment details:'||char(10)||'%')`)
	if err != nil {
		return err
	}
	type row struct {
		id   int64
		text string
	}
	var old []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.text); err != nil {
			rows.Close()
			return err
		}
		old = append(old, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(old) == 0 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range old {
		if _, err := tx.Exec(`UPDATE sale_messages SET text=? WHERE rowid=?`, OldPaymentText(r.text), r.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AddSaleMessage(ctx context.Context, task int64, peer string, m SaleMessage) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO sale_messages(task_id,peer,id,at,from_me,text,kind,checking) VALUES(?,?,?,?,?,?,?,?)`, task, peer, m.ID, m.At, m.FromMe, m.Text, m.Kind, m.Checking)
	return err
}
func (s *Store) SetSaleMessageMedia(ctx context.Context, task int64, peer, id, mime string, data []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sale_messages SET media=?,mime=? WHERE task_id=? AND peer=? AND id=?`, data, mime, task, peer, id)
	return err
}
func (s *Store) SaleMessages(ctx context.Context, task int64, peer string) ([]SaleMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,at,from_me,text,kind,checking,media IS NOT NULL FROM sale_messages WHERE task_id=? AND peer=? ORDER BY at,rowid`, task, peer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SaleMessage{}
	for rows.Next() {
		var m SaleMessage
		if err := rows.Scan(&m.ID, &m.At, &m.FromMe, &m.Text, &m.Kind, &m.Checking, &m.Media); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) SaleMessageMedia(ctx context.Context, task int64, peer, id string) ([]byte, string, error) {
	var data []byte
	var mime string
	err := s.db.QueryRowContext(ctx, `SELECT media,mime FROM sale_messages WHERE task_id=? AND peer=? AND id=? AND media IS NOT NULL`, task, peer, id).Scan(&data, &mime)
	return data, mime, err
}
