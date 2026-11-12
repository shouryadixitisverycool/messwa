package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/ingest"
	"messwa/internal/mcpserver"
	"messwa/internal/store"
)

func TestHandler(t *testing.T) {
	cfg := config.Default()
	cfg.Groups = []string{"g1@g.us"}
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in := ingest.New(cfg, st)
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, cfg.Location())
	if err := in.Post(context.Background(), ingest.GroupPost{Chat: "g1@g.us", ID: "a", Sender: "911@s.whatsapp.net", TS: now, Text: "Sell palash lunch @25 <script>"}); err != nil {
		t.Fatal(err)
	}
	if err := in.Post(context.Background(), ingest.GroupPost{Chat: "g1@g.us", ID: "b", Sender: "912@s.whatsapp.net", TS: now, Text: "Selling kadamba non veg lunch 60"}); err != nil {
		t.Fatal(err)
	}
	ms := mcpserver.New(cfg, st, nil)
	ms.SetClock(func() time.Time { return now.Add(time.Minute) })
	srv := startDashboard(t, cfg, st, ms, NewHub())
	c := srv.as(t, accounts.Member)

	res, err := c.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var s state
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(s.Listings) != 2 {
		t.Fatalf("listings: %+v", s.Listings)
	}
	for _, l := range s.Listings {
		if l.Mess == "palash" && (l.Diet != "veg" || *l.Price != 25) {
			t.Errorf("palash listing: %+v", l)
		}
	}
	diets := map[string]string{}
	for _, p := range s.Market.Posts {
		diets[p.Mess] = p.Diet
	}
	if diets["palash"] != "veg" || diets["kadamba"] != "nonveg" {
		t.Errorf("market diets: %v", diets)
	}
	if s.Current != nil || s.Next == nil || s.Next.Meal != "lunch" {
		t.Errorf("schedule: current %+v next %+v", s.Current, s.Next)
	}
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Error("missing CSP")
	}

	res, err = c.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Errorf("index: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}

	res = c.send(t, "POST", "/api/state", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST got %d, want 405", res.StatusCode)
	}
}

func TestEvents(t *testing.T) {
	cfg := config.Default()
	cfg.Groups = []string{"g1@g.us"}
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hub := NewHub()
	in := ingest.New(cfg, st)
	in.OnChange(hub.Notify)
	srv := startDashboard(t, cfg, st, mcpserver.New(cfg, st, nil), hub)
	c := srv.as(t, accounts.Member)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events", nil)
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	lines := bufio.NewScanner(res.Body)
	lines.Scan() // "retry: 3000", sent before any change

	post := store.LogEntry{Chat: "g1@g.us", ID: "a", Type: "post", Sender: "911@s.whatsapp.net", TS: time.Now(), Text: "Sell kadamba dinner 40"}
	if err := in.Handle(ctx, post); err != nil {
		t.Fatal(err)
	}
	for lines.Scan() {
		if lines.Text() == "event: change" {
			return
		}
	}
	t.Fatalf("no change event: %v", lines.Err())
}

// Your sell tasks reach the board for admins only, like the rest of the
// linked WhatsApp account.
func TestStateSalesAdminOnly(t *testing.T) {
	cfg := config.Default()
	cfg.Groups = []string{"g1@g.us"}
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 12, 13, 0, 0, 0, cfg.Location())
	if _, err := st.CreateSellTask(context.Background(), store.SellTask{Date: "2026-11-12", Meal: "lunch", MinPaise: 1500, MaxPaise: 3000,
		CreatedAt: now, EndsAt: now.Add(90 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	ms := mcpserver.New(cfg, st, nil)
	ms.SetClock(func() time.Time { return now.Add(time.Minute) })
	srv := startDashboard(t, cfg, st, ms, NewHub())
	for _, c := range []struct {
		role accounts.Role
		want int
	}{{accounts.Member, 0}, {accounts.Admin, 1}} {
		var s state
		srv.as(t, c.role).get(t, "/api/state", &s)
		if len(s.Sales) != c.want {
			t.Errorf("%s got %d sales, want %d", c.role, len(s.Sales), c.want)
		}
	}
}
