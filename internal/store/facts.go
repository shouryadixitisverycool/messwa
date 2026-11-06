package store

import (
	"context"
	"time"
)

// Listings, posts and reactions are pruned after two weeks. listing_facts
// keeps one row per listing for good: what was offered, when, at what
// price, and whether it sold. No message text. The insights page reads it.
//
// Key is the chat and message ID of the listing's first post, which stays
// the same when rebuild re-derives listings under new IDs.
const factSchema = `
CREATE TABLE IF NOT EXISTS listing_facts (
	key         TEXT PRIMARY KEY,
	seller      TEXT NOT NULL,
	seller_name TEXT NOT NULL DEFAULT '',
	kind        TEXT NOT NULL,
	meal        TEXT NOT NULL,
	meal_date   TEXT NOT NULL,
	mess        TEXT NOT NULL,
	diet        TEXT NOT NULL,
	price       INTEGER NOT NULL,
	qty         INTEGER NOT NULL,
	wants       TEXT NOT NULL DEFAULT '',
	first_seen  INTEGER NOT NULL,
	last_seen   INTEGER NOT NULL,
	meal_end    INTEGER NOT NULL,
	sold_at     INTEGER,
	closed      INTEGER NOT NULL DEFAULT 0,
	deleted     INTEGER NOT NULL DEFAULT 0,
	posts       INTEGER NOT NULL,
	groups      INTEGER NOT NULL,
	other_ticks INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS listing_facts_date ON listing_facts(meal_date);`

// FactsFreeze is how long after a meal ends its facts keep updating. A late
// tick or un-tick still counts within it. After it, the row is final, so
// pruning the reactions behind it can't un-sell a listing.
const FactsFreeze = 7 * 24 * time.Hour

// Fact is one listing after its meal ended.
type Fact struct {
	Seller, SellerName       string
	Kind, Meal, Date         string
	Mess, Diet, Wants        string
	Price, Qty               int
	FirstSeen, LastSeen, End time.Time
	SoldAt                   time.Time // zero if it never sold
	Closed, Deleted          bool
	Posts, Groups            int
	OtherTicks               int
}

// SnapshotFacts writes a fact for every listing whose meal has ended by now,
// and refreshes the ones that ended within FactsFreeze. Sold works as in
// Market: the earliest of the seller's tick (or an admin's, when the rule
// allows it) and their "sold" reply or edit. Closed and other_ticks follow
// fillSold. Buy orders get facts too; a ticked buy order went through.
func (s *Store) SnapshotFacts(ctx context.Context, rule SoldRule, now time.Time) error {
	sold, closed := normalizeAll(rule.Emojis), normalizeAll(rule.ClosedEmojis)
	if len(sold) == 0 {
		sold = []string{"\x00"}
	}
	if len(closed) == 0 {
		closed = []string{"\x00"}
	}
	admin := "0"
	if rule.AdminsCount {
		admin = `EXISTS (SELECT 1 FROM group_admins a WHERE a.chat=r.chat AND a.jid IN (r.reactor, r.reactor_alt))`
	}
	inSold := "x.emoji IN (" + placeholders(len(sold)) + ")"
	q := `WITH ended AS (
		SELECT * FROM listings WHERE meal_end > 0 AND meal_end <= ?
	), x AS (
		SELECT l.id, r.reactor, r.emoji, r.ts,
			(r.reactor = l.seller OR (l.seller_alt != '' AND r.reactor = l.seller_alt)
				OR (r.reactor_alt != '' AND r.reactor_alt IN (l.seller, l.seller_alt))) AS by_seller,
			` + admin + ` AS by_admin
		FROM ended l JOIN posts p ON p.listing_id=l.id JOIN reactions r ON r.chat=p.chat AND r.target_id=p.id
	), rx AS (
		SELECT x.id,
			min(CASE WHEN ` + inSold + ` AND (x.by_seller OR x.by_admin) THEN x.ts END) AS sold_at,
			max(x.emoji IN (` + placeholders(len(closed)) + `) AND x.by_seller) AS closed,
			count(DISTINCT CASE WHEN ` + inSold + ` AND NOT x.by_seller AND NOT x.by_admin THEN x.reactor END) AS others
		FROM x GROUP BY x.id
	), px AS (
		-- SQLite fills chat and id from the row that has min(ts): the first post.
		SELECT p.listing_id AS id, p.chat || '/' || p.id AS key, min(p.ts),
			count(*) AS posts, count(DISTINCT p.chat) AS groups, min(p.deleted) AS deleted
		FROM ended l JOIN posts p ON p.listing_id=l.id GROUP BY p.listing_id
	)
	INSERT INTO listing_facts(key,seller,seller_name,kind,meal,meal_date,mess,diet,price,qty,wants,
		first_seen,last_seen,meal_end,sold_at,closed,deleted,posts,groups,other_ticks)
	SELECT px.key, l.seller, l.seller_name, l.kind, l.meal, l.meal_date, l.mess, l.diet, l.price, l.qty, l.wants,
		l.first_seen, l.last_seen, l.meal_end,
		nullif(min(coalesce(l.sold_text_at, 9e18), coalesce(rx.sold_at, 9e18)), 9e18),
		coalesce(rx.closed, 0), px.deleted, px.posts, px.groups, coalesce(rx.others, 0)
	FROM ended l JOIN px ON px.id=l.id LEFT JOIN rx ON rx.id=l.id
	WHERE true
	ON CONFLICT(key) DO UPDATE SET
		seller=excluded.seller, seller_name=excluded.seller_name, kind=excluded.kind, meal=excluded.meal,
		meal_date=excluded.meal_date, mess=excluded.mess, diet=excluded.diet, price=excluded.price,
		qty=excluded.qty, wants=excluded.wants, first_seen=excluded.first_seen, last_seen=excluded.last_seen,
		meal_end=excluded.meal_end, sold_at=excluded.sold_at, closed=excluded.closed, deleted=excluded.deleted,
		posts=excluded.posts, groups=excluded.groups, other_ticks=excluded.other_ticks
	WHERE listing_facts.meal_end >= ?`
	args := []any{now.Unix()}
	args = append(args, toAny(sold)...)
	args = append(args, toAny(closed)...)
	args = append(args, toAny(sold)...)
	args = append(args, now.Add(-FactsFreeze).Unix())
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

// Facts returns every fact for meals dated from..to, inclusive.
func (s *Store) Facts(ctx context.Context, from, to string) ([]Fact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seller,seller_name,kind,meal,meal_date,mess,diet,wants,price,qty,
		first_seen,last_seen,meal_end,coalesce(sold_at,0),closed,deleted,posts,groups,other_ticks
		FROM listing_facts WHERE meal_date >= ? AND meal_date <= ? ORDER BY meal_end, first_seen`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		var f Fact
		var first, last, end, sold int64
		if err := rows.Scan(&f.Seller, &f.SellerName, &f.Kind, &f.Meal, &f.Date, &f.Mess, &f.Diet, &f.Wants, &f.Price, &f.Qty,
			&first, &last, &end, &sold, &f.Closed, &f.Deleted, &f.Posts, &f.Groups, &f.OtherTicks); err != nil {
			return nil, err
		}
		f.FirstSeen, f.LastSeen, f.End = time.Unix(first, 0), time.Unix(last, 0), time.Unix(end, 0)
		if sold > 0 {
			f.SoldAt = time.Unix(sold, 0)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FirstFactDate is the earliest meal date with a fact, or "" when there are none.
func (s *Store) FirstFactDate(ctx context.Context) (string, error) {
	var d string
	err := s.db.QueryRowContext(ctx, `SELECT coalesce(min(meal_date), '') FROM listing_facts`).Scan(&d)
	return d, err
}

// FactTotal sums one meal date's facts for one mess and diet. Offers are
// sell and exchange listings that name a mess and weren't deleted; Wanted
// are buy orders that weren't deleted. AskSum and Asks cover the offers
// that are sales with a price.
type FactTotal struct {
	Date, Mess, Diet     string
	Offers, Sold, Wanted int
	AskSum, Asks         int
}

// FactTotals returns the totals for every meal date up to to, oldest first.
func (s *Store) FactTotals(ctx context.Context, to string) ([]FactTotal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT meal_date, mess, diet,
		sum(kind != 'buy' AND mess != '' AND deleted = 0),
		sum(kind != 'buy' AND mess != '' AND deleted = 0 AND sold_at IS NOT NULL),
		sum(kind = 'buy' AND deleted = 0),
		sum(CASE WHEN kind = 'sell' AND mess != '' AND deleted = 0 AND price > 0 THEN price ELSE 0 END),
		sum(kind = 'sell' AND mess != '' AND deleted = 0 AND price > 0)
		FROM listing_facts WHERE meal_date <= ? GROUP BY meal_date, mess, diet ORDER BY meal_date`, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FactTotal
	for rows.Next() {
		var t FactTotal
		if err := rows.Scan(&t.Date, &t.Mess, &t.Diet, &t.Offers, &t.Sold, &t.Wanted, &t.AskSum, &t.Asks); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
