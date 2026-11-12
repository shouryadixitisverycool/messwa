package mcpserver

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"messwa/internal/config"
	"messwa/internal/store"
)

func TestSaleViews(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 12, 13, 52, 0, 0, time.UTC)
	s := New(config.Default(), st, nil)
	s.SetClock(func() time.Time { return now })

	// An earlier try for the same meal, cancelled.
	old, err := st.CreateSellTask(ctx, store.SellTask{Date: "2026-11-12", Meal: "lunch", MinPaise: 1000, MaxPaise: 4000, CreatedAt: now.Add(-time.Hour), EndsAt: now.Add(38 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	st.EndSellTask(ctx, old, store.TaskCancelled, "cancelled", now.Add(-40*time.Minute))
	id, err := st.CreateSellTask(ctx, store.SellTask{Date: "2026-11-12", Meal: "lunch", MinPaise: 1000, MaxPaise: 4000, Mess: "kadamba", Diet: "veg",
		CreatedAt: now.Add(-32 * time.Minute), EndsAt: now.Add(38 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	st.SetSellTaskTarget(ctx, id, 3000)

	// Two posts: one went up and got buried under 23 others, one failed.
	posted := now.Add(-31 * time.Minute)
	st.ClaimOfferPost(ctx, id, "a@g.us", "o1", 0, posted)
	st.SetOfferState(ctx, id, "o1", store.OfferPosted)
	st.ClaimOfferPost(ctx, id, "b@g.us", "o2", 0, posted)
	st.SetOfferFailed(ctx, id, "o2", "server returned error 479")
	mine := &store.Listing{Seller: "me", Kind: "sell", Meal: "lunch", MealDate: "2026-11-12", MealEnd: now.Add(38 * time.Minute), FirstSeen: posted, LastSeen: posted}
	st.CreateListing(ctx, mine)
	st.AddPost(ctx, store.Post{Chat: "a@g.us", ID: "o1", ListingID: mine.ID, Sender: "me", TS: posted, Text: "sell"})
	for i := range 23 {
		l := &store.Listing{Seller: fmt.Sprint(i), Kind: "sell", Meal: "lunch", MealDate: "2026-11-12", MealEnd: now, FirstSeen: now, LastSeen: now}
		st.CreateListing(ctx, l)
		st.AddPost(ctx, store.Post{Chat: "a@g.us", ID: fmt.Sprint("p", i), ListingID: l.ID, Sender: fmt.Sprint(i), TS: posted.Add(time.Duration(i+1) * time.Minute), Text: "sell"})
	}

	// Mikhail went below the lowest and needs you. Only his reading is
	// stored, never his words.
	mikhail := "911111111111@s.whatsapp.net"
	st.AddSellTaskBuyer(ctx, id, mikhail, "", now.Add(-14*time.Minute))
	st.ClaimNegotiationMessage(ctx, id, mikhail, 0, store.Negotiation{Ask: 3000, BotMsgs: 1, LastOffer: 1500, StartedAt: now})
	st.ParkSellTaskBuyer(ctx, id, mikhail, "offered ₹15 after the bot's lowest of ₹30")
	for _, e := range []store.SellEvent{
		{Kind: store.EventRead, N: 1000, Text: "offer"},
		{Kind: store.EventBotSaid, N: 3000, Text: "₹30 is the lowest I can do"},
		{Kind: store.EventRead, N: 1500, Text: "offer"},
		{Kind: store.EventParked, Text: "offered ₹15 after the bot's lowest of ₹30"},
		{Kind: store.EventJoined, Peer: "someone-else"},
	} {
		if e.Peer == "" {
			e.Peer = mikhail
		}
		e.TaskID, e.At = id, now.Add(-12*time.Minute)
		if err := st.AddSellTaskEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	vs, err := s.SaleViews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The cancelled try ended 40 minutes ago, past the linger.
	if len(vs) != 1 {
		t.Fatalf("views = %+v", vs)
	}
	v := vs[0]
	if v.ID != id || v.Min != 10 || v.Max != 40 || v.Target != 30 || !v.Negotiates {
		t.Fatalf("view = %+v", v)
	}
	// 38 minutes left of the 3-hour slide from ₹30 to ₹10, in ₹5 steps.
	if v.Lowest != 15 || v.Held {
		t.Fatalf("lowest = %v held = %v", v.Lowest, v.Held)
	}
	if len(v.Posts) != 2 || v.Posts[0].After != 23 || v.Posts[1].State != "failed" || v.Posts[1].Error != "server returned error 479" {
		t.Fatalf("posts = %+v", v.Posts)
	}
	if len(v.Listings) != 1 || v.Listings[0] != mine.ID {
		t.Fatalf("listings = %v", v.Listings)
	}
	b := v.Buyers[0]
	if b.Key != "+911111111111" || b.State != store.BuyerParked || b.Offer != 15 || b.Ask != 30 || len(b.Events) != 4 {
		t.Fatalf("buyer = %+v", b)
	}
	if len(v.History) != 2 || v.History[0].State != store.TaskCancelled || v.History[1].Buyers != 1 {
		t.Fatalf("history = %+v", v.History)
	}

	// A second active buyer holds the price at the target.
	st.AddSellTaskBuyer(ctx, id, "922222222222@s.whatsapp.net", "", now)
	st.ClaimNegotiationMessage(ctx, id, "922222222222@s.whatsapp.net", 0, store.Negotiation{Ask: 4000, BotMsgs: 1, StartedAt: now})
	st.AddSellTaskBuyer(ctx, id, "933333333333@s.whatsapp.net", "", now)
	st.ClaimNegotiationMessage(ctx, id, "933333333333@s.whatsapp.net", 0, store.Negotiation{Ask: 4000, BotMsgs: 1, StartedAt: now})
	vs, _ = s.SaleViews(ctx)
	if vs[0].Lowest != 30 || !vs[0].Held {
		t.Fatalf("lowest = %v held = %v", vs[0].Lowest, vs[0].Held)
	}
}
