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

// A negotiating sell task talks to every buyer who replies, in parallel,
// and takes payment from one at a time. Each buyer's side of the haggle is
// journaled here: every bot message is claimed by bumping bot_msgs from the
// count it last saw, so a restart never repeats one. Buyer text is never
// stored, only the numbers parsed from it.
var sellBuyerColumns = []string{
	"ask INTEGER NOT NULL DEFAULT 0",        // paise the bot last asked
	"rounds INTEGER NOT NULL DEFAULT 0",     // counter-offers sent
	"bot_msgs INTEGER NOT NULL DEFAULT 0",   // messages the negotiation sent
	"unknowns INTEGER NOT NULL DEFAULT 0",   // unreadable messages restated
	"last_offer INTEGER NOT NULL DEFAULT 0", // paise the buyer last offered
	"quoted TEXT NOT NULL DEFAULT ''",       // every price the bot stated, paise, comma separated
	"said_lowest INTEGER NOT NULL DEFAULT 0",
	"started_at INTEGER", // first bot message
	"agreed TEXT NOT NULL DEFAULT ''",
	"agreed_at INTEGER",
	"note TEXT NOT NULL DEFAULT ''",   // why the buyer needs you
	"refund TEXT NOT NULL DEFAULT ''", // a payment the bot could not honour
	"told TEXT NOT NULL DEFAULT ''",   // once-per-buyer lines sent, comma separated
}

// Buyer states a negotiating task adds. negotiating: the bot is haggling.
// agreed: a price was agreed and the buyer waits for their payment turn.
// parked: the bot stopped talking to them and they need you.
const (
	BuyerNegotiating = "negotiating"
	BuyerAgreed      = "agreed"
	BuyerParked      = "parked"
)

// Negotiation is one buyer's side of a haggle, as journaled.
type Negotiation struct {
	Ask        int64 // paise
	Rounds     int
	BotMsgs    int
	Unknowns   int
	LastOffer  int64 // paise; 0 when none
	Quoted     []int64
	SaidLowest bool
	StartedAt  time.Time
}

func joinPaise(xs []int64) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.FormatInt(x, 10)
	}
	return strings.Join(parts, ",")
}

func splitPaise(s string) []int64 {
	var out []int64
	for _, p := range strings.Split(s, ",") {
		if v, err := strconv.ParseInt(p, 10, 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

const activeTask = `EXISTS(SELECT 1 FROM sell_tasks WHERE id=? AND state='active')`

// honoured is a condition that holds when a price is at or above the
// task's floor, or the bot already quoted or agreed that price with the
// buyer. A range edit that raises the floor never takes back a price the
// bot has said, so those stay allowed; before any edit every quoted price
// was at or above the floor anyway. honouredArgs gives its arguments.
const honoured = `(? >= (SELECT min_paise FROM sell_tasks WHERE id=?) OR EXISTS(SELECT 1 FROM sell_task_buyers
 WHERE task_id=? AND peer=? AND (instr(','||quoted||',', ','||?||',')>0 OR agreed=?)))`

func honouredArgs(id int64, peer string, paise int64) []any {
	return []any{paise, id, id, peer, paise, FormatPaise(paise)}
}

// priceHonoured reports whether peer may be asked or charged paise on task
// id; see honoured.
func priceHonoured(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id int64, peer string, paise int64) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT `+honoured, honouredArgs(id, peer, paise)...).Scan(&ok)
	return ok, err
}

// ClaimNegotiationMessage records n and claims the bot's next message to
// peer, which may then be sent once. A queued buyer becomes negotiating; an
// agreed one stays agreed (the message tells them someone else is paying). It reports false when the message
// was already claimed (before a restart), the buyer has moved on, the task
// ended, or n.Ask is below the task's floor and was never quoted to peer.
func (s *Store) ClaimNegotiationMessage(ctx context.Context, id int64, peer string, sent int, n Negotiation) (bool, error) {
	if n.BotMsgs != sent+1 {
		return false, errors.New("a claim adds exactly one message")
	}
	r, err := s.db.ExecContext(ctx, `UPDATE sell_task_buyers SET state=CASE WHEN state='queued' THEN 'negotiating' ELSE state END,
 ask=?,rounds=?,bot_msgs=?,unknowns=?,last_offer=?,quoted=?,said_lowest=?,started_at=coalesce(started_at,?)
 WHERE task_id=? AND peer=? AND bot_msgs=? AND state IN ('queued','negotiating','agreed') AND `+activeTask+`
 AND `+honoured,
		append([]any{n.Ask, n.Rounds, n.BotMsgs, n.Unknowns, n.LastOffer, joinPaise(n.Quoted), n.SaidLowest, n.StartedAt.Unix(),
			id, peer, sent, id}, honouredArgs(id, peer, n.Ask)...)...)
	if err != nil {
		return false, err
	}
	c, err := r.RowsAffected()
	return c == 1, err
}

// ClaimBuyerLine claims a line the bot says to peer at most once per task,
// such as "only whatsapp pay sorry", before it is sent. It reports false
// when it was already claimed or the task isn't active.
func (s *Store) ClaimBuyerLine(ctx context.Context, id int64, peer, kind string) (bool, error) {
	if kind == "" || strings.Contains(kind, ",") {
		return false, fmt.Errorf("bad line kind %q", kind)
	}
	r, err := s.db.ExecContext(ctx, `UPDATE sell_task_buyers SET told=told||','||?
 WHERE task_id=? AND peer=? AND instr(told||',', ','||?||',')=0 AND `+activeTask,
		kind, id, peer, kind, id)
	if err != nil {
		return false, err
	}
	c, err := r.RowsAffected()
	return c == 1, err
}

// AgreeSellTaskBuyer records the price agreed with a negotiating (or not yet
// answered) buyer. The store refuses a price below the task's floor unless
// the bot already quoted it to them.
func (s *Store) AgreeSellTaskBuyer(ctx context.Context, id int64, peer, amount string, n Negotiation, at time.Time) (bool, error) {
	paise, err := ParsePaise(amount)
	if err != nil {
		return false, err
	}
	r, err := s.db.ExecContext(ctx, `UPDATE sell_task_buyers SET state='agreed',agreed=?,agreed_at=?,ask=?,rounds=?,bot_msgs=?,unknowns=?,last_offer=?,quoted=?,said_lowest=?
 WHERE task_id=? AND peer=? AND state IN ('queued','negotiating') AND `+activeTask+`
 AND `+honoured,
		append([]any{amount, at.UnixNano(), n.Ask, n.Rounds, n.BotMsgs, n.Unknowns, n.LastOffer, joinPaise(n.Quoted), n.SaidLowest,
			id, peer, id}, honouredArgs(id, peer, paise)...)...)
	if err != nil {
		return false, err
	}
	c, err := r.RowsAffected()
	return c == 1, err
}

// RecordBuyerOffer sets the buyer's latest offer, in paise, for a step
// that sends nothing, such as parking them.
func (s *Store) RecordBuyerOffer(ctx context.Context, id int64, peer string, paise int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sell_task_buyers SET last_offer=? WHERE task_id=? AND peer=?`, paise, id, peer)
	return err
}

// MoveSellTaskBuyerFrom moves a buyer to state from any of from.
func (s *Store) MoveSellTaskBuyerFrom(ctx context.Context, id int64, peer string, from []string, to string) (bool, error) {
	if len(from) == 0 {
		return false, errors.New("no state to move from")
	}
	args := []any{to, id, peer}
	for _, f := range from {
		args = append(args, f)
	}
	r, err := s.db.ExecContext(ctx, `UPDATE sell_task_buyers SET state=? WHERE task_id=? AND peer=? AND state IN (`+placeholders(len(from))+`)`, args...)
	if err != nil {
		return false, err
	}
	c, err := r.RowsAffected()
	return c == 1, err
}

// ParkSellTaskBuyer stops the bot talking to a buyer and says why they
// need you. A buyer whose payment turn is under way keeps it; only the
// note is set.
func (s *Store) ParkSellTaskBuyer(ctx context.Context, id int64, peer, note string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sell_task_buyers SET note=?,
 state=CASE WHEN state IN ('queued','negotiating','agreed') THEN 'parked' ELSE state END
 WHERE task_id=? AND peer=?`, note, id, peer)
	return err
}

// RefundSellTaskBuyer records a payment the bot could not honour, so the
// status tells you to send it back, and parks the buyer.
func (s *Store) RefundSellTaskBuyer(ctx context.Context, id int64, peer, amount, note string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sell_task_buyers SET refund=?,note=?,
 state=CASE WHEN state IN ('queued','negotiating','agreed') THEN 'parked' ELSE state END
 WHERE task_id=? AND peer=?`, amount, note, id, peer)
	return err
}

// RepriceTaskDelivery changes an armed, unpaid reservation to the amount
// the buyer actually paid, so the ledger records it. The amount must be at
// least the task's floor, or a price the bot quoted or agreed with them.
func (s *Store) RepriceTaskDelivery(ctx context.Context, taskID int64, d Delivery, amount string) (Delivery, error) {
	paise, err := ParsePaise(amount)
	if err != nil {
		return d, err
	}
	r, err := s.db.ExecContext(ctx, `UPDATE qr_deliveries SET amount=?
 WHERE task_id=? AND date=? AND meal=? AND peer=? AND amount=? AND image_hash=? AND state='armed' AND payment_id IS NULL
 AND `+activeTask+` AND `+honoured,
		append([]any{amount, taskID, d.Date, d.Meal, d.Peer, d.Amount, d.ImageHash, taskID}, honouredArgs(taskID, d.Peer, paise)...)...)
	if err != nil {
		return d, err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return d, errors.Join(err, fmt.Errorf("could not reprice the reservation to INR %s", amount))
	}
	d.Amount = amount
	return d, nil
}

// ClaimTaskSale records a payment that arrived before the buyer was given
// the reservation: it arms and claims the meal in one step, so the QR can
// go out. It reports false when the meal already has a delivery record,
// for this buyer or anyone else. The amount must be at least the floor, or
// a price the bot quoted or agreed with the buyer.
func (s *Store) ClaimTaskSale(ctx context.Context, taskID int64, d Delivery, p Payment, messageID string) (bool, error) {
	if p.ID == "" || messageID == "" {
		return false, errors.New("missing payment or outgoing message ID")
	}
	paise, err := ParsePaise(d.Amount)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var minPaise int64
	var date, meal, state string
	if err := tx.QueryRowContext(ctx, `SELECT min_paise,date,meal,state FROM sell_tasks WHERE id=?`, taskID).Scan(&minPaise, &date, &meal, &state); err != nil {
		return false, err
	}
	switch {
	case state != TaskActive:
		return false, fmt.Errorf("sell task is %s", state)
	case date != d.Date || meal != d.Meal:
		return false, errors.New("delivery is for a different meal than the task")
	case paise < minPaise:
		if ok, err := priceHonoured(ctx, tx, taskID, d.Peer, paise); err != nil || !ok {
			return false, errors.Join(err, fmt.Errorf("INR %s is below the task's floor of INR %s", d.Amount, FormatPaise(minPaise)))
		}
	}
	var paidAt any
	if !p.At.IsZero() {
		paidAt = p.At.Unix()
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO qr_deliveries(date,meal,peer,amount,image_hash,armed_at,state,payment_id,message_id,paid_at,buyer_name,mess,diet,task_id)
 VALUES(?,?,?,?,?,?,'sending',?,?,?,?,?,?,?) ON CONFLICT(date,meal) DO NOTHING`,
		d.Date, d.Meal, d.Peer, d.Amount, d.ImageHash, d.ArmedAt.Unix(), p.ID, messageID, paidAt, p.BuyerName, d.Mess, d.Diet, taskID)
	if err != nil {
		return false, err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return false, err
	}
	return true, tx.Commit()
}
