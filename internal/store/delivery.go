package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const deliverySchema = `
CREATE TABLE IF NOT EXISTS qr_deliveries (
 date TEXT NOT NULL,
 meal TEXT NOT NULL,
 peer TEXT NOT NULL,
 amount TEXT NOT NULL,
 image_hash TEXT NOT NULL,
 armed_at INTEGER NOT NULL,
 state TEXT NOT NULL DEFAULT 'armed',
 payment_id TEXT UNIQUE,
 message_id TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(date, meal)
);
CREATE TABLE IF NOT EXISTS offer_posts (
 date TEXT NOT NULL,
 meal TEXT NOT NULL,
 chat TEXT NOT NULL,
 message_id TEXT NOT NULL,
 posted_at INTEGER NOT NULL,
 reacted_at INTEGER,
 PRIMARY KEY(chat, message_id)
);
CREATE INDEX IF NOT EXISTS offer_posts_slot ON offer_posts(date, meal);`

// Columns added for the sales page. They only describe a delivery; none of
// them takes part in arming or claiming it.
var deliveryColumns = []string{
	"mess TEXT NOT NULL DEFAULT ''",
	"diet TEXT NOT NULL DEFAULT ''",
	"buyer_name TEXT NOT NULL DEFAULT ''",
	"offer_at INTEGER",
	"paid_at INTEGER",
	"sent_at INTEGER",
}

type Delivery struct {
	Date, Meal, Peer, Amount, ImageHash string
	ArmedAt                             time.Time
	// Mess and Diet are recorded when known, from the portal registration.
	// Resuming doesn't compare them.
	Mess, Diet string
}

func (s *Store) insertDelivery(ctx context.Context, d Delivery) (bool, error) {
	r, err := s.db.ExecContext(ctx, `INSERT INTO qr_deliveries(date,meal,peer,amount,image_hash,armed_at,mess,diet)
 VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(date,meal) DO NOTHING`, d.Date, d.Meal, d.Peer, d.Amount, d.ImageHash, d.ArmedAt.Unix(), d.Mess, d.Diet)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

func (s *Store) DeliveryExists(ctx context.Context, date, meal string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM qr_deliveries WHERE date=? AND meal=?)`, date, meal).Scan(&exists)
	return exists, err
}

// ArmNewDelivery claims a previously unused meal for a buyer found in a group.
// A concurrent command that already claimed the slot wins.
func (s *Store) ArmNewDelivery(ctx context.Context, d Delivery) (time.Time, error) {
	inserted, err := s.insertDelivery(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	if !inserted {
		return time.Time{}, errors.New("meal already has a delivery record")
	}
	return d.ArmedAt.Truncate(time.Second), nil
}

// ReleaseUnpaidDelivery removes only the unclaimed reservation for this buyer.
func (s *Store) ReleaseUnpaidDelivery(ctx context.Context, d Delivery) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM qr_deliveries
 WHERE date=? AND meal=? AND peer=? AND amount=? AND image_hash=? AND armed_at=? AND state='armed' AND payment_id IS NULL`,
		d.Date, d.Meal, d.Peer, d.Amount, d.ImageHash, d.ArmedAt.Unix())
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows != 1 {
		return errors.New("reservation was changed or claimed before it could be released")
	}
	return err
}

// HandOffDelivery records that you took over the sale with d.Peer, so the
// slot is reserved but the bot sends nothing and waits for no payment.
// buyerName is the buyer's WhatsApp push name, if known.
func (s *Store) HandOffDelivery(ctx context.Context, d Delivery, buyerName string) error {
	r, err := s.db.ExecContext(ctx, `INSERT INTO qr_deliveries(date,meal,peer,amount,image_hash,armed_at,state,mess,diet,buyer_name)
 VALUES(?,?,?,?,?,?,'handed_off',?,?,?) ON CONFLICT(date,meal) DO NOTHING`, d.Date, d.Meal, d.Peer, d.Amount, d.ImageHash, d.ArmedAt.Unix(), d.Mess, d.Diet, buyerName)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return errors.Join(err, errors.New("meal already has a delivery record"))
	}
	return nil
}

// ReleaseHandoff frees a handed-off slot so it can be offered again. It
// touches nothing in any other state and reports whether a slot was freed.
func (s *Store) ReleaseHandoff(ctx context.Context, date, meal string) (bool, error) {
	r, err := s.db.ExecContext(ctx, `DELETE FROM qr_deliveries WHERE date=? AND meal=? AND state='handed_off'`, date, meal)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

// An existing slot can only resume with the exact same buyer, price and image,
// except a handed-off one: you own that sale, so arming it takes whatever
// buyer, price and image you give.
func (s *Store) ArmDelivery(ctx context.Context, d Delivery) (time.Time, error) {
	_, err := s.db.ExecContext(ctx, `UPDATE qr_deliveries SET peer=?,amount=?,image_hash=?,armed_at=?,state='armed',offer_at=NULL,
 buyer_name=CASE WHEN peer=? THEN buyer_name ELSE '' END
 WHERE date=? AND meal=? AND state='handed_off'`, d.Peer, d.Amount, d.ImageHash, d.ArmedAt.Unix(), d.Peer, d.Date, d.Meal)
	if err != nil {
		return time.Time{}, err
	}
	_, err = s.insertDelivery(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	if d.Mess != "" {
		if _, err := s.db.ExecContext(ctx, `UPDATE qr_deliveries SET mess=?,diet=? WHERE date=? AND meal=? AND mess=''`, d.Mess, d.Diet, d.Date, d.Meal); err != nil {
			return time.Time{}, err
		}
	}
	var peer, amount, hash, state string
	var armedAt int64
	err = s.db.QueryRowContext(ctx, `SELECT peer,amount,image_hash,state,armed_at FROM qr_deliveries WHERE date=? AND meal=?`, d.Date, d.Meal).Scan(&peer, &amount, &hash, &state, &armedAt)
	if err != nil {
		return time.Time{}, err
	}
	if peer != d.Peer || amount != d.Amount || hash != d.ImageHash {
		return time.Time{}, errors.New("meal already armed with different buyer, amount or image")
	}
	if state != "armed" {
		return time.Time{}, fmt.Errorf("meal delivery is %s; inspect the buyer's chat before any manual follow-up", state)
	}
	return time.Unix(armedAt, 0), nil
}

// MarkOfferSent records when the offer text went to the buyer, so the sales
// page can show how long they took to pay.
func (s *Store) MarkOfferSent(ctx context.Context, d Delivery, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE qr_deliveries SET offer_at=? WHERE date=? AND meal=?`, at.Unix(), d.Date, d.Meal)
	return err
}

// Payment is the message that triggered a delivery.
type Payment struct {
	ID        string
	At        time.Time
	BuyerName string // the buyer's WhatsApp push name; untrusted
}

// Claim before network I/O. Uncertain sends deliberately remain 'sending' and
// are never retried automatically, even after a process restart.
func (s *Store) ClaimDelivery(ctx context.Context, d Delivery, p Payment, messageID string) (bool, error) {
	if p.ID == "" || messageID == "" {
		return false, errors.New("missing payment or outgoing message ID")
	}
	var paidAt any
	if !p.At.IsZero() {
		paidAt = p.At.Unix()
	}
	r, err := s.db.ExecContext(ctx, `UPDATE qr_deliveries SET state='sending',payment_id=?,message_id=?,paid_at=?,buyer_name=?
 WHERE date=? AND meal=? AND peer=? AND amount=? AND image_hash=? AND state='armed'`, p.ID, messageID, paidAt, p.BuyerName, d.Date, d.Meal, d.Peer, d.Amount, d.ImageHash)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

func (s *Store) FinishDelivery(ctx context.Context, d Delivery, messageID string) error {
	r, err := s.db.ExecContext(ctx, `UPDATE qr_deliveries SET state='sent',sent_at=? WHERE date=? AND meal=? AND state='sending' AND message_id=?`, time.Now().Unix(), d.Date, d.Meal, messageID)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("delivery state changed before completion was recorded")
	}
	return nil
}

// DeliveryRecord is one armed meal as the sales page sees it.
type DeliveryRecord struct {
	Date, Meal, Peer, Amount, State string
	Mess, Diet                      string
	// BuyerName is the push name from the payment message, falling back to
	// the one stored for the DM peer. PeerAlt is the peer's other JID (phone
	// or LID) when known. Both are untrusted.
	BuyerName, PeerAlt               string
	ArmedAt, OfferAt, PaidAt, SentAt time.Time // zero when unknown
}

// Deliveries returns every recorded meal, oldest first: armed, handed_off,
// sending and sent.
func (s *Store) Deliveries(ctx context.Context) ([]DeliveryRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.date,d.meal,d.peer,d.amount,d.state,d.mess,d.diet,
 CASE WHEN d.buyer_name != '' THEN d.buyer_name ELSE coalesce((SELECT p.push_name FROM dm_peers p WHERE d.peer IN (p.jid, p.alt) AND p.push_name != '' LIMIT 1), '') END,
 coalesce((SELECT CASE WHEN p.jid = d.peer THEN p.alt ELSE p.jid END FROM dm_peers p WHERE d.peer IN (p.jid, p.alt) LIMIT 1), ''),
 d.armed_at, coalesce(d.offer_at,0), coalesce(d.paid_at,0), coalesce(d.sent_at,0)
 FROM qr_deliveries d ORDER BY d.date, d.meal`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	unix := func(v int64) time.Time {
		if v == 0 {
			return time.Time{}
		}
		return time.Unix(v, 0)
	}
	var out []DeliveryRecord
	for rows.Next() {
		var r DeliveryRecord
		var armed, offer, paid, sent int64
		if err := rows.Scan(&r.Date, &r.Meal, &r.Peer, &r.Amount, &r.State, &r.Mess, &r.Diet, &r.BuyerName, &r.PeerAlt, &armed, &offer, &paid, &sent); err != nil {
			return nil, err
		}
		r.ArmedAt, r.OfferAt, r.PaidAt, r.SentAt = unix(armed), unix(offer), unix(paid), unix(sent)
		out = append(out, r)
	}
	return out, rows.Err()
}

// OfferPost is a group post offering a meal. A meal can have several: one per
// group, and another after a failed sale is offered again. They live apart
// from qr_deliveries because a reservation row is deleted when a buyer
// passes, while the posts still need their ✅ once the meal sells.
type OfferPost struct {
	Date, Meal string
	Chat, ID   string // group JID and message ID
	PostedAt   time.Time
	ReactedAt  time.Time // when messwa last reacted ✅; zero if never
}

func (s *Store) RecordOfferPost(ctx context.Context, p OfferPost) error {
	if p.Chat == "" || p.ID == "" {
		return errors.New("offer post needs a group and a message ID")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO offer_posts(date,meal,chat,message_id,posted_at) VALUES(?,?,?,?,?)
 ON CONFLICT(chat,message_id) DO NOTHING`, p.Date, p.Meal, p.Chat, p.ID, p.PostedAt.Unix())
	return err
}

// OfferPosts returns the meal's offer posts, oldest first.
func (s *Store) OfferPosts(ctx context.Context, date, meal string) ([]OfferPost, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT chat,message_id,posted_at,coalesce(reacted_at,0) FROM offer_posts
 WHERE date=? AND meal=? ORDER BY posted_at, chat, message_id`, date, meal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OfferPost
	for rows.Next() {
		p := OfferPost{Date: date, Meal: meal}
		var posted, reacted int64
		if err := rows.Scan(&p.Chat, &p.ID, &posted, &reacted); err != nil {
			return nil, err
		}
		p.PostedAt = time.Unix(posted, 0)
		if reacted != 0 {
			p.ReactedAt = time.Unix(reacted, 0)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) MarkOfferReacted(ctx context.Context, chat, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE offer_posts SET reacted_at=? WHERE chat=? AND message_id=?`, at.Unix(), chat, id)
	return err
}
