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

func TestUsageIsAdminOnlyAndHidesBuyers(t *testing.T) {
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
	today := time.Now().In(cfg.Location()).Format("2006-01-02")
	for _, c := range []*testClient{admin, member} {
		st, err := store.OpenPrivate(filepath.Join(dir, c.user.Name+".db"), root)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if _, err := st.RecordManualSale(ctx, store.ManualSale{Date: today, Meal: "lunch", Peer: "919876543210@s.whatsapp.net", BuyerName: c.user.Name + "-buyer-name", Amount: "30.00", At: time.Now()}, false); err != nil {
			t.Fatal(err)
		}
		srv := mcpserver.New(config.Default(), st, nil)
		srv.SetAccounts(acc)
		srv.BindAccount(c.user.ID, &testDevice{name: c.user.Name})
		servers[c.user.ID] = srv
	}

	res, err := http.Get(d.URL + "/api/usage")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", res.StatusCode)
	}
	if res := member.get(t, "/api/usage", nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("member: %d", res.StatusCode)
	}

	raw, err := admin.Get(d.URL + "/api/usage")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Body.Close()
	body, _ := io.ReadAll(raw.Body)
	if raw.StatusCode != 200 {
		t.Fatalf("admin: %d %s", raw.StatusCode, body)
	}
	text := string(body)
	for _, leak := range []string{"buyer-name", "9876543210", "whatsapp.net"} {
		if strings.Contains(text, leak) {
			t.Fatalf("usage leaked %q: %s", leak, text)
		}
	}

	var st usageState
	if admin.get(t, "/api/usage", &st); len(st.Users) != 2 {
		t.Fatalf("users: %+v", st.Users)
	}
	for _, u := range st.Users {
		if !u.Linked || !u.EverSold || len(u.Sales) != 1 || u.Sales[0].Amount != 30 {
			t.Fatalf("%s: %+v", u.Name, u)
		}
	}
}
