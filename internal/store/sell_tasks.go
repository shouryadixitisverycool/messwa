package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A sell task sells one registered meal from inside run. Every message it
// sends is claimed here first, so a restart resumes the task without
// repeating the group post, a payment prompt or a "sorry sold".
const sellTaskSchema = `
CREATE TABLE IF NOT EXISTS sell_tasks (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 date TEXT NOT NULL,
 meal TEXT NOT NULL,
 min_paise INTEGER NOT NULL,
 max_paise INTEGER NOT NULL,
 user_id INTEGER NOT NULL,
 account_hash TEXT NOT NULL DEFAULT '',
 mess TEXT NOT NULL DEFAULT '',
 diet TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'active',
 detail TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 ends_at INTEGER NOT NULL,
 ended_at INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS sell_tasks_one_active ON sell_tasks(date, meal) WHERE state='active';
CREATE TABLE IF NOT EXISTS sell_task_offers (
 task_id INTEGER NOT NULL,
 group_jid TEXT NOT NULL,
 offer_id TEXT NOT NULL,
 state TEXT NOT NULL,
 posted_at INTEGER NOT NULL,
 PRIMARY KEY(task_id, group_jid)
);
CREATE TABLE IF NOT EXISTS sell_task_buyers (
 task_id INTEGER NOT NULL,
 peer TEXT NOT NULL,
 alt TEXT NOT NULL DEFAULT '',
 joined_at INTEGER NOT NULL,
 state TEXT NOT NULL DEFAULT 'queued',
 amount TEXT NOT NULL DEFAULT '',
 pay_until INTEGER,
 PRIMARY KEY(task_id, peer)
);`

// Sell task states. Only an active task may send anything.
const (
	TaskActive    = "active"
	TaskSold      = "sold"
	TaskCancelled = "cancelled"
	TaskExpired   = "expired"
	TaskFailed    = "failed"
)

// Offer post states. A post left in OfferPosting was claimed but its send
// never confirmed; it is never sent again.
const (
	OfferPosting = "posting"
	OfferPosted  = "posted"
	OfferFailed  = "failed"
)

// Buyer states. Each message to a buyer moves them to a new state before
// it is sent: queued -> offered (payment prompt) and queued -> told_sold.
const (
	BuyerQueued   = "queued"
	BuyerOffered  = "offered"
	BuyerPassed   = "passed"
	BuyerTimedOut = "timed_out"
	BuyerBought   = "bought"
	BuyerSkipped  = "skipped"
	BuyerToldSold = "told_sold"
)

type SellTask struct {
	ID                 int64
	Date, Meal         string
	MinPaise, MaxPaise int64
	// UserID is whose mess API key reads the registration and QR; 0 is the
	// owner stand-in. AccountHash binds the task to that portal account.
	UserID      int64
	AccountHash string
	Mess, Diet  string
	State       string
	Detail      string
	CreatedAt   time.Time
	EndsAt      time.Time
	EndedAt     time.Time // zero while active
}

type SellOffer struct {
	Group, ID, State string
	PostedAt         time.Time
}

type SellBuyer struct {
	// Peer is the buyer's phone-number JID when known, else their LID.
	// Alt is the chat they replied from when that differs.
	Peer, Alt string
	JoinedAt  time.Time
	State     string
	Amount    string    // the price asked, once offered
	PayUntil  time.Time // zero until offered
}

// CreateSellTask starts a task unless the meal already has one running or
// any delivery record, sold or not.
func (s *Store) CreateSellTask(ctx context.Context, t SellTask) (int64, error) {
	if t.MinPaise <= 0 || t.MaxPaise < t.MinPaise {
		return 0, errors.New("prices must be positive with max at least min")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM qr_deliveries WHERE date=? AND meal=?)`, t.Date, t.Meal).Scan(&exists); err != nil {
		return 0, err
	}
	if exists {
		return 0, errors.New("meal already has a delivery record; check the sales page")
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sell_tasks WHERE date=? AND meal=? AND state='active')`, t.Date, t.Meal).Scan(&exists); err != nil {
		return 0, err
	}
	if exists {
		return 0, errors.New("a sell task for this meal is already running")
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO sell_tasks(date,meal,min_paise,max_paise,user_id,account_hash,mess,diet,created_at,ends_at)
 VALUES(?,?,?,?,?,?,?,?,?,?)`, t.Date, t.Meal, t.MinPaise, t.MaxPaise, t.UserID, t.AccountHash, t.Mess, t.Diet, t.CreatedAt.Unix(), t.EndsAt.Unix())
	if err != nil {
		return 0, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

const sellTaskCols = `id,date,meal,min_paise,max_paise,user_id,account_hash,mess,diet,state,detail,created_at,ends_at,coalesce(ended_at,0)`

func scanSellTask(row interface{ Scan(...any) error }) (SellTask, error) {
	var t SellTask
	var created, ends, ended int64
	err := row.Scan(&t.ID, &t.Date, &t.Meal, &t.MinPaise, &t.MaxPaise, &t.UserID, &t.AccountHash, &t.Mess, &t.Diet, &t.State, &t.Detail, &created, &ends, &ended)
	t.CreatedAt, t.EndsAt = time.Unix(created, 0), time.Unix(ends, 0)
	if ended != 0 {
		t.EndedAt = time.Unix(ended, 0)
	}
	return t, err
}

// GetSellTask returns nil when there is no such task.
func (s *Store) GetSellTask(ctx context.Context, id int64) (*SellTask, error) {
	t, err := scanSellTask(s.db.QueryRowContext(ctx, `SELECT `+sellTaskCols+` FROM sell_tasks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// SellTasks returns active tasks and tasks created since since, newest first.
func (s *Store) SellTasks(ctx context.Context, since time.Time) ([]SellTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sellTaskCols+` FROM sell_tasks WHERE state='active' OR created_at>=? ORDER BY id DESC`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SellTask
	for rows.Next() {
		t, err := scanSellTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// EndSellTask moves an active task to its final state. It reports false
// when the task had already ended.
func (s *Store) EndSellTask(ctx context.Context, id int64, state, detail string, at time.Time) (bool, error) {
	if state == TaskActive {
		return false, errors.New("cannot end a task as active")
	}
	r, err := s.db.ExecContext(ctx, `UPDATE sell_tasks SET state=?,detail=?,ended_at=? WHERE id=? AND state='active'`, state, detail, at.Unix(), id)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

// ClaimOfferPost records a group post before it is sent. Each group gets at
// most one post per task, whatever happens to the send.
func (s *Store) ClaimOfferPost(ctx context.Context, id int64, group, offerID string, at time.Time) error {
	r, err := s.db.ExecContext(ctx, `INSERT INTO sell_task_offers(task_id,group_jid,offer_id,state,posted_at)
 SELECT ?,?,?,'posting',? WHERE EXISTS(SELECT 1 FROM sell_tasks WHERE id=? AND state='active')
 ON CONFLICT(task_id,group_jid) DO NOTHING`, id, group, offerID, at.Unix(), id)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return errors.Join(err, errors.New("offer already posted to this group, or the task has ended"))
	}
	return nil
}

func (s *Store) SetOfferState(ctx context.Context, id int64, offerID, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sell_task_offers SET state=? WHERE task_id=? AND offer_id=? AND state='posting'`, state, id, offerID)
	return err
}

// OfferClaimed reports whether a post with this ID to this group was
// claimed and not yet resolved, which is the only time it may be sent.
func (s *Store) OfferClaimed(ctx context.Context, id int64, group, offerID string) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sell_task_offers WHERE task_id=? AND group_jid=? AND offer_id=? AND state='posting')`, id, group, offerID).Scan(&ok)
	return ok, err
}

func (s *Store) SellTaskOffers(ctx context.Context, id int64) ([]SellOffer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT group_jid,offer_id,state,posted_at FROM sell_task_offers WHERE task_id=? ORDER BY rowid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SellOffer
	for rows.Next() {
		var o SellOffer
		var at int64
		if err := rows.Scan(&o.Group, &o.ID, &o.State, &at); err != nil {
			return nil, err
		}
		o.PostedAt = time.Unix(at, 0)
		out = append(out, o)
	}
	return out, rows.Err()
}

// AddSellTaskBuyer queues someone who replied privately to the task's
// offer. Only queued buyers can ever receive a message from the task. It
// reports false for repeat replies and ended tasks.
func (s *Store) AddSellTaskBuyer(ctx context.Context, id int64, peer, alt string, at time.Time) (bool, error) {
	if alt == peer {
		alt = ""
	}
	r, err := s.db.ExecContext(ctx, `INSERT INTO sell_task_buyers(task_id,peer,alt,joined_at)
 SELECT ?,?,?,? WHERE EXISTS(SELECT 1 FROM sell_tasks WHERE id=? AND state='active')
 ON CONFLICT(task_id,peer) DO NOTHING`, id, peer, alt, at.UnixNano(), id)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

// IsSellTaskBuyer reports whether any of peers replied to the task's offer.
func (s *Store) IsSellTaskBuyer(ctx context.Context, id int64, peers ...string) (bool, error) {
	for _, p := range peers {
		if p == "" {
			continue
		}
		var ok bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sell_task_buyers WHERE task_id=? AND ? IN (peer, alt))`, id, p).Scan(&ok); err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

// SellTaskBuyers returns buyers in the order they replied.
func (s *Store) SellTaskBuyers(ctx context.Context, id int64) ([]SellBuyer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT peer,alt,joined_at,state,amount,coalesce(pay_until,0) FROM sell_task_buyers WHERE task_id=? ORDER BY joined_at, rowid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SellBuyer
	for rows.Next() {
		var b SellBuyer
		var joined, until int64
		if err := rows.Scan(&b.Peer, &b.Alt, &joined, &b.State, &b.Amount, &until); err != nil {
			return nil, err
		}
		b.JoinedAt = time.Unix(0, joined)
		if until != 0 {
			b.PayUntil = time.Unix(until, 0)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// MoveSellTaskBuyer changes a buyer's state only from the expected one, so
// a message claimed by one transition can't be claimed again. Moving to
// BuyerOffered records the price asked and the payment deadline.
func (s *Store) MoveSellTaskBuyer(ctx context.Context, id int64, peer, from, to, amount string, payUntil time.Time) (bool, error) {
	q := `UPDATE sell_task_buyers SET state=? WHERE task_id=? AND peer=? AND state=?`
	args := []any{to, id, peer, from}
	if to == BuyerOffered {
		q = `UPDATE sell_task_buyers SET state=?,amount=?,pay_until=? WHERE task_id=? AND peer=? AND state=?
 AND EXISTS(SELECT 1 FROM sell_tasks WHERE id=? AND state='active')`
		args = []any{to, amount, payUntil.Unix(), id, peer, from, id}
	}
	r, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

// ArmTaskDelivery reserves the meal for one buyer of an active task at a
// price no lower than the task's floor. Like ArmNewDelivery, it fails when
// the meal already has a delivery record.
func (s *Store) ArmTaskDelivery(ctx context.Context, taskID int64, d Delivery) (time.Time, error) {
	amount, err := ParsePaise(d.Amount)
	if err != nil {
		return time.Time{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	var minPaise int64
	var date, meal, state string
	err = tx.QueryRowContext(ctx, `SELECT min_paise,date,meal,state FROM sell_tasks WHERE id=?`, taskID).Scan(&minPaise, &date, &meal, &state)
	if err != nil {
		return time.Time{}, err
	}
	switch {
	case state != TaskActive:
		return time.Time{}, fmt.Errorf("sell task is %s", state)
	case date != d.Date || meal != d.Meal:
		return time.Time{}, errors.New("delivery is for a different meal than the task")
	case amount < minPaise:
		return time.Time{}, fmt.Errorf("INR %s is below the task's floor of INR %s", d.Amount, FormatPaise(minPaise))
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO qr_deliveries(date,meal,peer,amount,image_hash,armed_at,mess,diet,task_id)
 VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(date,meal) DO NOTHING`, d.Date, d.Meal, d.Peer, d.Amount, d.ImageHash, d.ArmedAt.Unix(), d.Mess, d.Diet, taskID)
	if err != nil {
		return time.Time{}, err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return time.Time{}, errors.Join(err, errors.New("meal already has a delivery record"))
	}
	return d.ArmedAt.Truncate(time.Second), tx.Commit()
}

// TaskDelivery returns the delivery record a task armed, or nil.
func (s *Store) TaskDelivery(ctx context.Context, taskID int64) (*Delivery, string, error) {
	var d Delivery
	var state string
	var armed int64
	err := s.db.QueryRowContext(ctx, `SELECT date,meal,peer,amount,image_hash,armed_at,mess,diet,state FROM qr_deliveries WHERE task_id=?`, taskID).
		Scan(&d.Date, &d.Meal, &d.Peer, &d.Amount, &d.ImageHash, &armed, &d.Mess, &d.Diet, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	d.ArmedAt = time.Unix(armed, 0)
	return &d, state, nil
}

// ParsePaise reads an INR amount with two decimal places, like "60.00".
func ParsePaise(amount string) (int64, error) {
	rupees, paise, ok := strings.Cut(amount, ".")
	if !ok || len(paise) != 2 || rupees == "" || len(rupees) > 12 {
		return 0, fmt.Errorf("%q is not an INR amount with two decimal places", amount)
	}
	r, err1 := strconv.ParseUint(rupees, 10, 64)
	p, err2 := strconv.ParseUint(paise, 10, 64)
	if err1 != nil || err2 != nil {
		return 0, fmt.Errorf("%q is not an INR amount with two decimal places", amount)
	}
	return int64(r*100 + p), nil
}

// FormatPaise writes paise as the amount WhatsApp Pay reports, like "60.00".
func FormatPaise(p int64) string { return fmt.Sprintf("%d.%02d", p/100, p%100) }
