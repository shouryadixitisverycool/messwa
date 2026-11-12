package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"messwa/internal/config"
	"messwa/internal/ingest"
	"messwa/internal/portal"
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

func TestParseBuyerJID(t *testing.T) {
	for in, want := range map[string]string{
		"+91 98765 43210":             "919876543210@s.whatsapp.net",
		"919876543210":                "919876543210@s.whatsapp.net",
		"+91-98765-43210":             "919876543210@s.whatsapp.net",
		"919876543210@s.whatsapp.net": "919876543210@s.whatsapp.net",
		"12345@lid":                   "12345@lid",
	} {
		if got, err := parseBuyerJID(in); err != nil || got.String() != want {
			t.Errorf("%q = %v, %v; want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"", "9876543210", "Daniel", "120363000000000001@g.us", "1234567890123456"} {
		if _, err := parseBuyerJID(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestParseMarkSold(t *testing.T) {
	cfg, _, _, _ := soldSetup(t)
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC) // 15:30 in Kolkata
	a, err := parseMarkSold(cfg, []string{"-date", "2026-11-12", "-meal", "lunch", "-peer", "+91 98765 43210", "-name", " Daniel ",
		"-amount", "20", "-paid-by", "image", "-at", "13:45"}, now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 12, 13, 45, 0, 0, cfg.Location())
	if a.sale.Peer != "919876543210@s.whatsapp.net" || a.sale.Amount != "20.00" || a.sale.Kind != store.PaidByImage ||
		a.sale.BuyerName != "Daniel" || !a.sale.At.Equal(want) || !a.react || a.force {
		t.Fatalf("parsed %+v", a)
	}
	a, err = parseMarkSold(cfg, []string{"-date", "2026-11-12", "-meal", "lunch", "-peer", "919876543210", "-amount", "₹20.50", "-react=false"}, now)
	if err != nil || a.sale.Kind != "" || a.sale.Amount != "20.50" || !a.sale.At.Equal(now) || a.react {
		t.Fatalf("defaults: %+v, %v", a, err)
	}
	base := []string{"-date", "2026-11-12", "-meal", "lunch", "-peer", "919876543210", "-amount", "20"}
	for name, extra := range map[string][]string{
		"no buyer":      {"-peer", ""},
		"no amount":     {"-amount", ""},
		"zero amount":   {"-amount", "0"},
		"bad paid-by":   {"-paid-by", "cash"},
		"future time":   {"-at", "18:00"},
		"bad time":      {"-at", "lunchtime"},
		"unknown meal":  {"-meal", "brunch"},
		"bad date":      {"-date", "12-11-2026"},
		"extra args":    {"extra"},
		"three decimal": {"-amount", "20.505"},
	} {
		if _, err := parseMarkSold(cfg, append(append([]string{}, base...), extra...), now); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

type fakeRegistrations struct {
	regs []portal.Registration
	err  error
}

func (f fakeRegistrations) Registrations(context.Context, string, string) ([]portal.Registration, error) {
	return f.regs, f.err
}

// TestMarkSoldRecordsTheSale is the 12 Nov case: the QR went out by hand,
// so the ledger had no row and the sales page said "ate". mark-sold now
// records the sale, with the mess and diet from the (availed) registration,
// even though no offer post was recorded.
func TestMarkSoldRecordsTheSale(t *testing.T) {
	cfg, st, _, log := soldSetup(t)
	ctx := t.Context()
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	a, err := parseMarkSold(cfg, []string{"-date", "2026-11-12", "-meal", "lunch", "-peer", "919876543210", "-name", "Daniel", "-amount", "20", "-paid-by", "image"}, now)
	if err != nil {
		t.Fatal(err)
	}
	availed := "2026-11-12T08:10:00Z"
	regs := fakeRegistrations{regs: []portal.Registration{
		{Date: "2026-11-12", Meal: "breakfast", Mess: "palash-nonveg"},
		{Date: "2026-11-12", Meal: "lunch", Mess: "kadamba-veg", AvailedAt: &availed},
	}}
	msg, err := recordSale(ctx, cfg, st, log, regs, a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "Recorded 2026-11-12 lunch as sold") || !strings.Contains(msg, "₹20") || !strings.Contains(msg, "image, unverified") {
		t.Fatalf("message = %q", msg)
	}
	ds, err := st.Deliveries(ctx)
	if err != nil || len(ds) != 1 {
		t.Fatalf("ledger = %+v, %v", ds, err)
	}
	if d := ds[0]; d.State != "sent" || d.Mess != "kadamba" || d.Diet != "veg" || d.BuyerName != "Daniel" || d.PaymentKind != store.PaidByImage || !d.SentAt.Equal(now) {
		t.Fatalf("row = %+v", d)
	}

	// Running it again refuses, and says how to overwrite.
	if _, err := recordSale(ctx, cfg, st, log, regs, a); err == nil || !strings.Contains(err.Error(), "-force") {
		t.Fatalf("second run: %v", err)
	}
	a.force = true
	if msg, err := recordSale(ctx, cfg, st, log, regs, a); err != nil || !strings.Contains(msg, "It was sent before") {
		t.Fatalf("forced: %q, %v", msg, err)
	}

	// Without the portal the sale is still recorded.
	b, _ := parseMarkSold(cfg, []string{"-date", "2026-11-12", "-meal", "dinner", "-peer", "919876543210", "-amount", "25"}, now)
	if _, err := recordSale(ctx, cfg, st, log, fakeRegistrations{err: errors.New("portal down")}, b); err != nil {
		t.Fatal(err)
	}
	if _, err := recordSale(ctx, cfg, st, log, nil, b); err == nil {
		t.Fatal("recorded dinner twice without -force")
	}
}
