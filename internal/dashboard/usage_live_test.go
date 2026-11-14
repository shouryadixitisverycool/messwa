package dashboard

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
	"messwa/internal/store"
)

func TestUsageLiveCountsBuyersWithoutNamingThem(t *testing.T) {
	ctx := t.Context()
	accounts.CheapHashing()
	dir := t.TempDir()
	cfg := config.Default()
	root, err := store.Open(filepath.Join(dir, "root.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	acc, err := accounts.New(root, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	rootSrv := mcpserver.New(cfg, root, nil)
	rootSrv.SetAccounts(acc)
	servers := map[int64]*mcpserver.Server{}
	lookup := func(ctx context.Context, id int64) (*mcpserver.Server, error) { return servers[id], nil }
	d := &testDash{acc: acc}
	d.Server = httptest.NewServer(AccountHandler(cfg, rootSrv, NewHub(), acc, lookup))
	defer d.Close()
	admin := d.as(t, accounts.Admin)
	member := d.as(t, accounts.Member)

	now := time.Now()
	today := now.In(cfg.Location()).Format("2006-01-02")
	for _, c := range []*testClient{admin, member} {
		st, err := store.OpenPrivate(filepath.Join(dir, c.user.Name+".db"), root)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		srv := mcpserver.New(config.Default(), st, nil)
		srv.SetAccounts(acc)
		srv.BindAccount(c.user.ID, &testDevice{name: c.user.Name})
		servers[c.user.ID] = srv
		if c != member {
			continue
		}
		id, err := st.CreateSellTask(ctx, store.SellTask{Date: today, Meal: "lunch", MinPaise: 3000, MaxPaise: 4000, UserID: c.user.ID,
			Mess: "kadamba", Diet: "veg", Handoff: "never", State: store.TaskActive, CreatedAt: now, EndsAt: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.ClaimOfferPost(ctx, id, "123@g.us", "OFFER1", 0, now); err != nil {
			t.Fatal(err)
		}
		if err := st.SetOfferState(ctx, id, "OFFER1", store.OfferPosted); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"919876543210@s.whatsapp.net", "919000000001@s.whatsapp.net"} {
			if _, err := st.AddSellTaskBuyer(ctx, id, p, "", now); err != nil {
				t.Fatal(err)
			}
		}
		if ok, err := st.MoveSellTaskBuyer(ctx, id, "919876543210@s.whatsapp.net", store.BuyerQueued, store.BuyerOffered, "40.00", now.Add(5*time.Minute)); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}

	for _, path := range []string{"/api/usage/live", "/api/usage/events"} {
		res, err := http.Get(d.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s signed out: %d", path, res.StatusCode)
		}
		if res := member.get(t, path, nil); res.StatusCode != http.StatusForbidden {
			t.Fatalf("%s member: %d", path, res.StatusCode)
		}
	}

	raw, err := admin.Get(d.URL + "/api/usage/live")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(raw.Body)
	raw.Body.Close()
	if raw.StatusCode != 200 {
		t.Fatalf("admin: %d %s", raw.StatusCode, body)
	}
	for _, leak := range []string{"9876543210", "9000000001", "whatsapp.net", "g.us"} {
		if strings.Contains(string(body), leak) {
			t.Fatalf("live leaked %q: %s", leak, body)
		}
	}
	var st liveState
	admin.get(t, "/api/usage/live", &st)
	if len(st.Tasks) != 1 {
		t.Fatalf("tasks: %+v", st.Tasks)
	}
	lt := st.Tasks[0]
	if lt.User != member.user.Name || lt.Groups != 1 || lt.InLine != 1 || lt.Paying != 1 || lt.PayUntil == 0 || lt.Max != 40 || !lt.Linked {
		t.Fatalf("task: %+v", lt)
	}
}
