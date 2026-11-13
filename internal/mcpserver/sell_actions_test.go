package mcpserver

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"messwa/internal/config"
	"messwa/internal/store"
)

type actionSell struct {
	fakeSell
	action            string
	id                int64
	buyer, name, note string
	price             int
	muted             bool
	err               error
}

func (f *actionSell) MarkSold(_ context.Context, id int64, buyer, name string, price int, note string) error {
	f.action, f.id, f.buyer, f.name, f.price, f.note = "sold", id, buyer, name, price, note
	return f.err
}
func (f *actionSell) AddBuyer(_ context.Context, id int64, buyer, name string) error {
	f.action, f.id, f.buyer, f.name = "add", id, buyer, name
	return f.err
}
func (f *actionSell) MuteBuyer(_ context.Context, id int64, buyer string, muted bool) error {
	f.action, f.id, f.buyer, f.muted = "mute", id, buyer, muted
	return f.err
}

func TestSellActionTools(t *testing.T) {
	ctx := t.Context()
	st, err := store.Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	cfg.MCP.SellTasks = true
	op := &actionSell{fakeSell: fakeSell{st: st}}
	id, err := op.Start(ctx, 0, "2026-11-13", "lunch", 30, 60, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(cfg, st, nil)
	srv.SetSellTasks(op)
	ct, transport := mcp.NewInMemoryTransports()
	serverSession, err := srv.MCP().Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, name := range []string{"mark_sell_task_sold", "add_sell_task_buyer", "mute_sell_task_buyer", "unmute_sell_task_buyer"} {
		args := map[string]any{"id": id, "buyer": "+911111111111"}
		if name == "mark_sell_task_sold" || name == "add_sell_task_buyer" {
			args["name"] = "Friend"
		}
		if name == "mark_sell_task_sold" {
			args["price"], args["note"] = 55, "cash"
		}
		result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("%s: %+v %v", name, result, err)
		}
		if op.id != id || op.buyer != "+911111111111" {
			t.Fatalf("operation = %+v", op)
		}
		switch name {
		case "mark_sell_task_sold":
			if op.action != "sold" || op.name != "Friend" || op.price != 55 || op.note != "cash" {
				t.Fatalf("sold = %+v", op)
			}
		case "add_sell_task_buyer":
			if op.action != "add" || op.name != "Friend" {
				t.Fatalf("add = %+v", op)
			}
		default:
			if op.action != "mute" || op.muted != (name == "mute_sell_task_buyer") {
				t.Fatalf("mute = %+v", op)
			}
		}
		op.err = errors.New("task ended")
		result, err = client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || !result.IsError {
			t.Fatalf("%s did not report manager error: %+v %v", name, result, err)
		}
		op.err = nil
	}
}
