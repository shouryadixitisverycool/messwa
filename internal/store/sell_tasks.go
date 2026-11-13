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
 handoff TEXT NOT NULL DEFAULT 'never',
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
 seq INTEGER NOT NULL DEFAULT 0,
 offer_id TEXT NOT NULL,
 state TEXT NOT NULL,
 posted_at INTEGER NOT NULL,
 PRIMARY KEY(task_id, group_jid, seq)
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
	// TaskHandedOff ended at the first private reply: that buyer's chat is
	// yours, and the meal's delivery record is handed_off.
	TaskHandedOff = "handed_off"
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
	// BuyerHandedOff is the buyer whose chat the task handed to you.
	BuyerHandedOff = "handed_off"
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
	// Handoff is "never" or "on_reply", as in [sell] handoff.
	Handoff   string
	State     string
	Detail    string
	CreatedAt time.Time
	EndsAt    time.Time
	EndedAt   time.Time // zero while active
	// TargetPaise is the price a negotiation aims for, from the market's
	// median ask; 0 until the task first negotiates.
	TargetPaise int64
}

// SellOffer is one copy of a task's offer in a group. Seq 0 is the first
// copy there and each repost is one higher.
type SellOffer struct {
	Group, ID, State string
	Seq              int
	PostedAt         time.Time
	Error            string // why a failed post failed
	// RevokedAt is when the copy was deleted for everyone after the task
	// was cancelled; zero if it wasn't. RevokeError is why the last
	// attempt to delete it failed or was skipped.
	RevokedAt   time.Time
	RevokeError string
}

type SellBuyer struct {
	// Peer is the buyer's phone-number JID when known, else their LID.
	// Alt is the chat they replied from when that differs.
	Peer, Alt string
	JoinedAt  time.Time
	State     string
	Amount    string    // the price asked, once offered
	PayUntil  time.Time // zero until offered
	// A negotiating task's haggle with this buyer.
	Negotiation
	Agreed   string    // the agreed price, once agreed
	AgreedAt time.Time // zero until agreed
	// Note says why the buyer needs you; Refund is a payment the bot could
	// not honour.
	Note, Refund string
	// Muted is set while the bot sends this buyer nothing for the task.
	// Name is the name you gave a buyer you added.
	Muted bool
	Name  string
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
	if t.Handoff == "" {
		t.Handoff = "never"
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO sell_tasks(date,meal,min_paise,max_paise,user_id,account_hash,mess,diet,handoff,created_at,ends_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?)`, t.Date, t.Meal, t.MinPaise, t.MaxPaise, t.UserID, t.AccountHash, t.Mess, t.Diet, t.Handoff, t.CreatedAt.Unix(), t.EndsAt.Unix())
	if err != nil {
		return 0, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

const sellTaskCols = `id,date,meal,min_paise,max_paise,user_id,account_hash,mess,diet,handoff,state,detail,created_at,ends_at,coalesce(ended_at,0),target_paise`

func scanSellTask(row interface{ Scan(...any) error }) (SellTask, error) {
	var t SellTask
	var created, ends, ended int64
	err := row.Scan(&t.ID, &t.Date, &t.Meal, &t.MinPaise, &t.MaxPaise, &t.UserID, &t.AccountHash, &t.Mess, &t.Diet, &t.Handoff, &t.State, &t.Detail, &created, &ends, &ended, &t.TargetPaise)
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

// SetSellTaskPrices changes an active task's floor and opening price and
// returns the task as it was before. It fails when the task has ended or
// the prices don't make sense; whether the task negotiates is the caller's
// rule to check.
func (s *Store) SetSellTaskPrices(ctx context.Context, id, minPaise, maxPaise int64) (SellTask, error) {
	if minPaise <= 0 || maxPaise < minPaise {
		return SellTask{}, errors.New("prices must be positive with max at least min")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SellTask{}, err
	}
	defer tx.Rollback()
	old, err := scanSellTask(tx.QueryRowContext(ctx, `SELECT `+sellTaskCols+` FROM sell_tasks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return SellTask{}, fmt.Errorf("no sell task %d", id)
	}
	if err != nil {
		return SellTask{}, err
	}
	if old.State != TaskActive {
		return old, fmt.Errorf("sell task %d is %s, not active", id, old.State)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sell_tasks SET min_paise=?,max_paise=? WHERE id=? AND state='active'`, minPaise, maxPaise, id); err != nil {
		return old, err
	}
	return old, tx.Commit()
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

// ClaimOfferPost records copy number seq of the offer in a group before it
// is sent: 0 for the first copy, then one more for each repost. Each
// number is claimed at most once per group, whatever happens to the send,
// and only after the copy before it was claimed.
func (s *Store) ClaimOfferPost(ctx context.Context, id int64, group, offerID string, seq int, at time.Time) error {
	r, err := s.db.ExecContext(ctx, `INSERT INTO sell_task_offers(task_id,group_jid,seq,offer_id,state,posted_at)
 SELECT ?,?,?,?,'posting',? WHERE EXISTS(SELECT 1 FROM sell_tasks WHERE id=? AND state='active')
 AND (SELECT count(*) FROM sell_task_offers WHERE task_id=? AND group_jid=?)=?
 ON CONFLICT(task_id,group_jid,seq) DO NOTHING`, id, group, seq, offerID, at.Unix(), id, id, group, seq)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return errors.Join(err, errors.New("this copy of the offer was already claimed for this group, or the task has ended"))
	}
	return nil
}

// migrateSellOffers moves a sell_task_offers table from before reposts,
// which allowed one copy per group, to one keyed by copy number. Existing
// copies become copy 0.
func migrateSellOffers(db *sql.DB) error {
	var has bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('sell_task_offers') WHERE name='seq')`).Scan(&has); err != nil || has {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`ALTER TABLE sell_task_offers RENAME TO sell_task_offers_old`,
		`CREATE TABLE sell_task_offers (
 task_id INTEGER NOT NULL,
 group_jid TEXT NOT NULL,
 seq INTEGER NOT NULL DEFAULT 0,
 offer_id TEXT NOT NULL,
 state TEXT NOT NULL,
 posted_at INTEGER NOT NULL,
 PRIMARY KEY(task_id, group_jid, seq)
)`,
		`INSERT INTO sell_task_offers(task_id,group_jid,seq,offer_id,state,posted_at)
 SELECT task_id,group_jid,0,offer_id,state,posted_at FROM sell_task_offers_old ORDER BY rowid`,
		`DROP TABLE sell_task_offers_old`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	rows, err := s.db.QueryContext(ctx, `SELECT group_jid,seq,offer_id,state,posted_at,error,revoked_at,revoke_error FROM sell_task_offers WHERE task_id=? ORDER BY posted_at, rowid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SellOffer
	for rows.Next() {
		var o SellOffer
		var at, revoked int64
		if err := rows.Scan(&o.Group, &o.Seq, &o.ID, &o.State, &at, &o.Error, &revoked, &o.RevokeError); err != nil {
			return nil, err
		}
		o.PostedAt = time.Unix(at, 0)
		if revoked != 0 {
			o.RevokedAt = time.Unix(revoked, 0)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// SetOfferRevoked records that a cancelled task deleted its copy for
// everyone.
func (s *Store) SetOfferRevoked(ctx context.Context, id int64, offerID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sell_task_offers SET revoked_at=?,revoke_error='' WHERE task_id=? AND offer_id=?`, at.Unix(), id, offerID)
	return err
}

// SetOfferRevokeError records why a cancelled task's copy is still up.
func (s *Store) SetOfferRevokeError(ctx context.Context, id int64, offerID, why string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sell_task_offers SET revoke_error=? WHERE task_id=? AND offer_id=?`, why, id, offerID)
	return err
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
	rows, err := s.db.QueryContext(ctx, `SELECT peer,alt,joined_at,state,amount,coalesce(pay_until,0),
 ask,rounds,bot_msgs,unknowns,last_offer,quoted,said_lowest,pass_countered,coalesce(started_at,0),agreed,coalesce(agreed_at,0),note,refund,
 muted_at IS NOT NULL,name
 FROM sell_task_buyers WHERE task_id=? ORDER BY joined_at, rowid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SellBuyer
	for rows.Next() {
		var b SellBuyer
		var joined, until, started, agreed int64
		var quoted string
		if err := rows.Scan(&b.Peer, &b.Alt, &joined, &b.State, &b.Amount, &until,
			&b.Ask, &b.Rounds, &b.BotMsgs, &b.Unknowns, &b.LastOffer, &quoted, &b.SaidLowest, &b.PassCountered, &started, &b.Agreed, &agreed, &b.Note, &b.Refund, &b.Muted, &b.Name); err != nil {
			return nil, err
		}
		b.JoinedAt = time.Unix(0, joined)
		if until != 0 {
			b.PayUntil = time.Unix(until, 0)
		}
		if started != 0 {
			b.StartedAt = time.Unix(started, 0)
		}
		if agreed != 0 {
			b.AgreedAt = time.Unix(0, agreed)
		}
		b.Quoted = splitPaise(quoted)
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
// price no lower than the task's floor, or one the bot already quoted or
// agreed with them before a range edit raised it. Like ArmNewDelivery, it fails when
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
		// A price agreed before a range edit raised the floor still stands.
		if ok, err := priceHonoured(ctx, tx, taskID, d.Peer, amount); err != nil || !ok {
			return time.Time{}, errors.Join(err, fmt.Errorf("INR %s is below the task's floor of INR %s", d.Amount, FormatPaise(minPaise)))
		}
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

// HandOffTaskDelivery records that an active task handed its first buyer
// to you, like HandOffDelivery, and ties the record to the task. It fails
// when the meal already has a delivery record.
func (s *Store) HandOffTaskDelivery(ctx context.Context, taskID int64, d Delivery, buyerName string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var date, meal, state string
	if err := tx.QueryRowContext(ctx, `SELECT date,meal,state FROM sell_tasks WHERE id=?`, taskID).Scan(&date, &meal, &state); err != nil {
		return err
	}
	switch {
	case state != TaskActive:
		return fmt.Errorf("sell task is %s", state)
	case date != d.Date || meal != d.Meal:
		return errors.New("delivery is for a different meal than the task")
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO qr_deliveries(date,meal,peer,amount,image_hash,armed_at,state,mess,diet,buyer_name,task_id)
 VALUES(?,?,?,?,?,?,'handed_off',?,?,?,?) ON CONFLICT(date,meal) DO NOTHING`, d.Date, d.Meal, d.Peer, d.Amount, d.ImageHash, d.ArmedAt.Unix(), d.Mess, d.Diet, buyerName, taskID)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return errors.Join(err, errors.New("meal already has a delivery record"))
	}
	return tx.Commit()
}

// TaskBuyerName is the WhatsApp push name recorded with a task's delivery,
// from the payment or the handoff. It is empty when none was recorded.
func (s *Store) TaskBuyerName(ctx context.Context, taskID int64) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT coalesce(buyer_name,'') FROM qr_deliveries WHERE task_id=?`, taskID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
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
