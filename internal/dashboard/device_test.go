package dashboard

import (
	"context"
	"encoding/json"
	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
	"messwa/internal/store"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testDevice struct {
	name           string
	links, unlinks int
}

func (d *testDevice) Linked() bool { return true }
func (d *testDevice) LinkCode(context.Context, string) (string, error) {
	return "", nil
}
func (d *testDevice) Status() any                  { return map[string]string{"account": d.name} }
func (d *testDevice) Link(context.Context) error   { d.links++; return nil }
func (d *testDevice) Unlink(context.Context) error { d.unlinks++; return nil }

func TestAccountDashboardScopesSalesDevicesAndSettings(t *testing.T) {
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
	devices := map[int64]*testDevice{}
	servers := map[int64]*mcpserver.Server{}
	lookup := func(ctx context.Context, id int64) (*mcpserver.Server, error) { return servers[id], nil }
	d := &testDash{acc: acc}
	d.Server = httptest.NewServer(AccountHandler(cfg, rootSrv, NewHub(), acc, lookup))
	defer d.Close()
	// Sign-in uses an unauthenticated route; subsequent requests select the private server.
	admin := d.as(t, accounts.Admin)
	member := d.as(t, accounts.Member)
	for _, client := range []*testClient{admin, member} {
		u := client.user
		st, err := store.OpenPrivate(filepath.Join(dir, u.Name+".db"), root)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if _, err := st.RecordManualSale(ctx, store.ManualSale{Date: time.Now().In(cfg.Location()).Format("2006-01-02"), Meal: "lunch", Peer: u.Name + "-private-buyer", Amount: "30.00", At: time.Now()}, false); err != nil {
			t.Fatal(err)
		}
		c := config.Default()
		srv := mcpserver.New(c, st, nil)
		srv.SetAccounts(acc)
		device := &testDevice{name: u.Name}
		srv.BindAccount(u.ID, device)
		servers[u.ID] = srv
		devices[u.ID] = device
	}
	for _, client := range []*testClient{admin, member} {
		var status map[string]string
		if res := client.get(t, "/api/whatsapp?user_id=999", &status); res.StatusCode != 200 || status["account"] != client.user.Name {
			t.Fatalf("device scope: %d %v", res.StatusCode, status)
		}
		res := client.send(t, "POST", "/api/whatsapp?user_id=999", map[string]any{})
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatal(res.StatusCode)
		}
		if devices[client.user.ID].links != 1 {
			t.Fatal("wrong device linked")
		}
		var sales map[string]any
		if res := client.get(t, "/api/sales", &sales); res.StatusCode != 200 {
			t.Fatal(res.StatusCode)
		}
		text := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(toJSON(t, sales)), "\\n", ""), "\\t", ""))
		other := admin
		if client == admin {
			other = member
		}
		if !strings.Contains(text, client.user.Name+"-private-buyer") || strings.Contains(text, other.user.Name+"-private-buyer") {
			t.Fatalf("sales leaked: %s", text)
		}
		old := client.csrf
		client.csrf = ""
		res = client.send(t, "DELETE", "/api/whatsapp", nil)
		res.Body.Close()
		client.csrf = old
		if res.StatusCode != 403 || devices[client.user.ID].unlinks != 0 {
			t.Fatal("unlink bypassed CSRF")
		}
		res = client.send(t, "PUT", "/api/settings", map[string]any{"values": map[string]any{"sell.show_price": false}})
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatal(res.StatusCode)
		}
		var got struct{ Values map[string]any }
		client.get(t, "/api/settings", &got)
		if _, ok := got.Values["sold.admins_count"]; ok == (client == member) {
			t.Errorf("%s sees market rules: %v", client.user.Name, ok)
		}
		if got.Values["sell.show_price"] != false {
			t.Errorf("%s's show_price didn't stick: %v", client.user.Name, got.Values["sell.show_price"])
		}
		res = client.send(t, "PUT", "/api/settings", map[string]any{"values": map[string]any{"sold.admins_count": true}})
		res.Body.Close()
		if client == member && res.StatusCode != 403 {
			t.Fatal("member changed market rules")
		}
	}
	if res := member.get(t, "/api/insights", nil); res.StatusCode != 403 {
		t.Fatal("member read insights")
	}
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
