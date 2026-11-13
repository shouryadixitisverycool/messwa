package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Things you do to a running sell task by hand, from the dashboard or an
// MCP tool: record a sale you made yourself, add a buyer the bot should
// quote, and mute a buyer the bot should leave alone.

// sellActionColumns are added to sell_task_buyers. muted_at is set while
// the bot must send that buyer nothing for the task; name is the name you
// gave a buyer you added.
var sellActionColumns = []string{
	"muted_at INTEGER",
	"name TEXT NOT NULL DEFAULT ''",
}

// PaidManual marks a sell task's sale you made yourself outside the bot and
// recorded with mark_sell_task_sold. Nothing verified the payment.
const PaidManual = "manual"

// ErrNoSuchBuyer means the buyer never joined the task.
var ErrNoSuchBuyer = errors.New("that person is not one of the task's buyers")

// NameSellTaskBuyer records the name you gave a buyer.
func (s *Store) NameSellTaskBuyer(ctx context.Context, id int64, peer, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sell_task_buyers SET name=? WHERE task_id=? AND peer=?`, name, id, peer)
	return err
}

// MuteSellTaskBuyer mutes or unmutes the buyer whose peer or alt is any of
// peers, and returns their peer. Their state and history stay as they are.
// It reports changed=false when they were already in that state.
func (s *Store) MuteSellTaskBuyer(ctx context.Context, id int64, muted bool, at time.Time, peers ...string) (peer string, changed bool, err error) {
	peer, err = s.findSellTaskBuyer(ctx, id, peers...)
	if err != nil {
		return "", false, err
	}
	q := `UPDATE sell_task_buyers SET muted_at=? WHERE task_id=? AND peer=? AND muted_at IS NULL`
	args := []any{at.UnixNano(), id, peer}
	if !muted {
		q = `UPDATE sell_task_buyers SET muted_at=NULL WHERE task_id=? AND peer=? AND muted_at IS NOT NULL`
		args = []any{id, peer}
	}
	r, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return peer, false, err
	}
	n, err := r.RowsAffected()
	return peer, n == 1, err
}

// findSellTaskBuyer returns the peer of the task's buyer matching any of
// peers by peer or alt.
func (s *Store) findSellTaskBuyer(ctx context.Context, id int64, peers ...string) (string, error) {
	for _, p := range peers {
		if p == "" {
			continue
		}
		var peer string
		err := s.db.QueryRowContext(ctx, `SELECT peer FROM sell_task_buyers WHERE task_id=? AND ? IN (peer, alt) LIMIT 1`, id, p).Scan(&peer)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		return peer, err
	}
	return "", ErrNoSuchBuyer
}

// SellTaskBuyerMuted reports whether the task's buyer matching any of peers
// is muted.
func (s *Store) SellTaskBuyerMuted(ctx context.Context, id int64, peers ...string) (bool, error) {
	for _, p := range peers {
		if p == "" {
			continue
		}
		var muted bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sell_task_buyers WHERE task_id=? AND ? IN (peer, alt) AND muted_at IS NOT NULL)`, id, p).Scan(&muted); err != nil || muted {
			return muted, err
		}
	}
	return false, nil
}

// ClaimSoldNotice moves a buyer who is still in the running to told_sold
// before the bot tells them the meal sold. That includes a buyer asked to
// pay when you sold the meal yourself; after a bot sale nobody else is in
// that state. A muted buyer is never claimed and keeps their state.
func (s *Store) ClaimSoldNotice(ctx context.Context, id int64, peer string) (bool, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE sell_task_buyers SET state='told_sold'
 WHERE task_id=? AND peer=? AND state IN ('queued','negotiating','agreed','parked','offered') AND muted_at IS NULL`, id, peer)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

// TaskManualSale is a sale of an active task's meal that you made yourself.
type TaskManualSale struct {
	// Peer is the buyer's JID, or "" when you only gave a name.
	Peer      string
	BuyerName string
	Amount    string // INR, like "55.00"
	At        time.Time
}

// RecordTaskManualSale writes a sale you made yourself to the delivery
// ledger as sent, paid and sent at m.At, with payment kind PaidManual, and
// ties it to the task. The task must still be active. A meal with no row
// gets one; a reservation the task armed for a buyer becomes this sale. A
// row that was paid, sent or handed off is left alone and the call fails.
// When Peer is one of the task's buyers, they become bought.
func (s *Store) RecordTaskManualSale(ctx context.Context, taskID int64, m TaskManualSale) error {
	if amount, err := ParsePaise(m.Amount); err != nil {
		return err
	} else if amount <= 0 {
		return errors.New("sale amount must be positive")
	}
	if m.Peer == "" && m.BuyerName == "" {
		return errors.New("a sale needs a buyer: a phone number, a JID or a name")
	}
	if m.At.IsZero() {
		return errors.New("a sale needs a time")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var date, meal, mess, diet, state string
	err = tx.QueryRowContext(ctx, `SELECT date,meal,mess,diet,state FROM sell_tasks WHERE id=?`, taskID).Scan(&date, &meal, &mess, &diet, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("no sell task %d", taskID)
	}
	if err != nil {
		return err
	}
	if state != TaskActive {
		return fmt.Errorf("sell task %d is %s", taskID, state)
	}
	var dstate string
	var dtask sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT state,task_id FROM qr_deliveries WHERE date=? AND meal=?`, date, meal).Scan(&dstate, &dtask)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO qr_deliveries(date,meal,peer,amount,image_hash,armed_at,state,mess,diet,buyer_name,paid_at,sent_at,payment_kind,task_id)
 VALUES(?,?,?,?,'manual',?,'sent',?,?,?,?,?,?,?)`, date, meal, m.Peer, m.Amount, m.At.Unix(), mess, diet, m.BuyerName, m.At.Unix(), m.At.Unix(), PaidManual, taskID)
	case err != nil:
	case (dstate == "armed" || dstate == "handed_off") && dtask.Valid && dtask.Int64 == taskID:
		var result sql.Result
		result, err = tx.ExecContext(ctx, `UPDATE qr_deliveries SET state='sent',peer=?,amount=?,image_hash='manual',payment_kind=?,paid_at=?,sent_at=?,buyer_name=?
 WHERE date=? AND meal=? AND state IN ('armed','handed_off') AND payment_id IS NULL`, m.Peer, m.Amount, PaidManual, m.At.Unix(), m.At.Unix(), m.BuyerName, date, meal)
		if err == nil {
			n, rowsErr := result.RowsAffected()
			if rowsErr != nil {
				return rowsErr
			}
			if n != 1 {
				return ErrAlreadySold
			}
		}
	default:
		return fmt.Errorf("%w (state %s); the bot or an earlier sale already has it", ErrAlreadySold, dstate)
	}
	if err != nil {
		return err
	}
	if m.Peer != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE sell_task_buyers SET state='bought' WHERE task_id=? AND ? IN (peer, alt)`, taskID, m.Peer); err != nil {
			return err
		}
	}
	return tx.Commit()
}
