package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPricesOutlivePrune(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seen := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for i, p := range []int{60, 50, -1, 0, 70} {
		l := &Listing{Seller: string(rune('a' + i)), Kind: "sell", Meal: "lunch", MealDate: "2026-11-02", Mess: "kadamba", Price: p, Qty: 1, Text: "x", Norm: "x", FirstSeen: seen, LastSeen: seen}
		if err := s.CreateListing(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	buy := &Listing{Seller: "z", Kind: "buy", Meal: "lunch", MealDate: "2026-11-02", Mess: "kadamba", Price: 90, Qty: 1, Text: "x", Norm: "x", FirstSeen: seen, LastSeen: seen}
	if err := s.CreateListing(ctx, buy); err != nil {
		t.Fatal(err)
	}
	ps, err := s.Prices(ctx, "2026-11-01", "2026-11-03")
	if err != nil || len(ps) != 1 || len(ps[0].Prices) != 3 || ps[0].Prices[0] != 50 || ps[0].Prices[2] != 70 {
		t.Fatalf("live prices %+v %v: want 50 60 70, no free, unpriced or buy orders", ps, err)
	}
	if err := s.SnapshotPrices(ctx, "2026-11-01"); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(ctx, seen, seen.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ps, err = s.Prices(ctx, "2026-11-01", "2026-11-03")
	if err != nil || len(ps) != 1 || len(ps[0].Prices) != 3 {
		t.Fatalf("after prune %+v %v", ps, err)
	}
	// A later snapshot with fewer prices (listings partly pruned) keeps the fuller one.
	l := &Listing{Seller: "late", Kind: "sell", Meal: "lunch", MealDate: "2026-11-02", Mess: "kadamba", Price: 40, Qty: 1, Text: "x", Norm: "x", FirstSeen: seen, LastSeen: seen.Add(2 * time.Hour)}
	if err := s.CreateListing(ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := s.SnapshotPrices(ctx, "2026-11-01"); err != nil {
		t.Fatal(err)
	}
	if ps, _ = s.Prices(ctx, "2026-11-01", "2026-11-03"); len(ps[0].Prices) != 3 {
		t.Fatalf("partial snapshot replaced a fuller one: %+v", ps)
	}
}

func TestDeliveryRecords(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 11, 2, 19, 0, 0, 0, time.UTC)
	d := Delivery{Date: "2026-11-02", Meal: "dinner", Peer: "91999@s.whatsapp.net", Amount: "55.00", ImageHash: "h", ArmedAt: at}
	if _, err := s.ArmDelivery(ctx, d); err != nil {
		t.Fatal(err)
	}
	// Resuming with the mess now known fills it in.
	d.Mess, d.Diet = "palash", "veg"
	if _, err := s.ArmDelivery(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkOfferSent(ctx, d, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchPeer(ctx, "123@lid", "91999@s.whatsapp.net", "Meera from DMs", false, at); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Deliveries(ctx)
	if err != nil || len(rs) != 1 || rs[0].BuyerName != "Meera from DMs" || rs[0].PeerAlt != "123@lid" || rs[0].Mess != "palash" || rs[0].State != "armed" {
		t.Fatalf("armed %+v %v", rs, err)
	}
	if ok, err := s.ClaimDelivery(ctx, d, Payment{ID: "p", At: at.Add(3 * time.Minute), BuyerName: "Meera"}, "m"); !ok || err != nil {
		t.Fatal("claim", err)
	}
	if err := s.FinishDelivery(ctx, d, "m"); err != nil {
		t.Fatal(err)
	}
	rs, _ = s.Deliveries(ctx)
	r := rs[0]
	if r.State != "sent" || r.BuyerName != "Meera" || !r.OfferAt.Equal(at.Add(time.Minute)) || !r.PaidAt.Equal(at.Add(3*time.Minute)) || r.SentAt.IsZero() {
		t.Fatalf("sent %+v", r)
	}
}

// A market_prices from before diets is rebuilt from the live listings, with
// veg and non-veg asks kept apart and posts that don't say counted as veg.
func TestPricesMigrateToDiets(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "d.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for i, l := range []struct {
		diet  string
		price int
	}{{"", 20}, {"veg", 25}, {"nonveg", 70}, {"nonveg", 80}} {
		if err := s.CreateListing(ctx, &Listing{Seller: string(rune('a' + i)), Kind: "sell", Meal: "dinner", MealDate: "2026-11-02", Mess: "kadamba", Diet: l.diet, Price: l.price, Qty: 1, Text: "x", Norm: "x", FirstSeen: seen, LastSeen: seen}); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{`DROP TABLE market_prices`,
		`CREATE TABLE market_prices (date TEXT NOT NULL, meal TEXT NOT NULL, mess TEXT NOT NULL, prices TEXT NOT NULL, n INTEGER NOT NULL, PRIMARY KEY (date, meal, mess))`,
		`INSERT INTO market_prices VALUES ('2026-11-02','dinner','kadamba','20,25,70,80,90',5)`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ps, err := s.Prices(ctx, "2026-11-01", "2026-11-03")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range ps {
		got[p.Diet] = joinInts(p.Prices)
	}
	if len(got) != 2 || got["veg"] != "20,25" || got["nonveg"] != "70,80" {
		t.Fatalf("prices after migrating %+v", ps)
	}
}
