package store

import (
	"context"
	"time"
)

// MarketPost is one message in a market group that became part of a
// listing. Each copy in each group counts, and so does every repost. Replies
// under a listing (bumps, price changes, "sold") don't.
type MarketPost struct {
	TS        time.Time
	ListingID int64
	Mess      string
	Diet      string // veg, nonveg, egg, or empty when the post doesn't say
	Kind      string
	Price     int // -1 when the listing has no price
}

// MarketSale is a sell or exchange listing that sold.
type MarketSale struct {
	TS        time.Time // when the seller marked it sold
	FirstSeen time.Time
	ListingID int64
	Mess      string
	Diet      string
	Price     int
}

// Market returns the posts made in [since, until] and the listings marked
// sold in that range, for listings that name a mess. A listing's sale time is
// the earliest of the seller's sold reaction (or an admin's, when the rule
// allows it) and their "sold" reply or edit. Buy orders that went through
// aren't sales.
func (s *Store) Market(ctx context.Context, rule SoldRule, since, until time.Time) ([]MarketPost, []MarketSale, error) {
	rows, err := s.boardDB().QueryContext(ctx, `SELECT p.ts, l.id, l.mess, l.diet, l.kind, l.price FROM posts p JOIN listings l ON l.id=p.listing_id
		WHERE p.ts >= ? AND p.ts <= ? AND l.mess != ''
		AND NOT EXISTS (SELECT 1 FROM group_log g WHERE g.chat=p.chat AND g.id=p.id AND g.type='post' AND g.reply_to != '')
		ORDER BY p.ts`, since.Unix(), until.Unix())
	if err != nil {
		return nil, nil, err
	}
	var posts []MarketPost
	for rows.Next() {
		var p MarketPost
		var ts int64
		if err := rows.Scan(&ts, &p.ListingID, &p.Mess, &p.Diet, &p.Kind, &p.Price); err != nil {
			rows.Close()
			return nil, nil, err
		}
		p.TS = time.Unix(ts, 0)
		posts = append(posts, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	sold := normalizeAll(rule.Emojis)
	if len(sold) == 0 {
		sold = []string{"\x00"}
	}
	admin := "0"
	if rule.AdminsCount {
		admin = `EXISTS (SELECT 1 FROM group_admins a WHERE a.chat=r.chat AND a.jid IN (r.reactor, r.reactor_alt))`
	}
	// A listing sold in range was posted at most a day before, since the
	// dedupe window and meal windows keep listings shorter-lived than that.
	q := `SELECT id, mess, diet, price, first_seen, at FROM (
		SELECT l.id, l.mess, l.diet, l.price, l.first_seen, min(
			coalesce(l.sold_text_at, 9e18),
			coalesce((SELECT min(r.ts) FROM posts p JOIN reactions r ON r.chat=p.chat AND r.target_id=p.id
				WHERE p.listing_id=l.id AND r.emoji IN (` + placeholders(len(sold)) + `) AND (
					r.reactor = l.seller OR (l.seller_alt != '' AND r.reactor = l.seller_alt)
					OR (r.reactor_alt != '' AND r.reactor_alt IN (l.seller, l.seller_alt))
					OR ` + admin + `)), 9e18)) AS at
		FROM listings l
		WHERE l.kind != 'buy' AND l.mess != '' AND l.last_seen >= ? AND l.first_seen <= ?)
	WHERE at >= ? AND at <= ? ORDER BY at`
	args := append(toAny(sold), since.Add(-24*time.Hour).Unix(), until.Unix(), since.Unix(), until.Unix())
	rows, err = s.boardDB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var sales []MarketSale
	for rows.Next() {
		var m MarketSale
		var first, at int64
		if err := rows.Scan(&m.ListingID, &m.Mess, &m.Diet, &m.Price, &first, &at); err != nil {
			return nil, nil, err
		}
		m.FirstSeen, m.TS = time.Unix(first, 0), time.Unix(at, 0)
		sales = append(sales, m)
	}
	return posts, sales, rows.Err()
}
