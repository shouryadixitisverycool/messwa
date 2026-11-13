// Package store keeps everything messwa is allowed to know. Anything not
// written here is invisible to the MCP server.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS groups (
	jid  TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS group_admins (
	chat TEXT NOT NULL,
	jid  TEXT NOT NULL,
	PRIMARY KEY (chat, jid)
);
CREATE TABLE IF NOT EXISTS listings (
	id          INTEGER PRIMARY KEY,
	seller      TEXT NOT NULL,
	seller_alt  TEXT NOT NULL DEFAULT '',
	seller_name TEXT NOT NULL DEFAULT '',
	kind        TEXT NOT NULL DEFAULT '',
	meal        TEXT NOT NULL DEFAULT '',
	meal_source TEXT NOT NULL DEFAULT '',
	meal_date   TEXT NOT NULL DEFAULT '',
	meal_end    INTEGER NOT NULL DEFAULT 0,
	mess        TEXT NOT NULL DEFAULT '',
	diet        TEXT NOT NULL DEFAULT '',
	price       INTEGER NOT NULL DEFAULT -1,
	qty         INTEGER NOT NULL DEFAULT 1,
	wants       TEXT NOT NULL DEFAULT '',
	text        TEXT NOT NULL,
	norm        TEXT NOT NULL,
	first_seen  INTEGER NOT NULL,
	last_seen   INTEGER NOT NULL,
	sold_text_at INTEGER
);
CREATE INDEX IF NOT EXISTS listings_seller ON listings(seller, last_seen);
CREATE INDEX IF NOT EXISTS listings_seen ON listings(first_seen);
CREATE TABLE IF NOT EXISTS posts (
	chat       TEXT NOT NULL,
	id         TEXT NOT NULL,
	listing_id INTEGER NOT NULL REFERENCES listings(id) ON DELETE CASCADE,
	sender     TEXT NOT NULL,
	ts         INTEGER NOT NULL,
	text       TEXT NOT NULL,
	edited_at  INTEGER,
	deleted    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (chat, id)
);
CREATE INDEX IF NOT EXISTS posts_listing ON posts(listing_id);
CREATE TABLE IF NOT EXISTS reactions (
	chat        TEXT NOT NULL,
	target_id   TEXT NOT NULL,
	reactor     TEXT NOT NULL,
	reactor_alt TEXT NOT NULL DEFAULT '',
	emoji       TEXT NOT NULL,
	ts          INTEGER NOT NULL,
	PRIMARY KEY (chat, target_id, reactor)
);
CREATE TABLE IF NOT EXISTS dm_peers (
	jid        TEXT PRIMARY KEY,
	alt        TEXT NOT NULL DEFAULT '',
	push_name  TEXT NOT NULL DEFAULT '',
	first_seen INTEGER NOT NULL,
	last_in    INTEGER,
	last_out   INTEGER
);
CREATE TABLE IF NOT EXISTS dm_messages (
	chat    TEXT NOT NULL,
	id      TEXT NOT NULL,
	from_me INTEGER NOT NULL,
	ts      INTEGER NOT NULL,
	text    TEXT NOT NULL,
	PRIMARY KEY (chat, id)
);
CREATE INDEX IF NOT EXISTS dm_messages_ts ON dm_messages(chat, ts);
`

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	os.Chmod(path, 0o600)
	if _, err := db.Exec(schema + logSchema + deliverySchema + priceSchema + accountSchema + factSchema + sellTaskSchema + sellEventSchema + settingsSchema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	// Columns added after the first release. Listings are derived data, so
	// run rebuild afterwards to fill them in for old rows.
	for _, col := range []string{
		"price INTEGER NOT NULL DEFAULT -1",
		"qty INTEGER NOT NULL DEFAULT 1",
		"wants TEXT NOT NULL DEFAULT ''",
	} {
		if _, err := db.Exec("ALTER TABLE listings ADD COLUMN " + col); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	for _, col := range deliveryColumns {
		if _, err := db.Exec("ALTER TABLE qr_deliveries ADD COLUMN " + col); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	for _, col := range append(sellBuyerColumns, sellActionColumns...) {
		if _, err := db.Exec("ALTER TABLE sell_task_buyers ADD COLUMN " + col); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	// Rebuild sell_task_offers for reposts first: the rebuilt table has no
	// error column, so the ALTERs below must run after it.
	if err := migrateSellOffers(db); err != nil {
		return nil, fmt.Errorf("migrate sell_task_offers: %w", err)
	}
	for _, c := range []struct{ table, col string }{
		{"sell_tasks", "target_paise INTEGER NOT NULL DEFAULT 0"},
		{"sell_task_offers", "error TEXT NOT NULL DEFAULT ''"},
	} {
		if _, err := db.Exec("ALTER TABLE " + c.table + " ADD COLUMN " + c.col); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// ---- groups ----

func (s *Store) SetGroup(ctx context.Context, jid, name string, admins []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO groups(jid,name) VALUES(?,?) ON CONFLICT(jid) DO UPDATE SET name=excluded.name`, jid, name); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM group_admins WHERE chat=?`, jid); err != nil {
		return err
	}
	for _, a := range admins {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO group_admins(chat,jid) VALUES(?,?)`, jid, a); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) GroupNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT jid,name FROM groups`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var j, n string
		if err := rows.Scan(&j, &n); err != nil {
			return nil, err
		}
		m[j] = n
	}
	return m, rows.Err()
}

// ---- listings and posts ----

// Listing is a deduplicated offer or request, possibly posted in several groups.
type Listing struct {
	ID         int64
	Seller     string
	SellerAlt  string
	SellerName string
	Kind       string
	Meal       string
	MealSource string
	MealDate   string
	MealEnd    time.Time
	Mess       string
	Diet       string
	Price      int // -1 when the post didn't say
	Qty        int
	Wants      string // comma-separated messes wanted in an exchange
	Text       string
	Norm       string
	FirstSeen  time.Time
	LastSeen   time.Time
	SoldText   bool
	SoldReact  bool
	Closed     bool // the seller reacted with a withdrawn emoji
	OtherTicks int  // people other than the seller/admins who reacted with a sold emoji
	Deleted    bool // every copy was deleted
	Chats      []string
}

type Post struct {
	Chat      string
	ID        string
	ListingID int64
	Sender    string
	TS        time.Time
	Text      string
	Edited    bool
	Deleted   bool
}

// HasPost reports whether a post was already ingested.
func (s *Store) HasPost(ctx context.Context, chat, id string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM posts WHERE chat=? AND id=?`, chat, id).Scan(&n)
	return n > 0, err
}

// RecentBySeller returns the seller's listings seen since t, newest first.
func (s *Store) RecentBySeller(ctx context.Context, ids []string, since time.Time) ([]Listing, error) {
	ids = nonEmpty(ids)
	if len(ids) == 0 {
		return nil, nil
	}
	in := placeholders(len(ids))
	args := append(toAny(ids), toAny(ids)...)
	args = append(args, since.Unix())
	return s.queryListings(ctx, `WHERE (l.seller IN (`+in+`) OR l.seller_alt IN (`+in+`)) AND l.last_seen >= ? ORDER BY l.last_seen DESC`, args...)
}

func (s *Store) CreateListing(ctx context.Context, l *Listing) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO listings(seller,seller_alt,seller_name,kind,meal,meal_source,meal_date,meal_end,mess,diet,price,qty,wants,text,norm,first_seen,last_seen)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		l.Seller, l.SellerAlt, l.SellerName, l.Kind, l.Meal, l.MealSource, l.MealDate, l.MealEnd.Unix(), l.Mess, l.Diet, l.Price, l.Qty, l.Wants, l.Text, l.Norm, l.FirstSeen.Unix(), l.LastSeen.Unix())
	if err != nil {
		return err
	}
	l.ID, err = res.LastInsertId()
	return err
}

// UpdateListing rewrites a listing's derived fields after an edit.
func (s *Store) UpdateListing(ctx context.Context, l *Listing) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listings SET kind=?,meal=?,meal_source=?,meal_date=?,meal_end=?,mess=?,diet=?,price=?,qty=?,wants=?,text=?,norm=? WHERE id=?`,
		l.Kind, l.Meal, l.MealSource, l.MealDate, l.MealEnd.Unix(), l.Mess, l.Diet, l.Price, l.Qty, l.Wants, l.Text, l.Norm, l.ID)
	return err
}

func (s *Store) AddPost(ctx context.Context, p Post) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT OR IGNORE INTO posts(chat,id,listing_id,sender,ts,text) VALUES(?,?,?,?,?,?)`,
		p.Chat, p.ID, p.ListingID, p.Sender, p.TS.Unix(), p.Text)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if _, err := tx.Exec(`UPDATE listings SET last_seen=max(last_seen,?) WHERE id=?`, p.TS.Unix(), p.ListingID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PostListing returns the listing a post belongs to, if the post is known.
func (s *Store) PostListing(ctx context.Context, chat, id string) (*Listing, error) {
	ls, err := s.queryListings(ctx, `WHERE l.id = (SELECT listing_id FROM posts WHERE chat=? AND id=?)`, chat, id)
	if err != nil || len(ls) == 0 {
		return nil, err
	}
	return &ls[0], nil
}

func (s *Store) EditPost(ctx context.Context, chat, id, text string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE posts SET text=?, edited_at=? WHERE chat=? AND id=?`, text, at.Unix(), chat, id)
	return err
}

func (s *Store) DeletePost(ctx context.Context, chat, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE posts SET deleted=1 WHERE chat=? AND id=?`, chat, id)
	return err
}

// SetPrice records the seller's latest stated price, from a reply like "@25".
func (s *Store) SetPrice(ctx context.Context, listingID int64, price int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listings SET price=? WHERE id=?`, price, listingID)
	return err
}

func (s *Store) MarkSoldByText(ctx context.Context, listingID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listings SET sold_text_at=coalesce(sold_text_at, ?) WHERE id=?`, at.Unix(), listingID)
	return err
}

// SetReaction records or (with an empty emoji) removes one person's reaction.
func (s *Store) SetReaction(ctx context.Context, chat, target, reactor, reactorAlt, emoji string, at time.Time) error {
	if emoji == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM reactions WHERE chat=? AND target_id=? AND reactor=?`, chat, target, reactor)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO reactions(chat,target_id,reactor,reactor_alt,emoji,ts) VALUES(?,?,?,?,?,?)
		ON CONFLICT(chat,target_id,reactor) DO UPDATE SET emoji=excluded.emoji, reactor_alt=excluded.reactor_alt, ts=excluded.ts`,
		chat, target, reactor, reactorAlt, NormalizeEmoji(emoji), at.Unix())
	return err
}

// SoldRule says which reactions count as "sold". It is applied at query
// time so removing a ✅ un-sells the listing.
type SoldRule struct {
	Emojis       []string
	ClosedEmojis []string
	AdminsCount  bool
}

// NormalizeEmoji drops variation selectors and skin tones so "👍🏽" == "👍"
// and "☑️" == "☑".
func NormalizeEmoji(e string) string {
	return strings.Map(func(r rune) rune {
		if r == 0xFE0F || r == 0xFE0E || r >= 0x1F3FB && r <= 0x1F3FF {
			return -1
		}
		return r
	}, e)
}

func normalizeAll(es []string) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, NormalizeEmoji(e))
	}
	return out
}

type ListingFilter struct {
	Since     time.Time
	EndsAfter time.Time // if set, only listings whose meal ends after this
	Kind      string
	Meal      string
	Mess      string
	Limit     int
}

func (s *Store) Listings(ctx context.Context, rule SoldRule, f ListingFilter) ([]Listing, error) {
	where := []string{"l.first_seen >= ?"}
	args := []any{f.Since.Unix()}
	if !f.EndsAfter.IsZero() {
		where = append(where, "l.meal_end > ?")
		args = append(args, f.EndsAfter.Unix())
	}
	for col, v := range map[string]string{"kind": f.Kind, "meal": f.Meal, "mess": f.Mess} {
		if v != "" {
			where = append(where, "l."+col+" = ?")
			args = append(args, v)
		}
	}
	q := "WHERE " + strings.Join(where, " AND ") + " ORDER BY l.first_seen DESC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	ls, err := s.queryListings(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return ls, s.fillSold(ctx, rule, ls)
}

func (s *Store) Listing(ctx context.Context, rule SoldRule, id int64) (*Listing, []Post, error) {
	ls, err := s.queryListings(ctx, `WHERE l.id = ?`, id)
	if err != nil || len(ls) == 0 {
		return nil, nil, err
	}
	if err := s.fillSold(ctx, rule, ls); err != nil {
		return nil, nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT chat,id,sender,ts,text,edited_at IS NOT NULL,deleted FROM posts WHERE listing_id=? ORDER BY ts`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var posts []Post
	for rows.Next() {
		var p Post
		var ts int64
		if err := rows.Scan(&p.Chat, &p.ID, &p.Sender, &ts, &p.Text, &p.Edited, &p.Deleted); err != nil {
			return nil, nil, err
		}
		p.ListingID, p.TS = id, time.Unix(ts, 0)
		posts = append(posts, p)
	}
	return &ls[0], posts, rows.Err()
}

func (s *Store) queryListings(ctx context.Context, tail string, args ...any) ([]Listing, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT l.id,l.seller,l.seller_alt,l.seller_name,l.kind,l.meal,l.meal_source,l.meal_date,l.meal_end,l.mess,l.diet,l.price,l.qty,l.wants,l.text,l.norm,l.first_seen,l.last_seen,
		l.sold_text_at IS NOT NULL,
		NOT EXISTS (SELECT 1 FROM posts p WHERE p.listing_id=l.id AND p.deleted=0),
		coalesce((SELECT group_concat(DISTINCT p.chat) FROM posts p WHERE p.listing_id=l.id), '')
		FROM listings l `+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Listing
	for rows.Next() {
		var l Listing
		var end, first, last int64
		var chats string
		if err := rows.Scan(&l.ID, &l.Seller, &l.SellerAlt, &l.SellerName, &l.Kind, &l.Meal, &l.MealSource, &l.MealDate, &end, &l.Mess, &l.Diet, &l.Price, &l.Qty, &l.Wants, &l.Text, &l.Norm, &first, &last, &l.SoldText, &l.Deleted, &chats); err != nil {
			return nil, err
		}
		l.MealEnd, l.FirstSeen, l.LastSeen = time.Unix(end, 0), time.Unix(first, 0), time.Unix(last, 0)
		if chats != "" {
			l.Chats = strings.Split(chats, ",")
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// fillSold works out reaction-based status for each listing, across all its
// copies: SoldReact (a sold emoji from the seller or, if allowed, an admin of
// that group), Closed (a withdrawn emoji from the seller), and OtherTicks.
func (s *Store) fillSold(ctx context.Context, rule SoldRule, ls []Listing) error {
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
	inSold := "emoji IN (" + placeholders(len(sold)) + ")"
	q := `WITH x AS (
		SELECT r.reactor, r.emoji AS emoji,
			(r.reactor IN (?,?) OR (r.reactor_alt != '' AND r.reactor_alt IN (?,?))) AS by_seller,
			` + admin + ` AS by_admin
		FROM posts p JOIN reactions r ON r.chat=p.chat AND r.target_id=p.id
		WHERE p.listing_id=?)
	SELECT
		coalesce(max(` + inSold + ` AND (by_seller OR by_admin)), 0),
		coalesce(max(emoji IN (` + placeholders(len(closed)) + `) AND by_seller), 0),
		count(DISTINCT CASE WHEN ` + inSold + ` AND NOT by_seller AND NOT by_admin THEN reactor END)
	FROM x`
	for i := range ls {
		l := &ls[i]
		args := []any{l.Seller, orNone(l.SellerAlt), l.Seller, orNone(l.SellerAlt), l.ID}
		args = append(args, toAny(sold)...)
		args = append(args, toAny(closed)...)
		args = append(args, toAny(sold)...)
		if err := s.db.QueryRowContext(ctx, q, args...).Scan(&l.SoldReact, &l.Closed, &l.OtherTicks); err != nil {
			return err
		}
	}
	return nil
}

// IsSold reports whether a listing counts as sold right now.
func (s *Store) IsSold(ctx context.Context, rule SoldRule, l *Listing) (bool, error) {
	one := []Listing{*l}
	if err := s.fillSold(ctx, rule, one); err != nil {
		return false, err
	}
	return one[0].SoldText || one[0].SoldReact || one[0].Closed, nil
}

// ---- DMs ----

type Peer struct {
	JID       string
	Alt       string
	PushName  string
	FirstSeen time.Time
	LastIn    time.Time // zero if they never wrote
	LastOut   time.Time // zero if you never wrote
}

// TouchPeer records DM activity. Content is stored separately and only for
// unsaved numbers, so this is all that exists for saved contacts.
func (s *Store) TouchPeer(ctx context.Context, jid, alt, pushName string, fromMe bool, at time.Time) error {
	col := "last_in"
	if fromMe {
		col = "last_out"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO dm_peers(jid,alt,push_name,first_seen,`+col+`) VALUES(?,?,?,?,?)
		ON CONFLICT(jid) DO UPDATE SET
			alt = CASE WHEN excluded.alt != '' THEN excluded.alt ELSE alt END,
			push_name = CASE WHEN excluded.push_name != '' THEN excluded.push_name ELSE push_name END,
			first_seen = min(first_seen, excluded.first_seen),
			`+col+` = max(coalesce(`+col+`, 0), excluded.`+col+`)`,
		jid, alt, pushName, at.Unix(), at.Unix())
	return err
}

func (s *Store) AddDM(ctx context.Context, chat, id string, fromMe bool, at time.Time, text string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO dm_messages(chat,id,from_me,ts,text) VALUES(?,?,?,?,?)`, chat, id, fromMe, at.Unix(), text)
	return err
}

func (s *Store) Peers(ctx context.Context, since time.Time) ([]Peer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT jid,alt,push_name,first_seen,coalesce(last_in,0),coalesce(last_out,0) FROM dm_peers
		WHERE max(coalesce(last_in,0), coalesce(last_out,0)) >= ? ORDER BY max(coalesce(last_in,0), coalesce(last_out,0)) DESC`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		var p Peer
		var first, in, outT int64
		if err := rows.Scan(&p.JID, &p.Alt, &p.PushName, &first, &in, &outT); err != nil {
			return nil, err
		}
		p.FirstSeen = time.Unix(first, 0)
		if in > 0 {
			p.LastIn = time.Unix(in, 0)
		}
		if outT > 0 {
			p.LastOut = time.Unix(outT, 0)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LastSentTo is when you last sent a DM to whichever peer one of jids
// names, as its JID or alternate ID. It is zero if you never did.
func (s *Store) LastSentTo(ctx context.Context, jids ...string) (time.Time, error) {
	if len(jids) == 0 {
		return time.Time{}, nil
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(jids)), ",")
	args := make([]any, 0, 2*len(jids))
	for range 2 {
		for _, j := range jids {
			args = append(args, j)
		}
	}
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(last_out),0) FROM dm_peers WHERE jid IN (`+in+`) OR alt IN (`+in+`)`, args...).Scan(&at)
	if err != nil || at == 0 {
		return time.Time{}, err
	}
	return time.Unix(at, 0), nil
}

func (s *Store) Peer(ctx context.Context, jid string) (*Peer, error) {
	ps, err := s.Peers(ctx, time.Unix(0, 0))
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.JID == jid || p.Alt == jid {
			return &p, nil
		}
	}
	return nil, nil
}

type DM struct {
	ID     string
	FromMe bool
	TS     time.Time
	Text   string
}

func (s *Store) DMs(ctx context.Context, chat string, since time.Time, limit int) ([]DM, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,from_me,ts,text FROM (
		SELECT * FROM dm_messages WHERE chat=? AND ts>=? ORDER BY ts DESC LIMIT ?) ORDER BY ts`, chat, since.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DM
	for rows.Next() {
		var m DM
		var ts int64
		if err := rows.Scan(&m.ID, &m.FromMe, &ts, &m.Text); err != nil {
			return nil, err
		}
		m.TS = time.Unix(ts, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

// DropDMContent removes stored content for a chat, used when a number
// turns out to be (or becomes) a saved contact.
func (s *Store) DropDMContent(ctx context.Context, chat string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM dm_messages WHERE chat=?`, chat)
	return err
}

// Prune deletes DM content older than dmBefore and listings older than listingsBefore.
func (s *Store) Prune(ctx context.Context, dmBefore, listingsBefore time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM dm_messages WHERE ts < ?`, dmBefore.Unix()); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM reactions WHERE ts < ?`, listingsBefore.Unix()); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM listings WHERE last_seen < ?`, listingsBefore.Unix()); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix()); err != nil {
		return err
	}
	return s.pruneLog(ctx, listingsBefore)
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func nonEmpty(ss []string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// orNone keeps an empty alt JID from matching empty reactor fields.
func orNone(s string) string {
	if s == "" {
		return "\x00none"
	}
	return s
}
