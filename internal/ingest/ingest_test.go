package ingest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"messwa/internal/config"
	"messwa/internal/store"
)

func setup(t *testing.T) (*Ingester, *store.Store, *config.Config) {
	t.Helper()
	cfg := config.Default()
	cfg.Groups = []string{"g1@g.us", "g2@g.us", "g3@g.us", "g4@g.us"}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(cfg, st), st, cfg
}

func at(cfg *config.Config, hm string) time.Time {
	v, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-02 "+hm, cfg.Location())
	return v
}

func rule(cfg *config.Config) store.SoldRule {
	return store.SoldRule{Emojis: cfg.Sold.Emojis, AdminsCount: cfg.Sold.AdminsCount}
}

func TestDedupeAcrossGroups(t *testing.T) {
	in, st, cfg := setup(t)
	ctx := context.Background()
	seller := "911111@s.whatsapp.net"
	posts := []GroupPost{
		{Chat: "g1@g.us", ID: "a", Sender: seller, SenderAlt: "1@lid", TS: at(cfg, "10:00"), Text: "Selling Kadamba lunch 🍛"},
		{Chat: "g2@g.us", ID: "b", Sender: seller, TS: at(cfg, "10:01"), Text: "selling kadamba lunch!!"},
		{Chat: "g3@g.us", ID: "c", Sender: "1@lid", TS: at(cfg, "10:02"), Text: "Selling Kadamba lunch veg"},
		{Chat: "g4@g.us", ID: "d", Sender: "922222@s.whatsapp.net", TS: at(cfg, "10:03"), Text: "selling kadamba lunch"},
		{Chat: "g1@g.us", ID: "e", Sender: "933333@s.whatsapp.net", TS: at(cfg, "10:04"), Text: "good morning everyone"},
		{Chat: "other@g.us", ID: "f", Sender: seller, TS: at(cfg, "10:05"), Text: "selling dinner"},
	}
	for _, p := range posts {
		if err := in.Post(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	ls, err := st.Listings(ctx, rule(cfg), store.ListingFilter{Since: at(cfg, "00:00")})
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 2 {
		t.Fatalf("want 2 listings, got %d: %+v", len(ls), ls)
	}
	var first store.Listing
	for _, l := range ls {
		if l.Seller == seller {
			first = l
		}
	}
	if len(first.Chats) != 3 || first.Meal != "lunch" || first.Mess != "kadamba" || first.Kind != "sell" || first.MealSource != "keyword" {
		t.Errorf("merged listing wrong: %+v", first)
	}
}

func TestSoldRules(t *testing.T) {
	in, st, cfg := setup(t)
	ctx := context.Background()
	seller := "911111@s.whatsapp.net"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "a", Sender: seller, SenderAlt: "1@lid", TS: at(cfg, "18:00"), Text: "selling dinner"}))
	must(in.Post(ctx, GroupPost{Chat: "g2@g.us", ID: "b", Sender: seller, TS: at(cfg, "18:01"), Text: "selling dinner"}))
	sold := func() bool {
		t.Helper()
		ls, err := st.Listings(ctx, rule(cfg), store.ListingFilter{Since: at(cfg, "00:00")})
		must(err)
		if len(ls) != 1 {
			t.Fatalf("got %d listings", len(ls))
		}
		return ls[0].SoldReact || ls[0].SoldText
	}

	must(in.Reaction(ctx, "g1@g.us", "a", "9random@s.whatsapp.net", "", "✅", at(cfg, "18:05")))
	if sold() {
		t.Error("a stranger's ✅ must not count")
	}
	must(in.Reaction(ctx, "g2@g.us", "b", "1@lid", seller, "✅", at(cfg, "18:06")))
	if !sold() {
		t.Error("seller's ✅ on any copy (via LID) should count")
	}
	must(in.Reaction(ctx, "g2@g.us", "b", "1@lid", seller, "", at(cfg, "18:07")))
	if sold() {
		t.Error("removing the ✅ should un-sell")
	}
	must(st.SetGroup(ctx, "g1@g.us", "Mess 1", []string{"9random@s.whatsapp.net"}))
	if sold() {
		t.Error("admin's ✅ should not count by default")
	}
	cfg.Sold.AdminsCount = true
	if !sold() {
		t.Error("admin's ✅ should count with admins_count")
	}
	cfg.Sold.AdminsCount = false
	must(st.SetGroup(ctx, "g1@g.us", "Mess 1", nil))
	must(in.Edit(ctx, "g1@g.us", "a", seller, "", "SOLD", at(cfg, "18:10")))
	if !sold() {
		t.Error("editing to SOLD should mark sold")
	}
	ls, _ := st.Listings(ctx, rule(cfg), store.ListingFilter{Since: at(cfg, "00:00")})
	if ls[0].Meal != "dinner" || ls[0].Kind != "sell" {
		t.Errorf("edit erased tags: %+v", ls[0])
	}
	must(in.Delete(ctx, "g1@g.us", "a"))
	must(in.Delete(ctx, "g2@g.us", "b"))
	ls, _ = st.Listings(ctx, rule(cfg), store.ListingFilter{Since: at(cfg, "00:00")})
	if !ls[0].Deleted {
		t.Error("all copies deleted should mark listing deleted")
	}
}

func TestSoldReply(t *testing.T) {
	in, st, cfg := setup(t)
	ctx := context.Background()
	seller := "911111@s.whatsapp.net"
	in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "a", Sender: seller, TS: at(cfg, "07:00"), Text: "selling bf"})
	in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "r1", Sender: "9x@s.whatsapp.net", ReplyTo: "a", TS: at(cfg, "07:01"), Text: "sold?"})
	ls, _ := st.Listings(ctx, rule(cfg), store.ListingFilter{Since: at(cfg, "00:00")})
	if ls[0].SoldText {
		t.Error("someone else's reply must not mark sold")
	}
	in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "r2", Sender: seller, ReplyTo: "a", TS: at(cfg, "07:02"), Text: "Sold!"})
	ls, _ = st.Listings(ctx, rule(cfg), store.ListingFilter{Since: at(cfg, "00:00")})
	if !ls[0].SoldText || ls[0].Meal != "breakfast" {
		t.Errorf("seller's sold reply: %+v", ls[0])
	}
}

func TestDMBoundary(t *testing.T) {
	in, st, cfg := setup(t)
	ctx := context.Background()
	in.DM(ctx, DirectMessage{Peer: "9saved@s.whatsapp.net", ID: "1", TS: at(cfg, "10:00"), Text: "private stuff", Saved: true})
	in.DM(ctx, DirectMessage{Peer: "9new@s.whatsapp.net", PushName: "Ravi", ID: "2", TS: at(cfg, "10:01"), Text: "is lunch available?"})
	in.DM(ctx, DirectMessage{Peer: "9new@s.whatsapp.net", FromMe: true, ID: "3", TS: at(cfg, "10:02"), Text: "yes"})

	if dms, _ := st.DMs(ctx, "9saved@s.whatsapp.net", time.Unix(0, 0), 50); len(dms) != 0 {
		t.Error("saved contact content was stored")
	}
	if dms, _ := st.DMs(ctx, "9new@s.whatsapp.net", time.Unix(0, 0), 50); len(dms) != 2 {
		t.Errorf("unsaved content: got %d", len(dms))
	}
	peers, _ := st.Peers(ctx, at(cfg, "00:00"))
	if len(peers) != 2 {
		t.Fatalf("peers: %+v", peers)
	}
	// Number gets saved later: next message drops what was stored.
	in.DM(ctx, DirectMessage{Peer: "9new@s.whatsapp.net", ID: "4", TS: at(cfg, "11:00"), Text: "hi", Saved: true})
	if dms, _ := st.DMs(ctx, "9new@s.whatsapp.net", time.Unix(0, 0), 50); len(dms) != 0 {
		t.Error("content should be dropped once the number is saved")
	}
}

func TestNoMergeIntoSoldOrOtherMess(t *testing.T) {
	in, st, cfg := setup(t)
	ctx := context.Background()
	s := "911111@s.whatsapp.net"
	in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "a", Sender: s, TS: at(cfg, "10:00"), Text: "selling kadamba lunch"})
	in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "b", Sender: s, TS: at(cfg, "10:05"), Text: "selling yuktahar lunch"})
	in.Reaction(ctx, "g1@g.us", "a", s, "", "✅", at(cfg, "10:10"))
	in.Post(ctx, GroupPost{Chat: "g2@g.us", ID: "c", Sender: s, TS: at(cfg, "10:20"), Text: "selling kadamba lunch again"})
	ls, _ := st.Listings(ctx, rule(cfg), store.ListingFilter{Since: at(cfg, "00:00")})
	if len(ls) != 3 {
		t.Fatalf("want 3 listings (kadamba sold, yuktahar, kadamba repost), got %d", len(ls))
	}
}

func TestHandleAndRebuild(t *testing.T) {
	in, st, cfg := setup(t)
	ctx := context.Background()
	s := "911111@s.whatsapp.net"
	events := []store.LogEntry{
		{Chat: "g1@g.us", ChatName: "Mess Sell", ID: "a", Type: "post", Sender: s, TS: at(cfg, "10:00"), Text: "selling lunch"},
		{Chat: "g2@g.us", ID: "b", Type: "post", Sender: s, TS: at(cfg, "10:01"), Text: "selling lunch"},
		{Chat: "g2@g.us", ID: "r", Type: "reaction", Sender: s, TS: at(cfg, "10:30"), Text: "✅", Target: "b"},
		{Chat: "other@g.us", ID: "x", Type: "post", Sender: s, TS: at(cfg, "10:02"), Text: "selling dinner"},
	}
	for _, e := range append(events, events...) { // replays must be no-ops
		if err := in.Handle(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	check := func(when string) {
		t.Helper()
		ls, _ := st.Listings(ctx, rule(cfg), store.ListingFilter{Since: at(cfg, "00:00")})
		if len(ls) != 1 || len(ls[0].Chats) != 2 || !ls[0].SoldReact {
			t.Fatalf("%s: %+v", when, ls)
		}
	}
	check("live")
	log, _ := st.Log(ctx, at(cfg, "00:00"))
	if len(log) != 3 {
		t.Errorf("log should hold the 3 market-group events, got %d", len(log))
	}
	if names, _ := st.GroupNames(ctx); names["g1@g.us"] != "Mess Sell" {
		t.Errorf("group name: %v", names)
	}
	if n, err := in.Rebuild(ctx); err != nil || n != 3 {
		t.Fatalf("rebuild: %d %v", n, err)
	}
	check("rebuilt")
}

func TestReactionsAndReplies(t *testing.T) {
	in, st, cfg := setup(t)
	ctx := context.Background()
	r := store.SoldRule{Emojis: cfg.Sold.Emojis, ClosedEmojis: cfg.Sold.ClosedEmojis, AdminsCount: true}
	get := func(id int64) store.Listing {
		t.Helper()
		l, _, err := st.Listing(ctx, r, id)
		if err != nil || l == nil {
			t.Fatal(err)
		}
		return *l
	}
	a, b := "91@s.whatsapp.net", "92@s.whatsapp.net"
	in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "p1", Sender: a, TS: at(cfg, "12:00"), Text: "Sell kadamb veg @30"})
	in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "p2", Sender: a, ReplyTo: "p1", TS: at(cfg, "12:10"), Text: "@25"})
	in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "p3", Sender: a, ReplyTo: "p2", TS: at(cfg, "12:20"), Text: "20"})
	in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "p4", Sender: b, ReplyTo: "p1", TS: at(cfg, "12:21"), Text: "@10"})
	l := get(1)
	if l.Price != 20 {
		t.Errorf("reply chain price = %d, want 20", l.Price)
	}
	in.Reaction(ctx, "g1@g.us", "p1", b, "", "✅", at(cfg, "12:30"))
	if l = get(1); l.SoldReact || l.OtherTicks != 1 {
		t.Errorf("buyer tick: sold=%v others=%d", l.SoldReact, l.OtherTicks)
	}
	in.Reaction(ctx, "g1@g.us", "p1", a, "", "❌", at(cfg, "12:31"))
	if l = get(1); !l.Closed {
		t.Error("seller ❌ should close")
	}
	in.Reaction(ctx, "g1@g.us", "p1", a, "", "👍🏽", at(cfg, "12:32"))
	if l = get(1); !l.SoldReact || l.Closed {
		t.Errorf("seller 👍🏽 should replace ❌ and mean sold: %+v", l)
	}
	in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "p5", Sender: a, TS: at(cfg, "12:40"), Text: "Exchange want palash have kadamb"})
	ls, _ := st.Listings(ctx, r, store.ListingFilter{Since: at(cfg, "00:00"), Kind: "exchange"})
	if len(ls) != 1 || ls[0].Mess != "kadamba" || ls[0].Wants != "palash" {
		t.Errorf("exchange: %+v", ls)
	}
}

func TestVegOnlyMess(t *testing.T) {
	in, st, cfg := setup(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "a", Sender: "91a@s.whatsapp.net", TS: at(cfg, "10:00"), Text: "Sell palash lunch"}))
	must(in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "b", Sender: "91b@s.whatsapp.net", TS: at(cfg, "10:00"), Text: "Sell kadamba lunch"}))
	must(in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "c", Sender: "91c@s.whatsapp.net", TS: at(cfg, "10:00"), Text: "Exchange have yuktahar want kadamba nv"}))
	ls, err := st.Listings(ctx, rule(cfg), store.ListingFilter{Since: at(cfg, "00:00")})
	must(err)
	diet := map[string]string{}
	for _, l := range ls {
		diet[l.Mess] = l.Diet
	}
	if diet["palash"] != "veg" || diet["kadamba"] != "" || diet["yuktahar"] != "veg" {
		t.Errorf("diets: %v", diet)
	}
}

func TestMarket(t *testing.T) {
	in, st, cfg := setup(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	a, b, c := "911111@s.whatsapp.net", "922222@s.whatsapp.net", "933333@s.whatsapp.net"
	must(in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "a1", Sender: a, SenderAlt: "1@lid", TS: at(cfg, "18:00"), Text: "selling kadamba dinner @30"}))
	must(in.Post(ctx, GroupPost{Chat: "g2@g.us", ID: "a2", Sender: a, TS: at(cfg, "18:01"), Text: "selling kadamba dinner @30"}))
	must(in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "b1", Sender: b, TS: at(cfg, "18:02"), Text: "selling palash dinner"}))
	must(in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "c1", Sender: c, TS: at(cfg, "18:03"), Text: "need kadamba dinner"}))
	must(in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "x", Sender: c, TS: at(cfg, "18:04"), Text: "hello"}))

	// a sells via LID tick on the second copy, a stranger's tick on b's post
	// doesn't count, and c's buy order going through isn't a sale.
	must(in.Reaction(ctx, "g2@g.us", "a2", "1@lid", a, "✅", at(cfg, "18:20")))
	must(in.Reaction(ctx, "g1@g.us", "b1", c, "", "✅", at(cfg, "18:21")))
	must(in.Reaction(ctx, "g1@g.us", "c1", c, "", "✅", at(cfg, "18:22")))
	// a's reply "sold" later doesn't move the sale time
	// and, like other replies under a listing, doesn't count as a message.
	must(in.Handle(ctx, store.LogEntry{Chat: "g1@g.us", ID: "a3", Type: "post", Sender: a, ReplyTo: "a1", TS: at(cfg, "18:30"), Text: "sold"}))

	posts, sales, err := st.Market(ctx, rule(cfg), at(cfg, "17:00"), at(cfg, "19:00"))
	must(err)
	if len(posts) != 4 {
		t.Fatalf("want 4 posts (2 copies, palash, buy order), got %+v", posts)
	}
	if posts[0].Mess != "kadamba" || posts[0].Price != 30 || posts[3].Kind != "buy" {
		t.Errorf("posts: %+v", posts)
	}
	if len(sales) != 1 || sales[0].Mess != "kadamba" || !sales[0].TS.Equal(at(cfg, "18:20")) || !sales[0].FirstSeen.Equal(at(cfg, "18:00")) {
		t.Fatalf("sales: %+v", sales)
	}

	_, sales, err = st.Market(ctx, rule(cfg), at(cfg, "18:25"), at(cfg, "19:00"))
	must(err)
	if len(sales) != 0 {
		t.Errorf("a sale before the range leaked in: %+v", sales)
	}
	posts, _, err = st.Market(ctx, rule(cfg), at(cfg, "17:00"), at(cfg, "18:01"))
	must(err)
	if len(posts) != 2 {
		t.Errorf("posts after until leaked in: %+v", posts)
	}
}

func TestExtraOnlyListingExcludedFromMealPrices(t *testing.T) {
	in, st, cfg := setup(t)
	ctx := context.Background()
	ts := at(cfg, "18:00")
	if err := in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "m1", Sender: "s1@s.whatsapp.net", Text: "sell kadamba dinner egg (extra only) x2 15", TS: ts}); err != nil {
		t.Fatal(err)
	}
	if err := in.Post(ctx, GroupPost{Chat: "g1@g.us", ID: "m2", Sender: "s2@s.whatsapp.net", Text: "sell kadamba non-veg dinner 65", TS: ts}); err != nil {
		t.Fatal(err)
	}

	prices, err := st.Prices(ctx, "2026-01-01", "2026-12-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(prices) != 1 || prices[0].Diet != "nonveg" || len(prices[0].Prices) != 1 || prices[0].Prices[0] != 65 {
		t.Fatalf("extra-only listing polluted meal prices: %+v", prices)
	}
}
