package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/store"
)

// fakeSell records what the tools asked for and creates the task rows a
// real manager would.
type fakeSell struct {
	st      *store.Store
	started []int64 // user IDs
	cancels []int64
}

func (f *fakeSell) Start(ctx context.Context, userID int64, date, meal string, minRupees, maxRupees int, handoff string) (int64, error) {
	f.started = append(f.started, userID)
	return f.st.CreateSellTask(ctx, store.SellTask{Date: date, Meal: meal, MinPaise: int64(minRupees) * 100, MaxPaise: int64(maxRupees) * 100,
		UserID: userID, Handoff: handoff, CreatedAt: time.Now(), EndsAt: time.Now().Add(time.Hour)})
}

func (f *fakeSell) Cancel(ctx context.Context, id int64) error {
	f.cancels = append(f.cancels, id)
	_, err := f.st.EndSellTask(ctx, id, store.TaskCancelled, "cancelled", time.Now())
	return err
}

func TestSellToolsNeedSettingAndAdmin(t *testing.T) {
	ctx := context.Background()
	t.Setenv(accounts.LegacyKeyEnv, "")
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	accounts.CheapHashing()
	acc, _ := accounts.New(st, "")
	admin, _ := acc.Add(ctx, "boss", "long enough pw", accounts.Admin)
	member, _ := acc.Add(ctx, "asha", "long enough pw", accounts.Member)
	adminTok, _, _ := acc.CreateToken(ctx, admin.ID, "")
	memberTok, _, _ := acc.CreateToken(ctx, member.ID, "")

	connect := func(cfg *config.Config, sell SellTasks, auth string) *mcp.ClientSession {
		t.Helper()
		s := New(cfg, st, nil)
		s.SetAccounts(acc)
		if sell != nil {
			s.SetSellTasks(sell)
		}
		h, err := s.HTTPHandler("", false)
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		tr := &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: headerRT{"Bearer " + auth}}, MaxRetries: -1, DisableStandaloneSSE: true}
		sess, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, tr, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { sess.Close() })
		return sess
	}
	tools := func(sess *mcp.ClientSession) []string {
		res, err := sess.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tool := range res.Tools {
			names = append(names, tool.Name)
		}
		return names
	}
	sell := &fakeSell{st: st}

	// Off by default: no tools and no mention in the instructions, even
	// with a manager attached.
	off := connect(config.Default(), sell, adminTok)
	if names := tools(off); slices.Contains(names, "start_sell_task") {
		t.Fatalf("sell tools listed with the setting off: %v", names)
	}
	if strings.Contains(off.InitializeResult().Instructions, "start_sell_task") {
		t.Fatal("instructions mention sell tasks with the setting off")
	}

	cfg := config.Default()
	cfg.MCP.SellTasks = true
	on := connect(cfg, sell, adminTok)
	for _, want := range []string{"start_sell_task", "sell_task_status", "cancel_sell_task"} {
		if !slices.Contains(tools(on), want) {
			t.Fatalf("%s missing with the setting on", want)
		}
	}
	if !strings.Contains(on.InitializeResult().Instructions, "never quotes, agrees or accepts less than min_price") {
		t.Fatal("instructions don't explain sell tasks")
	}
	start := map[string]any{"date": "2026-11-11", "meal": "lunch", "min_price": 30, "max_price": 60}

	member1 := connect(cfg, sell, memberTok)
	for _, name := range []string{"start_sell_task", "sell_task_status", "cancel_sell_task"} {
		res, err := member1.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"id": 1, "date": "2026-11-11", "meal": "lunch", "min_price": 30, "max_price": 60}})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatalf("member could call %s", name)
		}
	}
	if len(sell.started) != 0 {
		t.Fatal("a member's call reached the manager")
	}

	res, err := on.CallTool(ctx, &mcp.CallToolParams{Name: "start_sell_task", Arguments: start})
	if err != nil || res.IsError {
		t.Fatalf("admin start: %v %+v", err, res)
	}
	var task SellTaskOut
	b, _ := json.Marshal(res.StructuredContent)
	json.Unmarshal(b, &task)
	if task.ID == 0 || task.State != store.TaskActive || task.MinPrice != "30.00" || task.MaxPrice != "60.00" || len(sell.started) != 1 || sell.started[0] != admin.ID {
		t.Fatalf("started %+v as %v", task, sell.started)
	}
	res, err = on.CallTool(ctx, &mcp.CallToolParams{Name: "sell_task_status"})
	if err != nil || res.IsError {
		t.Fatalf("status: %v %+v", err, res)
	}
	var list SellTasksOut
	b, _ = json.Marshal(res.StructuredContent)
	json.Unmarshal(b, &list)
	if len(list.Tasks) != 1 || list.Tasks[0].ID != task.ID {
		t.Fatalf("status = %+v", list)
	}
	// A buyer the negotiation parked shows under needs_you with their chat.
	parked := "913333333333@s.whatsapp.net"
	st.AddSellTaskBuyer(ctx, task.ID, parked, parked, time.Now())
	if ok, err := st.ClaimNegotiationMessage(ctx, task.ID, parked, 0, store.Negotiation{Ask: 5000, BotMsgs: 1, LastOffer: 2000, Quoted: []int64{6000, 5000}, StartedAt: time.Now()}); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := st.ParkSellTaskBuyer(ctx, task.ID, parked, "sent messages the bot couldn't read"); err != nil {
		t.Fatal(err)
	}
	res, err = on.CallTool(ctx, &mcp.CallToolParams{Name: "sell_task_status", Arguments: map[string]any{"id": task.ID}})
	if err != nil || res.IsError {
		t.Fatalf("status: %v %+v", err, res)
	}
	b, _ = json.Marshal(res.StructuredContent)
	json.Unmarshal(b, &list)
	if nu := list.Tasks[0].NeedsYou; len(nu) != 1 || nu[0].ChatLink != "https://wa.me/913333333333" || nu[0].Ask != "50.00" ||
		nu[0].LastOffer != "20.00" || nu[0].Why == "" || list.Tasks[0].Buyers[0].State != store.BuyerParked {
		t.Fatalf("needs_you = %+v, buyers = %+v", nu, list.Tasks[0].Buyers)
	}

	res, err = on.CallTool(ctx, &mcp.CallToolParams{Name: "cancel_sell_task", Arguments: map[string]any{"id": task.ID}})
	if err != nil || res.IsError {
		t.Fatalf("cancel: %v %+v", err, res)
	}
	b, _ = json.Marshal(res.StructuredContent)
	json.Unmarshal(b, &task)
	if task.State != store.TaskCancelled || len(sell.cancels) != 1 {
		t.Fatalf("after cancel %+v", task)
	}

	// A task started with handoff on_reply shows who to message once it
	// hands off.
	start["handoff"] = "on_reply"
	res, err = on.CallTool(ctx, &mcp.CallToolParams{Name: "start_sell_task", Arguments: start})
	if err != nil || res.IsError {
		t.Fatalf("handoff start: %v %+v", err, res)
	}
	b, _ = json.Marshal(res.StructuredContent)
	json.Unmarshal(b, &task)
	if task.Handoff != "on_reply" {
		t.Fatalf("handoff task = %+v", task)
	}
	buyer, other := "911111111111@s.whatsapp.net", "912222222222@s.whatsapp.net"
	for _, p := range []string{buyer, other} {
		if _, err := st.AddSellTaskBuyer(ctx, task.ID, p, p, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	d := store.Delivery{Date: "2026-11-11", Meal: "lunch", Peer: buyer, Amount: "60.00", ImageHash: "portal:x", ArmedAt: time.Now()}
	if err := st.HandOffTaskDelivery(ctx, task.ID, d, "Asha"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MoveSellTaskBuyer(ctx, task.ID, buyer, store.BuyerQueued, store.BuyerHandedOff, "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EndSellTask(ctx, task.ID, store.TaskHandedOff, "handed off", time.Now()); err != nil {
		t.Fatal(err)
	}
	res, err = on.CallTool(ctx, &mcp.CallToolParams{Name: "sell_task_status", Arguments: map[string]any{"id": task.ID}})
	if err != nil || res.IsError {
		t.Fatalf("handoff status: %v %+v", err, res)
	}
	b, _ = json.Marshal(res.StructuredContent)
	json.Unmarshal(b, &list)
	h := list.Tasks[0].HandedOff
	if h == nil || h.Buyer.Name != "Asha" || h.Buyer.Phone != "+911111111111" || h.ChatLink != "https://wa.me/911111111111" || h.Asked != "60.00" ||
		len(h.Others) != 1 || h.Others[0].Phone != "+912222222222" || !strings.Contains(h.Finish, "-peer "+buyer) {
		t.Fatalf("handed_off = %+v", h)
	}
}
