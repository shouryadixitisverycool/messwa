package dashboard

import (
	"encoding/json"
	"testing"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
)

func TestSettingsAPI(t *testing.T) {
	st := newTestStore(t)
	cfg := config.Default()
	cfg.Groups = []string{"a@g.us", "b@g.us"}
	cfg.ApplySaved(nil)
	if err := st.SetGroupName(t.Context(), "a@g.us", "Mess Buy Sell"); err != nil {
		t.Fatal(err)
	}
	d := startDashboard(t, cfg, st, mcpserver.New(cfg, st, nil), NewHub())

	// Members get only the settings for their own WhatsApp.
	member := d.as(t, accounts.Member)
	var mine settingsState
	if res := member.get(t, "/api/settings", &mine); res.StatusCode != 200 {
		t.Fatalf("member GET = %d", res.StatusCode)
	}
	for k := range mine.Values {
		if !perUserSetting(k) {
			t.Errorf("member sees %s", k)
		}
	}
	if _, ok := mine.Values["sell.show_price"]; !ok {
		t.Error("member doesn't see their selling settings")
	}
	if res := member.send(t, "PUT", "/api/settings", map[string]any{"values": map[string]any{"dedupe.window": "2h"}}); res.StatusCode != 403 {
		t.Fatalf("member PUT of a shared setting = %d, want 403", res.StatusCode)
	}
	if res := member.get(t, "/settings.html", nil); res.StatusCode != 200 {
		t.Fatalf("member settings page = %d, want 200", res.StatusCode)
	}

	admin := d.as(t, accounts.Admin)
	var got settingsState
	if res := admin.get(t, "/api/settings", &got); res.StatusCode != 200 {
		t.Fatalf("admin GET = %d", res.StatusCode)
	}
	if got.Values["sell.show_price"] != true || len(got.Saved) != 0 || len(got.Groups) != 2 || got.Groups[0].Name != "Mess Buy Sell" || len(got.Meals) != 4 {
		t.Fatalf("state = %+v", got)
	}

	put := func(values map[string]any) (int, settingsState, string) {
		res := admin.send(t, "PUT", "/api/settings", map[string]any{"values": values})
		defer res.Body.Close()
		var out settingsState
		var e struct{ Error string }
		if res.StatusCode == 200 {
			json.NewDecoder(res.Body).Decode(&out)
		} else {
			json.NewDecoder(res.Body).Decode(&e)
		}
		return res.StatusCode, out, e.Error
	}
	code, _, msg := put(map[string]any{"sell.post_gap": 1})
	if code != 400 || msg == "" {
		t.Fatalf("bad post_gap: %d %q", code, msg)
	}
	if cfg.Live().Sell.PostGap.Seconds() != 15 {
		t.Fatal("a refused save changed the live config")
	}

	code, got, msg = put(map[string]any{"sell.show_price": false, "messes.veg_only": map[string]bool{"bakul": true}, "sales.monthly_fee": 3000})
	if code != 200 {
		t.Fatalf("save: %d %s", code, msg)
	}
	if cfg.Live().Sell.ShowPrice || cfg.Live().Sales.MonthlyFee != 3000 {
		t.Fatal("save didn't reach the live config")
	}
	if len(got.Saved) != 3 || got.Values["sell.show_price"] != false || got.Defaults["sell.show_price"] != true {
		t.Fatalf("after save = %+v", got)
	}
	// veg_only waits for a restart; the running config keeps the old value.
	if len(got.Restart) != 1 || got.Restart[0] != "messes.veg_only" || cfg.VegOnly("bakul") {
		t.Fatalf("restart = %v, running bakul veg_only = %v", got.Restart, cfg.VegOnly("bakul"))
	}

	// An empty save puts everything back to config.toml.
	if code, got, _ = put(map[string]any{}); code != 200 || len(got.Saved) != 0 || !cfg.Live().Sell.ShowPrice {
		t.Fatalf("reset: %d %+v", code, got)
	}
}
