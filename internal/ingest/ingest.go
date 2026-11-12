// Package ingest turns chat events into listings and DM records. It decides
// what gets stored; anything it drops never reaches the MCP server.
package ingest

import (
	"context"
	"strings"
	"time"

	"messwa/internal/classify"
	"messwa/internal/config"
	"messwa/internal/meals"
	"messwa/internal/store"
)

type Ingester struct {
	cfg    *config.Config
	st     *store.Store
	sched  *meals.Schedule
	meals  []classify.Named
	messes []classify.Named
	groups map[string]bool
	change func()
}

// OnChange sets a function to call after each market-group event changes
// the listings. It runs on the event handler's goroutine, so keep it quick.
func (in *Ingester) OnChange(f func()) { in.change = f }

func New(cfg *config.Config, st *store.Store) *Ingester {
	in := &Ingester{cfg: cfg, st: st, sched: meals.New(cfg), groups: map[string]bool{}}
	for _, m := range cfg.Meals {
		in.meals = append(in.meals, classify.Named{Name: m.Name, Aliases: m.Keywords, Fuzzy: true})
	}
	for _, m := range cfg.Messes {
		in.messes = append(in.messes, classify.Named{Name: m.Name, Aliases: m.Aliases, Fuzzy: true})
	}
	for _, g := range cfg.Groups {
		in.groups[g] = true
	}
	return in
}

func (in *Ingester) IsMarketGroup(jid string) bool { return in.groups[jid] }

// GroupPost is a new message in one of the market groups.
type GroupPost struct {
	Chat      string
	ID        string
	Sender    string // primary JID (phone number when known)
	SenderAlt string // the other identity (LID or phone number), if known
	PushName  string
	TS        time.Time
	Text      string
	ReplyTo   string // ID of the quoted message, if any
}

func (in *Ingester) Post(ctx context.Context, p GroupPost) error {
	if !in.groups[p.Chat] || strings.TrimSpace(p.Text) == "" {
		return nil
	}
	if seen, err := in.st.HasPost(ctx, p.Chat, p.ID); err != nil || seen {
		return err
	}
	l := in.classify(p.Text, p.TS)
	if p.ReplyTo != "" {
		handled, err := in.ownReply(ctx, p, &l)
		if err != nil || handled {
			return err
		}
	}
	if !isListing(&l) {
		return nil // chatter, or someone else's reply
	}
	l.Seller, l.SellerAlt, l.SellerName = p.Sender, p.SenderAlt, p.PushName
	l.FirstSeen, l.LastSeen = p.TS, p.TS

	match, err := in.findDuplicate(ctx, &l, p)
	if err != nil {
		return err
	}
	if match == nil {
		if err := in.st.CreateListing(ctx, &l); err != nil {
			return err
		}
		match = &l
	} else if enrich(match, &l) {
		if err := in.st.UpdateListing(ctx, match); err != nil {
			return err
		}
	}
	return in.st.AddPost(ctx, store.Post{Chat: p.Chat, ID: p.ID, ListingID: match.ID, Sender: p.Sender, TS: p.TS, Text: p.Text})
}

func isListing(l *store.Listing) bool {
	return l.Kind != "" || l.Mess != "" || l.MealSource == "keyword"
}

// ownReply handles a seller replying to their own listing. In these groups
// that's almost always an update: a new price ("@25"), a bump ("."), "sold",
// or "free". Those attach to the listing. A reply that is a full listing on
// its own ("Or sell kadamb veg") is left for the normal path.
func (in *Ingester) ownReply(ctx context.Context, p GroupPost, l *store.Listing) (bool, error) {
	parent, err := in.st.PostListing(ctx, p.Chat, p.ReplyTo)
	if err != nil || parent == nil || !sameUser(parent, p.Sender, p.SenderAlt) {
		return false, err
	}
	t := classify.Prepare(p.Text)
	sold := t.Words() <= 6 && t.Has(in.cfg.Live().Sold.Words...)
	if !sold && l.Mess != "" && l.Kind != "" {
		return false, nil
	}
	if sold {
		if err := in.st.MarkSoldByText(ctx, parent.ID, p.TS); err != nil {
			return false, err
		}
	} else if l.Price >= 0 {
		if err := in.st.SetPrice(ctx, parent.ID, l.Price); err != nil {
			return false, err
		}
	}
	return true, in.st.AddPost(ctx, store.Post{Chat: p.Chat, ID: p.ID, ListingID: parent.ID, Sender: p.Sender, TS: p.TS, Text: p.Text})
}

// findDuplicate treats posts as one listing when the same person offers the
// same kind of deal for the same meal slot within the dedupe window. People
// hold one registration per meal, so wording doesn't matter, but a different
// mess or diet (kadamba veg and nonveg are separate registrations) means a
// different listing. A sold listing never absorbs a new post: a repost after
// a buyer backs out should show as available.
func (in *Ingester) findDuplicate(ctx context.Context, l *store.Listing, p GroupPost) (*store.Listing, error) {
	live := in.cfg.Live()
	recent, err := in.st.RecentBySeller(ctx, []string{p.Sender, p.SenderAlt}, p.TS.Add(-live.Dedupe.Window.Duration))
	if err != nil {
		return nil, err
	}
	rule := store.SoldRule{Emojis: live.Sold.Emojis, ClosedEmojis: live.Sold.ClosedEmojis, AdminsCount: live.Sold.AdminsCount}
	for i := range recent {
		r := &recent[i]
		if r.Meal != l.Meal || r.MealDate != l.MealDate || r.Kind != l.Kind {
			continue
		}
		if conflict(r.Mess, l.Mess) || conflict(r.Diet, l.Diet) {
			continue
		}
		sold, err := in.st.IsSold(ctx, rule, r)
		if err != nil {
			return nil, err
		}
		if !sold {
			return r, nil
		}
	}
	return nil, nil
}

func conflict(a, b string) bool { return a != "" && b != "" && a != b }

// enrich fills gaps in an existing listing from a duplicate post, e.g.
// "Sell kadamb" followed by "Sell kadamb veg @20". Reports whether anything changed.
func enrich(dst, src *store.Listing) bool {
	changed := false
	set := func(d *string, v string) {
		if *d == "" && v != "" {
			*d, changed = v, true
		}
	}
	set(&dst.Mess, src.Mess)
	set(&dst.Diet, src.Diet)
	set(&dst.Wants, src.Wants)
	if src.Price >= 0 && src.Price != dst.Price {
		dst.Price, changed = src.Price, true
	}
	if src.Qty > dst.Qty {
		dst.Qty, changed = src.Qty, true
	}
	return changed
}

// classify fills in everything derivable from the text and post time.
func (in *Ingester) classify(text string, ts time.Time) store.Listing {
	t := classify.Prepare(text)
	l := store.Listing{
		Text:  text,
		Norm:  classify.Normalize(text),
		Kind:  classify.Kind(t),
		Diet:  classify.Diet(t),
		Price: -1,
		Qty:   classify.Quantity(t),
	}
	messes := classify.Pick(t, in.messes)
	if l.Kind == "exchange" {
		have, want := classify.Exchange(t, messes)
		l.Mess, l.Wants = have, strings.Join(want, ",")
	} else if len(messes) > 0 {
		l.Mess = messes[0].Name
	}
	if in.cfg.VegOnly(l.Mess) {
		l.Diet = "veg"
	}
	// A bare "palash" or "See kadamb" in these groups is a sale.
	if l.Kind == "" && l.Mess != "" {
		l.Kind = "sell"
	}
	if classify.Free(t) {
		l.Price = 0
	} else if v, ok := classify.Price(t); ok {
		l.Price = v
	}

	var slot meals.Slot
	var ok bool
	for _, h := range classify.Pick(t, in.meals) {
		// Several meals named: the listing is for whichever comes first.
		if s, found := in.sched.Next(ts, h.Name); found && (!ok || s.End.Before(slot.End)) {
			slot, ok = s, true
		}
	}
	l.MealSource = "keyword"
	if !ok {
		slot, ok = in.sched.Next(ts, "")
		l.MealSource = "time"
	}
	if ok {
		l.Meal, l.MealDate, l.MealEnd = slot.Meal, slot.Date, slot.End
	}
	return l
}

// Edit handles a sender editing one of their posts.
func (in *Ingester) Edit(ctx context.Context, chat, id, editor, editorAlt, text string, at time.Time) error {
	if !in.groups[chat] {
		return nil
	}
	l, err := in.st.PostListing(ctx, chat, id)
	if err != nil || l == nil || !sameUser(l, editor, editorAlt) {
		return err
	}
	if err := in.st.EditPost(ctx, chat, id, text, at); err != nil {
		return err
	}
	if classify.Prepare(text).Has(in.cfg.Live().Sold.Words...) {
		if err := in.st.MarkSoldByText(ctx, l.ID, at); err != nil {
			return err
		}
	}
	// Re-derive tags from the edited text but keep old ones the edit dropped,
	// so "SOLD" replacing the whole post doesn't erase what was sold.
	n := in.classify(text, l.FirstSeen)
	if n.MealSource != "keyword" {
		n.Meal, n.MealSource, n.MealDate, n.MealEnd = l.Meal, l.MealSource, l.MealDate, l.MealEnd
	}
	if n.Kind == "" {
		n.Kind = l.Kind
	}
	edited := n.Price
	enrich(&n, l)
	if edited >= 0 {
		n.Price = edited
	}
	n.ID = l.ID
	return in.st.UpdateListing(ctx, &n)
}

func (in *Ingester) Delete(ctx context.Context, chat, id string) error {
	if !in.groups[chat] {
		return nil
	}
	return in.st.DeletePost(ctx, chat, id)
}

func (in *Ingester) Reaction(ctx context.Context, chat, target, reactor, reactorAlt, emoji string, at time.Time) error {
	if !in.groups[chat] {
		return nil
	}
	return in.st.SetReaction(ctx, chat, target, reactor, reactorAlt, emoji, at)
}

// DirectMessage is one message in a one-to-one chat.
type DirectMessage struct {
	Peer     string // primary JID of the other person
	PeerAlt  string
	PushName string // only set for incoming messages
	FromMe   bool
	ID       string
	TS       time.Time
	Text     string
	Saved    bool // the peer is in your address book
}

// DM records activity for every DM, but content only for unsaved numbers.
func (in *Ingester) DM(ctx context.Context, m DirectMessage) error {
	if err := in.st.TouchPeer(ctx, m.Peer, m.PeerAlt, m.PushName, m.FromMe, m.TS); err != nil {
		return err
	}
	if m.Saved {
		return in.st.DropDMContent(ctx, m.Peer)
	}
	if strings.TrimSpace(m.Text) == "" {
		return nil
	}
	return in.st.AddDM(ctx, m.Peer, m.ID, m.FromMe, m.TS, m.Text)
}

func sameUser(l *store.Listing, jid, alt string) bool {
	for _, a := range []string{jid, alt} {
		if a != "" && (a == l.Seller || a == l.SellerAlt) {
			return true
		}
	}
	return false
}
