package dashboard

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/ingest"
	"messwa/internal/mcpserver"
	"messwa/internal/store"
)

func TestInsights(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Groups = []string{"g1@g.us"}
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in := ingest.New(cfg, st)
	posted := time.Date(2026, 11, 3, 11, 0, 0, 0, cfg.Location()) // a Tuesday
	for _, p := range []ingest.GroupPost{
		{Chat: "g1@g.us", ID: "a", Sender: "911@s.whatsapp.net", PushName: "Asha", TS: posted, Text: "Sell bakul lunch @25"},
		{Chat: "g1@g.us", ID: "b", Sender: "922@s.whatsapp.net", TS: posted, Text: "Selling kadamba dinner"},
	} {
		if err := in.Post(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := in.Reaction(ctx, "g1@g.us", "a", "911@s.whatsapp.net", "", "✅", posted.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	now := posted.Add(4 * time.Hour) // lunch is over, dinner isn't
	rule := store.SoldRule{Emojis: cfg.Sold.Emojis, ClosedEmojis: cfg.Sold.ClosedEmojis}
	if err := st.SnapshotFacts(ctx, rule, now); err != nil {
		t.Fatal(err)
	}
	ms := mcpserver.New(cfg, st, nil)
	ms.SetClock(func() time.Time { return now })
	d := startDashboard(t, cfg, st, ms, NewHub())

	// Analytics are for admins: no session gets 401, a member 403.
	if res, err := http.Get(d.URL + "/api/insights"); err != nil || res.StatusCode != 401 {
		t.Fatalf("signed out: %v %v", res, err)
	}
	if res, err := http.Get(d.URL + "/insights.html"); err != nil || res.Request.URL.Path != "/login.html" {
		t.Fatalf("signed out page should go to sign-in: %v %v", res, err)
	}
	m := d.as(t, accounts.Member)
	for _, p := range []string{"/api/insights", "/insights.html"} {
		if res := m.get(t, p, nil); res.StatusCode != 403 {
			t.Fatalf("member %s: %d", p, res.StatusCode)
		}
	}

	c := d.as(t, accounts.Admin)
	var s insightsState
	if res := c.get(t, "/api/insights?weeks=4", &s); res.StatusCode != 200 {
		t.Fatalf("admin: %d", res.StatusCode)
	}
	if s.First != "2026-11-03" || s.From != "2026-11-03" || s.Today != "2026-11-03" {
		t.Fatalf("dates first %q from %q today %q", s.First, s.From, s.Today)
	}
	if len(s.Rows) != 1 || len(s.Sellers) != 1 {
		t.Fatalf("rows %v sellers %v: want only the ended bakul lunch", s.Rows, s.Sellers)
	}
	row := map[string]any{}
	for i, c := range s.Cols {
		row[c] = s.Rows[0][i]
	}
	if row["mess"] != "bakul" || row["meal"] != "lunch" || row["price"] != 25.0 || row["sold"] != float64(posted.Add(5*time.Minute).Unix()) {
		t.Fatalf("row %v", row)
	}
	if s.Sellers[0].Name != "Asha" || s.Sellers[0].Phone != "+911" {
		t.Fatalf("seller %+v", s.Sellers[0])
	}
	if res := c.get(t, "/insights.html", nil); res.StatusCode != 200 {
		t.Fatalf("admin page: %d", res.StatusCode)
	}
}

// TestInsightsSeasons checks that a range stops at the season edge and that
// totals still cover every season.
func TestInsightsSeasons(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Groups = []string{"g1@g.us"}
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in := ingest.New(cfg, st)
	term := time.Date(2026, 12, 5, 11, 0, 0, 0, cfg.Location()) // monsoon term
	brk := time.Date(2026, 12, 9, 11, 0, 0, 0, cfg.Location())  // winter break
	for _, p := range []ingest.GroupPost{
		{Chat: "g1@g.us", ID: "a", Sender: "911@s.whatsapp.net", TS: term, Text: "Sell bakul lunch @25"},
		{Chat: "g1@g.us", ID: "b", Sender: "922@s.whatsapp.net", TS: term, Text: "Sell kadamba lunch @30"},
		{Chat: "g1@g.us", ID: "c", Sender: "933@s.whatsapp.net", TS: brk, Text: "Sell bakul lunch @50"},
	} {
		if err := in.Post(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := in.Reaction(ctx, "g1@g.us", "c", "933@s.whatsapp.net", "", "✅", brk.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	now := brk.Add(24 * time.Hour)
	rule := store.SoldRule{Emojis: cfg.Sold.Emojis, ClosedEmojis: cfg.Sold.ClosedEmojis}
	if err := st.SnapshotFacts(ctx, rule, now); err != nil {
		t.Fatal(err)
	}
	ms := mcpserver.New(cfg, st, nil)
	ms.SetClock(func() time.Time { return now })
	d := startDashboard(t, cfg, st, ms, NewHub())
	c := d.as(t, accounts.Admin)

	for _, tc := range []struct {
		q, season, from, to string
		rows                int
	}{
		{"weeks=4", "2026-12-08", "2026-12-08", "2026-12-10", 1},
		{"weeks=4&season=2026-08-01", "2026-08-01", "2026-12-05", "2026-12-07", 2},
		{"weeks=4&season=nope", "2026-12-08", "2026-12-08", "2026-12-10", 1},
		{"weeks=0&season=all", "all", "2026-12-05", "2026-12-10", 3},
	} {
		var s insightsState
		if res := c.get(t, "/api/insights?"+tc.q, &s); res.StatusCode != 200 {
			t.Fatalf("%s: %d", tc.q, res.StatusCode)
		}
		if s.Season != tc.season || s.From != tc.from || s.To != tc.to || len(s.Rows) != tc.rows {
			t.Errorf("%s: season %q %s..%s, %d rows", tc.q, s.Season, s.From, s.To, len(s.Rows))
		}
		if s.Current != "2026-12-08" || s.Next.Name != "Spring term" {
			t.Errorf("%s: current %q next %+v", tc.q, s.Current, s.Next)
		}
		if len(s.Seasons) != 2 || s.Seasons[0].Name != "Monsoon term" || s.Seasons[0].Days != 1 || s.Seasons[1].Days != 1 {
			t.Fatalf("%s: seasons %+v", tc.q, s.Seasons)
		}
		// Totals: season, mess, diet, offers, sold, wanted, ask_sum, asks.
		var offers, sold [2]float64
		for _, r := range s.Totals {
			i := int(r[0].(float64))
			offers[i] += r[3].(float64)
			sold[i] += r[4].(float64)
		}
		if offers != [2]float64{2, 1} || sold != [2]float64{0, 1} {
			t.Errorf("%s: totals offers %v sold %v", tc.q, offers, sold)
		}
	}
}
