package mcpserver

import (
	"context"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type linkedTestDevice struct{}

func (linkedTestDevice) Linked() bool                 { return true }
func (linkedTestDevice) Status() any                  { return nil }
func (linkedTestDevice) Link(context.Context) error   { return nil }
func (linkedTestDevice) Unlink(context.Context) error { return nil }

func TestHTTPRoutesPrivateDataAndActionsToTokenOwner(t *testing.T) {
	ctx := t.Context()
	accounts.CheapHashing()
	dir := t.TempDir()
	root, err := store.Open(filepath.Join(dir, "root.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	acc, err := accounts.New(root, "")
	if err != nil {
		t.Fatal(err)
	}
	servers := map[int64]*Server{}
	tokens := []string{}
	stores := []*store.Store{}
	for _, name := range []string{"alpha", "beta"} {
		u, err := acc.Add(ctx, name, "long password", accounts.Member)
		if err != nil {
			t.Fatal(err)
		}
		token, _, err := acc.CreateToken(ctx, u.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
		st, err := store.OpenPrivate(filepath.Join(dir, name+".db"), root)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		stores = append(stores, st)
		if err := st.TouchPeer(ctx, "123@s.whatsapp.net", "", name, false, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := st.AddDM(ctx, "123@s.whatsapp.net", "same", false, time.Now(), name+" secret"); err != nil {
			t.Fatal(err)
		}
		cfg := config.Default()
		cfg.MCP.SellTasks = true
		srv := New(cfg, st, nil)
		srv.SetAccounts(acc)
		srv.BindAccount(u.ID, linkedTestDevice{})
		srv.SetSellTasks(&fakeSell{st: st})
		servers[u.ID] = srv
	}
	betaID, err := stores[1].CreateSellTask(ctx, store.SellTask{Date: "2026-11-13", Meal: "lunch", UserID: 2, MinPaise: 100, MaxPaise: 100, CreatedAt: time.Now(), EndsAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Default(), root, nil)
	srv.SetAccounts(acc)
	srv.AccountServer = func(_ context.Context, id int64) (*Server, error) {
		s := servers[id]
		if s == nil {
			return nil, fmt.Errorf("no account")
		}
		return s, nil
	}
	handler, err := srv.HTTPHandler("", false)
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := httptest.NewServer(handler)
	defer httpSrv.Close()
	for i, token := range tokens {
		sess, err := mcp.NewClient(&mcp.Implementation{Name: "scope-test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpSrv.URL, HTTPClient: &http.Client{Transport: headerRT{"Bearer " + token}}, DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		own, other := []string{"alpha", "beta"}[i], []string{"beta", "alpha"}[i]
		result, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "read_unsaved_chat", Arguments: map[string]any{"jid": "123@s.whatsapp.net"}})
		if err != nil {
			t.Fatal(err)
		}
		text := fmt.Sprint(result.Content)
		for _, c := range result.Content {
			if v, ok := c.(*mcp.TextContent); ok {
				text = v.Text
			}
		}
		if result.IsError || !strings.Contains(text, own+" secret") || strings.Contains(text, other+" secret") {
			t.Fatalf("DM scope: %v %s", result.IsError, text)
		}
		if i == 0 {
			res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "cancel_sell_task", Arguments: map[string]any{"id": betaID}})
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError {
				t.Fatal("other account task appeared to cancel")
			}
			task, err := stores[1].GetSellTask(ctx, betaID)
			if err != nil || task.State != store.TaskActive {
				t.Fatalf("other account task changed: %+v %v", task, err)
			}
		}
		sess.Close()
	}
}
