package store

import (
	"context"
	"time"
)

// A sell task's event log is the story of the sale for the dashboard: what
// the bot sent, how it read each buyer message, and how the payment went.
// It never holds what a buyer wrote. A buyer message is stored only as the
// meaning the bot read into it (an offer of ₹15, a question, a picture), so
// saved contacts' words stay off disk as before. Text is set only for the
// bot's own messages and for notes messwa wrote itself.
const sellEventSchema = `
CREATE TABLE IF NOT EXISTS sell_task_events (
 task_id INTEGER NOT NULL,
 at INTEGER NOT NULL,
 peer TEXT NOT NULL DEFAULT '',
 kind TEXT NOT NULL,
 n INTEGER NOT NULL DEFAULT 0,
 text TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS sell_task_events_task ON sell_task_events(task_id, at);
CREATE INDEX IF NOT EXISTS sell_task_events_at ON sell_task_events(at);`

// Event kinds. Peer is the buyer, or the group for posts.
const (
	EventPosted        = "posted"         // a copy of the offer went up
	EventPostFailed    = "post_failed"    // Text: the send error
	EventJoined        = "joined"         // a buyer replied to a post
	EventRead          = "read"           // Text: the intent the bot read; N: their offer in paise
	EventImage         = "image"          // the buyer sent a picture, not a payment
	EventBotSaid       = "bot_said"       // Text: the template sent; N: the price in it, paise
	EventAgreed        = "agreed"         // N: paise
	EventPrompt        = "prompt"         // asked to pay; N: paise
	EventParked        = "parked"         // Text: why they need you
	EventPassed        = "passed"         // the buyer dropped out
	EventPassCounter   = "pass_counter"   // the buyer passed and got one lower price; N: paise, Text: what they passed at
	EventTimedOut      = "timed_out"      // no payment in their turn, or went quiet
	EventRevived       = "revived"        // a timed-out buyer wrote again and is negotiating; N: the bot's ask, paise
	EventPayment       = "payment"        // a matching WhatsApp Pay payment; N: paise
	EventPayUnreadable = "pay_unreadable" // a payment whose amount the bot couldn't read
	EventRecovery      = "recovery"       // asked the phone for the full payment; N: attempt
	EventPayMismatch   = "pay_mismatch"   // a payment that matches no price
	EventQRSent        = "qr_sent"
	EventToldSold      = "told_sold"
	EventTicked        = "ticked" // N: posts ticked ✅
	EventEnded         = "ended"  // Text: the task's final state
	// EventPrices is a range edit on a running task. N: the new floor,
	// paise; Text: "min-max was min-max", INR.
	EventPrices = "prices"
)

type SellEvent struct {
	TaskID int64
	At     time.Time
	Peer   string
	Kind   string
	N      int64
	Text   string
}

// AddSellTaskEvent appends one event. It only describes the sale; nothing
// reads it to decide what to send.
func (s *Store) AddSellTaskEvent(ctx context.Context, e SellEvent) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sell_task_events(task_id,at,peer,kind,n,text) VALUES(?,?,?,?,?,?)`,
		e.TaskID, e.At.UnixNano(), e.Peer, e.Kind, e.N, e.Text)
	return err
}

// SellTaskEvents returns a task's events, oldest first.
func (s *Store) SellTaskEvents(ctx context.Context, taskID int64) ([]SellEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at,peer,kind,n,text FROM sell_task_events WHERE task_id=? ORDER BY at, rowid`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SellEvent
	for rows.Next() {
		e := SellEvent{TaskID: taskID}
		var at int64
		if err := rows.Scan(&at, &e.Peer, &e.Kind, &e.N, &e.Text); err != nil {
			return nil, err
		}
		e.At = time.Unix(0, at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SellEventsBetween returns events at or after from and before to, oldest
// first, for one task or, with taskID 0, every task. A zero from or to
// leaves that end open. It returns at most limit events, the earliest
// ones, and whether more matched.
func (s *Store) SellEventsBetween(ctx context.Context, taskID int64, from, to time.Time, limit int) ([]SellEvent, bool, error) {
	q := `SELECT task_id,at,peer,kind,n,text FROM sell_task_events WHERE 1=1`
	var args []any
	if taskID != 0 {
		q += ` AND task_id=?`
		args = append(args, taskID)
	}
	if !from.IsZero() {
		q += ` AND at>=?`
		args = append(args, from.UnixNano())
	}
	if !to.IsZero() {
		q += ` AND at<?`
		args = append(args, to.UnixNano())
	}
	q += ` ORDER BY at, rowid LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []SellEvent
	for rows.Next() {
		var e SellEvent
		var at int64
		if err := rows.Scan(&e.TaskID, &at, &e.Peer, &e.Kind, &e.N, &e.Text); err != nil {
			return nil, false, err
		}
		e.At = time.Unix(0, at)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

// SellTasksForMeal returns every task for one meal, oldest first, so a
// restarted sale keeps its story.
func (s *Store) SellTasksForMeal(ctx context.Context, date, meal string) ([]SellTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sellTaskCols+` FROM sell_tasks WHERE date=? AND meal=? ORDER BY id`, date, meal)
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

// SetSellTaskTarget records the price the negotiation aims for.
func (s *Store) SetSellTaskTarget(ctx context.Context, id, paise int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sell_tasks SET target_paise=? WHERE id=?`, paise, id)
	return err
}

// SetOfferFailed resolves a claimed post as failed and keeps the send error.
func (s *Store) SetOfferFailed(ctx context.Context, id int64, offerID, why string) error {
	if len(why) > 300 {
		why = why[:300]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE sell_task_offers SET state='failed',error=? WHERE task_id=? AND offer_id=? AND state='posting'`, why, id, offerID)
	return err
}

// PostsAfter counts listing posts in a group after at, besides the post
// itself: how far down the chat your offer has been pushed.
func (s *Store) PostsAfter(ctx context.Context, chat, id string, at time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM posts WHERE chat=? AND id!=? AND ts>? AND deleted=0`, chat, id, at.Unix()).Scan(&n)
	return n, err
}

// TaskDeliveryTimes are when the task's sale was paid and the QR sent; zero
// when not (yet).
func (s *Store) TaskDeliveryTimes(ctx context.Context, taskID int64) (paid, sent time.Time, err error) {
	var p, q int64
	err = s.db.QueryRowContext(ctx, `SELECT coalesce(max(paid_at),0),coalesce(max(sent_at),0) FROM qr_deliveries WHERE task_id=?`, taskID).Scan(&p, &q)
	if p != 0 {
		paid = time.Unix(p, 0)
	}
	if q != 0 {
		sent = time.Unix(q, 0)
	}
	return paid, sent, err
}

// PeerName is the WhatsApp push name stored for a DM peer, or "". It is
// untrusted.
func (s *Store) PeerName(ctx context.Context, jids ...string) string {
	for _, j := range jids {
		if j == "" {
			continue
		}
		var name string
		if err := s.db.QueryRowContext(ctx, `SELECT push_name FROM dm_peers WHERE ? IN (jid, alt) AND push_name!='' LIMIT 1`, j).Scan(&name); err == nil {
			return name
		}
	}
	return ""
}
