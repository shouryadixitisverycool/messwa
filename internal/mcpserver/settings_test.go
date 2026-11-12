package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/store"
)

// Turning sell tasks on or off on the settings page changes the tools a
// connected agent sees, without a restart.
func TestSellToolsFollowSettings(t *testing.T) {
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
	cfg.ApplySaved(nil)
	s := New(cfg, st, nil)
	s.SetAccounts(acc)
	s.SetSellTasks(&fakeSell{st: st})
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
	hasSell := func() bool {
		t.Helper()
		res, err := sess.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(res.Tools, func(tool *mcp.Tool) bool { return tool.Name == "start_sell_task" })
	}

	if hasSell() {
		t.Fatal("sell tools listed before they were turned on")
	}
	if err := s.SaveSettings(ctx, config.Settings{"mcp.sell_tasks": json.RawMessage(`true`)}, admin.ID); err != nil {
		t.Fatal(err)
	}
	if !hasSell() {
		t.Fatal("sell tools missing after turning them on")
	}
	if err := s.SaveSettings(ctx, config.Settings{}, admin.ID); err != nil {
		t.Fatal(err)
	}
	if hasSell() {
		t.Fatal("sell tools still listed after turning them off")
	}

	// A save from another process reaches run on its next reload.
	if err := st.SaveSettings(ctx, map[string]json.RawMessage{"mcp.sell_tasks": json.RawMessage(`true`)}, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.ReloadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if !hasSell() || !cfg.Live().MCP.SellTasks {
		t.Fatal("reload didn't pick up the other process's save")
	}
}
