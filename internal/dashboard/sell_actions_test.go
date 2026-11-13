package dashboard

import (
	"context"
	"testing"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
)

type operatorCall struct {
	id                int64
	buyer, name, note string
	price             int
	muted             bool
}
type dashboardOperator struct{ calls []operatorCall }

func (o *dashboardOperator) Start(context.Context, int64, string, string, int, int, string) (int64, error) {
	return 1, nil
}
func (o *dashboardOperator) Cancel(context.Context, int64) error { return nil }
func (o *dashboardOperator) MarkSold(_ context.Context, id int64, buyer, name string, price int, note string) error {
	o.calls = append(o.calls, operatorCall{id: id, buyer: buyer, name: name, price: price, note: note})
	return nil
}
func (o *dashboardOperator) AddBuyer(_ context.Context, id int64, buyer, name string) error {
	o.calls = append(o.calls, operatorCall{id: id, buyer: buyer, name: name})
	return nil
}
func (o *dashboardOperator) MuteBuyer(_ context.Context, id int64, buyer string, muted bool) error {
	o.calls = append(o.calls, operatorCall{id: id, buyer: buyer, muted: muted})
	return nil
}

func TestSellActionsAPI(t *testing.T) {
	cfg := config.Default()
	cfg.MCP.SellTasks = true
	st := newTestStore(t)
	srv := mcpserver.New(cfg, st, nil)
	op := &dashboardOperator{}
	srv.SetSellTasks(op)
	d := startDashboard(t, cfg, st, srv, NewHub())
	admin, member := d.as(t, accounts.Admin), d.as(t, accounts.Member)
	for _, action := range []string{"sold", "add", "mute", "unmute"} {
		in := map[string]any{"action": action, "id": 7, "buyer": "+911111111111", "name": "Friend", "price": 55, "note": "cash"}
		if res := member.send(t, "POST", "/api/sell-action", in); res.StatusCode != 403 {
			t.Fatalf("member %s = %d", action, res.StatusCode)
		}
		old := admin.csrf
		admin.csrf = ""
		if res := admin.send(t, "POST", "/api/sell-action", in); res.StatusCode != 403 {
			t.Fatalf("no CSRF %s = %d", action, res.StatusCode)
		}
		admin.csrf = old
		if res := admin.send(t, "POST", "/api/sell-action", in); res.StatusCode != 200 {
			t.Fatalf("admin %s = %d", action, res.StatusCode)
		}
	}
	if len(op.calls) != 4 || op.calls[0].price != 55 || op.calls[0].note != "cash" || op.calls[1].name != "Friend" || !op.calls[2].muted || op.calls[3].muted {
		t.Fatalf("calls = %+v", op.calls)
	}
	for _, c := range op.calls {
		if c.id != 7 || c.buyer != "+911111111111" {
			t.Fatalf("call = %+v", c)
		}
	}
	if res := admin.send(t, "POST", "/api/sell-action", map[string]any{"action": "bad"}); res.StatusCode != 400 {
		t.Fatalf("bad action = %d", res.StatusCode)
	}
	cfg.MCP.SellTasks = false
	// Live config without saved settings reads the config itself.
	if res := admin.send(t, "POST", "/api/sell-action", map[string]any{"action": "add"}); res.StatusCode != 400 {
		t.Fatalf("disabled = %d", res.StatusCode)
	}
}
