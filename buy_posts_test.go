package main

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"go.mau.fi/whatsmeow/types"

	"messwa/internal/store"
)

// buyPost records a buy post by who in the fixture's group, as ingest
// would, and returns its message ID.
func (f *sellFixture) buyPost(who types.JID, meal, mess, diet, text string) string {
	f.t.Helper()
	ctx := context.Background()
	now := time.Now()
	slot, err := findSlot(f.cfg, f.date, meal)
	if err != nil {
		f.t.Fatal(err)
	}
	l := store.Listing{Seller: who.String(), SellerName: "poster " + who.User, Kind: "buy", Meal: meal, MealSource: "named",
		MealDate: f.date, MealEnd: slot.End, Mess: mess, Diet: diet, Price: -1, Qty: 1, Text: text, Norm: text, FirstSeen: now, LastSeen: now}
	if err := f.st.CreateListing(ctx, &l); err != nil {
		f.t.Fatal(err)
	}
	id := "buy-" + who.User + "-" + meal
	if err := f.st.AddPost(ctx, store.Post{Chat: f.group.String(), ID: id, ListingID: l.ID, Sender: who.String(), TS: now, Text: text}); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func TestSellTaskMessagesBuyPosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		other := types.NewJID("914444444444", types.DefaultUserServer)
		dinner := types.NewJID("915555555555", types.DefaultUserServer)
		veg := types.NewJID("916666666666", types.DefaultUserServer)
		postID := f.buyPost(buyerA, "lunch", "bakul", "", "need bakul lunch")
		f.buyPost(buyerB, "lunch", "", "", "anyone selling lunch")
		f.buyPost(other, "lunch", "kadamba", "", "need kadamba lunch")
		f.buyPost(dinner, "dinner", "bakul", "", "need bakul dinner")
		f.buyPost(veg, "lunch", "bakul", "veg", "need bakul veg lunch")

		m := f.negotiatingManager(ctx, 40)
		id, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		settle()

		got := sentTo(f.conn.messages(), buyerA)
		if len(got) != 2 || got[0].text != "have bakul non-veg lunch" || got[0].quoted != postID || got[1].text != "60" {
			t.Fatalf("buy poster got %+v", got)
		}
		if got := texts(sentTo(f.conn.messages(), buyerB)); len(got) != 2 {
			t.Fatalf("poster who named no mess got %q", got)
		}
		for _, p := range []types.JID{other, dinner, veg} {
			if got := sentTo(f.conn.messages(), p); len(got) != 0 {
				t.Fatalf("%s got %+v", p, got)
			}
		}
		b := buyerState(t, f, id, buyerA)
		if b.State != store.BuyerNegotiating || b.Name != "poster "+buyerA.User {
			t.Fatalf("buyer = %+v", b)
		}
		if ev := taskEvents(t, f, id, buyerA); !slices.Contains(ev, store.EventBuyPost) || !slices.Contains(ev, store.EventJoined) {
			t.Fatalf("events = %q", ev)
		}

		// They haggle like anyone who replied.
		f.conn.emit(dm(buyerA, "40?"))
		settle()
		if got := texts(sentTo(f.conn.messages(), buyerA)); len(got) < 3 || got[2] != "ok 40" {
			t.Fatalf("buy poster got %q", got)
		}

		// Later sweeps message new posts, and nobody twice.
		late := types.NewJID("917777777777", types.DefaultUserServer)
		f.buyPost(late, "lunch", "bakul", "nonveg", "need bakul nv lunch")
		time.Sleep(buyPostSweep)
		settle()
		if got := texts(sentTo(f.conn.messages(), late)); len(got) != 2 || got[0] != "have bakul non-veg lunch" {
			t.Fatalf("late poster got %q", got)
		}
		if got := texts(sentTo(f.conn.messages(), buyerA)); countOf(got, "have bakul non-veg lunch") != 1 {
			t.Fatalf("buy poster was messaged again: %q", got)
		}
	})
}

func TestSellTaskBuyPostsOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.buyPost(buyerA, "lunch", "bakul", "", "need bakul lunch")
		f.cfg.Sell.MessageBuyPosts = false
		m := f.negotiatingManager(ctx, 40)
		if _, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, ""); err != nil {
			t.Fatal(err)
		}
		settle()
		if got := sentTo(f.conn.messages(), buyerA); len(got) != 0 {
			t.Fatalf("buy poster got %+v with message_buy_posts off", got)
		}
	})
}

func TestSellTaskBuyPostsSkipOnReplyHandoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.buyPost(buyerA, "lunch", "bakul", "", "need bakul lunch")
		m := f.manager(ctx)
		id, err := m.Start(ctx, 0, f.date, "lunch", 30, 30, "on_reply")
		if err != nil {
			t.Fatal(err)
		}
		settle()
		if got := sentTo(f.conn.messages(), buyerA); len(got) != 0 {
			t.Fatalf("buy poster got %+v on an on_reply task", got)
		}
		if task := f.task(id); task.State != store.TaskActive {
			t.Fatalf("task = %s", task.State)
		}
	})
}

func TestSellTaskBuyPostsUnsavedOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	synctest.Test(t, func(t *testing.T) {
		f := newSellFixture(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.buyPost(buyerA, "lunch", "bakul", "", "need bakul lunch")
		f.conn.saved = map[types.JID]bool{buyerA: true}
		f.cfg.Sell.Buyers = "unsaved_only"
		m := f.negotiatingManager(ctx, 40)
		if _, err := m.Start(ctx, 0, f.date, "lunch", 30, 60, ""); err != nil {
			t.Fatal(err)
		}
		settle()
		if got := sentTo(f.conn.messages(), buyerA); len(got) != 0 {
			t.Fatalf("saved contact got %+v with buyers = unsaved_only", got)
		}
	})
}

func countOf(xs []string, x string) int {
	n := 0
	for _, s := range xs {
		if s == x {
			n++
		}
	}
	return n
}
