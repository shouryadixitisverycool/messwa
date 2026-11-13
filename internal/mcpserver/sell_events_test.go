package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/store"
)

func TestSellTaskEventsWindow(t *testing.T) {
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
	tok, _, _ := acc.CreateToken(ctx, admin.ID, "")

	cfg := config.Default()
	cfg.MCP.SellTasks = true
	s := New(cfg, st, nil)
	s.SetAccounts(acc)
	s.SetSellTasks(&fakeSell{st: st})
	loc := s.sched.Location()
	// The dinner test run: 12 Nov, two tasks, the first restarted as the second.
	at := func(hm string) time.Time {
		tm, _ := time.ParseInLocation("2006-01-02 15:04:05", "2026-11-12 "+hm, loc)
		return tm
	}
	s.now = func() time.Time { return at("21:00:00") }
	first, _ := st.CreateSellTask(ctx, store.SellTask{Date: "2026-11-12", Meal: "dinner", MinPaise: 5000, MaxPaise: 7000, UserID: admin.ID, CreatedAt: at("19:45:49"), EndsAt: at("21:30:00")})
	if _, err := st.EndSellTask(ctx, first, store.TaskCancelled, "restarted", at("20:03:50")); err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateSellTask(ctx, store.SellTask{Date: "2026-11-12", Meal: "dinner", MinPaise: 5000, MaxPaise: 7000, UserID: admin.ID, CreatedAt: at("20:03:56"), EndsAt: at("21:30:00")})
	if err != nil {
		t.Fatal(err)
	}
	group, buyer := "120363416069274282@g.us", "919999999999@s.whatsapp.net"
	st.SetGroupName(ctx, group, "Mess Buy Sell @ IIITH - 2")
	for _, e := range []store.SellEvent{
		{TaskID: first, At: at("19:45:55"), Peer: group, Kind: store.EventPosted},
		{TaskID: first, At: at("19:51:42"), Peer: buyer, Kind: store.EventJoined},
		{TaskID: first, At: at("19:51:50"), Peer: buyer, Kind: store.EventRead, N: 4000, Text: "offer"},
		{TaskID: second, At: at("20:03:56"), Peer: group, Kind: store.EventPostFailed, Text: "server returned error 479"},
		{TaskID: second, At: at("20:10:00"), Peer: group, Kind: store.EventTicked, N: 2},
	} {
		if err := st.AddSellTaskEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	h, err := s.HTTPHandler("", false)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	tr := &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: headerRT{"Bearer " + tok}}, MaxRetries: -1, DisableStandaloneSSE: true}
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	call := func(args map[string]any) (SellEventsOut, *mcp.CallToolResult) {
		t.Helper()
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "sell_task_events", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		var out SellEventsOut
		b, _ := json.Marshal(res.StructuredContent)
		json.Unmarshal(b, &out)
		return out, res
	}

	// A window across both tasks, given as clock times today.
	out, res := call(map[string]any{"since": "19:51", "until": "20:05"})
	if res.IsError {
		t.Fatalf("window: %+v", res.Content)
	}
	if len(out.Events) != 3 || out.Events[0].Kind != store.EventJoined || out.Events[0].At != "2026-11-12 19:51:42 IST" ||
		out.Events[1].Amount != "40.00" || out.Events[1].Buyer == nil || out.Events[1].Buyer.Phone != "+919999999999" ||
		out.Events[2].Group != "Mess Buy Sell @ IIITH - 2" || out.Events[2].Text != "server returned error 479" {
		t.Fatalf("window events = %+v", out.Events)
	}
	if len(out.Tasks) != 2 || out.Tasks[0].ID != first || out.Tasks[1].ID != second || out.Tasks[0].State != store.TaskCancelled || out.Tasks[0].Ended != "2026-11-12 20:03:50 IST" || out.Tasks[1].State != store.TaskActive {
		t.Fatalf("window tasks = %+v", out.Tasks)
	}

	// One task, whole history, capped.
	out, _ = call(map[string]any{"id": second})
	if len(out.Events) != 2 || out.Events[1].Count != 2 || out.Events[1].Amount != "" || out.More {
		t.Fatalf("task events = %+v", out)
	}
	out, _ = call(map[string]any{"id": first, "limit": 2})
	if len(out.Events) != 2 || !out.More {
		t.Fatalf("limited = %+v", out)
	}

	// The default window is the last two hours before until.
	out, _ = call(map[string]any{"until": "2026-11-12T14:40:00Z"})
	if len(out.Events) != 4 || out.Since != "2026-11-12 18:10:00 IST" {
		t.Fatalf("default window = %+v", out)
	}

	if _, res = call(map[string]any{"since": "dinner"}); !res.IsError {
		t.Fatal("an unreadable time was accepted")
	}
	if _, res = call(map[string]any{"since": "20:00", "until": "19:00"}); !res.IsError {
		t.Fatal("an empty window was accepted")
	}
}
