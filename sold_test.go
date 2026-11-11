package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"messwa/internal/config"
	"messwa/internal/ingest"
	"messwa/internal/store"
)

func soldSetup(t *testing.T) (*config.Config, *store.Store, ownGroupLog, *slog.Logger) {
	t.Helper()
	cfg := config.Default()
	cfg.Groups = []string{"111@g.us", "222@g.us"}
	st, err := store.Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	own := ownGroupLog{in: ingest.New(cfg, st), jid: "919999@s.whatsapp.net", alt: "77@lid", name: "me"}
	return cfg, st, own, slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestOfferPostsLedger(t *testing.T) {
	_, st, _, _ := soldSetup(t)
	ctx := t.Context()
	at := time.Date(2026, 11, 12, 11, 0, 0, 0, time.UTC)
	for _, p := range []store.OfferPost{
		{Date: "2026-11-12", Meal: "lunch", Chat: "222@g.us", ID: "B", PostedAt: at.Add(time.Minute)},
		{Date: "2026-11-12", Meal: "lunch", Chat: "111@g.us", ID: "A", PostedAt: at},
		{Date: "2026-11-12", Meal: "lunch", Chat: "111@g.us", ID: "A", PostedAt: at.Add(time.Hour)}, // repeat
		{Date: "2026-11-12", Meal: "dinner", Chat: "111@g.us", ID: "C", PostedAt: at},
	} {
		if err := st.RecordOfferPost(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RecordOfferPost(ctx, store.OfferPost{Date: "2026-11-12", Meal: "lunch", Chat: "111@g.us"}); err == nil {
		t.Error("recorded an offer post without a message ID")
	}
	if err := st.MarkOfferReacted(ctx, "222@g.us", "B", at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	posts, err := st.OfferPosts(ctx, "2026-11-12", "lunch")
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 || posts[0].ID != "A" || posts[1].ID != "B" {
		t.Fatalf("lunch posts = %+v, want A then B", posts)
	}
	if !posts[0].PostedAt.Equal(at) || !posts[0].ReactedAt.IsZero() {
		t.Errorf("A = %+v, want first posting time and no reaction", posts[0])
	}
	if !posts[1].ReactedAt.Equal(at.Add(2 * time.Hour)) {
		t.Errorf("B reacted at %v", posts[1].ReactedAt)
	}
}

// messwa's own offer and ✅ never come back as events, so the group log is
// written directly. The board must then count the listing as sold, and a
// failed reaction must not stop the others.
func TestReactSoldMarksListingSold(t *testing.T) {
	cfg, st, own, log := soldSetup(t)
	ctx := t.Context()
	posted, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-12 11:00", cfg.Location())
	recordOffers(ctx, st, own, log, "2026-11-12", "lunch", []postedOffer{
		{Chat: types.NewJID("111", types.GroupServer), ID: "OFFER1", At: posted, Text: "sell Kadamba veg lunch ₹60"},
		{Chat: types.NewJID("222", types.GroupServer), ID: "OFFER2", At: posted.Add(time.Second), Text: "sell Kadamba veg lunch ₹60"},
		{Chat: types.NewJID("999", types.GroupServer), ID: "TEST", At: posted, Text: "sell Kadamba veg lunch ₹60"},
	})
	rule := store.SoldRule{Emojis: cfg.Sold.Emojis, ClosedEmojis: cfg.Sold.ClosedEmojis}
	listing := func() store.Listing {
		t.Helper()
		ls, err := st.Listings(ctx, rule, store.ListingFilter{Since: posted.Add(-time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if len(ls) != 1 {
			t.Fatalf("listings = %+v, want the one offer from both market groups", ls)
		}
		return ls[0]
	}
	if l := listing(); l.Seller != own.jid || l.SoldReact {
		t.Fatalf("before the sale: seller=%s sold=%v", l.Seller, l.SoldReact)
	}

	var reacted []string
	react := func(_ context.Context, chat types.JID, target, emoji string) (string, time.Time, error) {
		if emoji != soldReaction {
			t.Errorf("reaction = %q", emoji)
		}
		if target == "OFFER1" {
			return "", time.Time{}, errors.New("network down")
		}
		reacted = append(reacted, chat.String()+"/"+target)
		return "R-" + target, posted.Add(10 * time.Minute), nil
	}
	done, total, err := reactSold(ctx, st, own, react, log, "2026-11-12", "lunch")
	if err != nil || done != 2 || total != 3 {
		t.Fatalf("reactSold = %d of %d, %v; want 2 of 3", done, total, err)
	}
	if len(reacted) != 2 || reacted[0] != "999@g.us/TEST" || reacted[1] != "222@g.us/OFFER2" {
		t.Errorf("reacted to %v", reacted)
	}
	if l := listing(); !l.SoldReact {
		t.Error("the seller's own ✅ on one copy should mark the listing sold")
	}
	posts, err := st.OfferPosts(ctx, "2026-11-12", "lunch")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range posts {
		if (p.ID == "OFFER1") != p.ReactedAt.IsZero() {
			t.Errorf("%s reacted at %v", p.ID, p.ReactedAt)
		}
	}
}

func TestReactSoldWithoutOffers(t *testing.T) {
	_, st, own, log := soldSetup(t)
	react := func(context.Context, types.JID, string, string) (string, time.Time, error) {
		t.Fatal("reacted with no offer posts")
		return "", time.Time{}, nil
	}
	if done, total, err := reactSold(t.Context(), st, own, react, log, "2026-11-12", "lunch"); err != nil || done != 0 || total != 0 {
		t.Fatalf("reactSold = %d of %d, %v", done, total, err)
	}
}
