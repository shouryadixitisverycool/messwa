package dashboard

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
	"messwa/internal/meals"
	"messwa/internal/wording"
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
	if cfg.Live().Sell.PostGap.Seconds() != 3 {
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

// An admin's meal times apply to every account at once; a member can't
// change them, and a member's own save leaves them alone.
func TestSettingsMealTimes(t *testing.T) {
	defer meals.SetShared(nil)
	st := newTestStore(t)
	cfg := config.Default()
	cfg.ApplySaved(nil)
	d := startDashboard(t, cfg, st, mcpserver.New(cfg, st, nil), NewHub())
	admin := d.as(t, accounts.Admin)
	member := d.as(t, accounts.Member)
	put := func(c *testClient, values map[string]any) int {
		res := c.send(t, "PUT", "/api/settings", map[string]any{"values": values})
		res.Body.Close()
		return res.StatusCode
	}
	bf := map[string]config.MealTime{"breakfast": {Starts: "06:30", Ends: "10:30"}}

	if code := put(admin, map[string]any{"meals.times": bf}); code != 200 {
		t.Fatalf("admin save = %d", code)
	}
	if ts := meals.Times(cfg); ts[0].Starts != "06:30" || ts[0].Ends != "10:30" {
		t.Fatalf("breakfast = %+v", ts[0])
	}
	var got settingsState
	admin.get(t, "/api/settings", &got)
	if !slices.Contains(got.Saved, "meals.times") || slices.Contains(got.Restart, "meals.times") {
		t.Fatalf("saved = %v, restart = %v", got.Saved, got.Restart)
	}

	overlap := map[string]config.MealTime{"breakfast": {Starts: "06:30", Ends: "13:00"}}
	if code := put(admin, map[string]any{"meals.times": overlap}); code != 400 {
		t.Fatalf("overlapping save = %d, want 400", code)
	}
	if code := put(member, map[string]any{"meals.times": overlap}); code != 403 {
		t.Fatalf("member save = %d, want 403", code)
	}
	if code := put(member, map[string]any{"sell.show_price": false}); code != 200 {
		t.Fatalf("member's own save = %d", code)
	}
	if meals.Shared()["breakfast"] != bf["breakfast"] {
		t.Fatalf("shared = %v", meals.Shared())
	}

	// Left out of an admin's save, they go back to config.toml.
	if code := put(admin, map[string]any{}); code != 200 || meals.Shared() != nil {
		t.Fatalf("reset = %d, shared = %v", code, meals.Shared())
	}
}

// A member rewrites the bot's messages: the page gets every slot and
// variable, a bad message is refused with why, and a good one goes live.
func TestSettingsMessages(t *testing.T) {
	st := newTestStore(t)
	cfg := config.Default()
	cfg.ApplySaved(nil)
	d := startDashboard(t, cfg, st, mcpserver.New(cfg, st, nil), NewHub())
	member := d.as(t, accounts.Member)

	var got settingsState
	if res := member.get(t, "/api/settings", &got); res.StatusCode != 200 {
		t.Fatalf("GET = %d", res.StatusCode)
	}
	if len(got.Wording.Slots) != len(wording.Slots) || len(got.Wording.Vars) != len(wording.Vars) || got.Values["sell.own_wording"] != true {
		t.Fatalf("wording = %+v, own_wording = %v", got.Wording, got.Values["sell.own_wording"])
	}
	for _, s := range got.Wording.Slots {
		if !slices.Contains(s.Uses, "meal") {
			t.Errorf("%s: uses %v, want every variable it can use", s.ID, s.Uses)
		}
	}

	put := func(msgs string) (int, string) {
		res := member.send(t, "PUT", "/api/settings", map[string]any{"values": map[string]any{"sell.messages": json.RawMessage(msgs)}})
		defer res.Body.Close()
		var e struct{ Error string }
		json.NewDecoder(res.Body).Decode(&e)
		return res.StatusCode, e.Error
	}
	if code, msg := put(`{"counter": [["take it for cheap"]]}`); code != 400 || !strings.Contains(msg, "needs {price}") {
		t.Fatalf("counter without a price: %d %q", code, msg)
	}
	if code, msg := put(`{"counter": [["ok [{buyer} ]{price}", "last one"]], "nudge": [["yes or no?"], ["are you buying?"], ["interested?"]]}`); code != 200 {
		t.Fatalf("save: %d %q", code, msg)
	}
	live := cfg.Live().Sell.Messages
	if len(live) != 1 || live["counter"][0][1] != "last one" {
		t.Fatalf("live messages = %v, want only the counter (the nudge matches the default)", live)
	}
}

func TestSettingsAlertsAPI(t *testing.T) {
	st := newTestStore(t)
	cfg := config.Default()
	cfg.ApplySaved(nil)
	d := startDashboard(t, cfg, st, mcpserver.New(cfg, st, nil), NewHub())
	member := d.as(t, accounts.Member)

	var got settingsState
	if res := member.get(t, "/api/settings", &got); res.StatusCode != 200 {
		t.Fatalf("GET = %d", res.StatusCode)
	}
	if got.Values["alerts.whatsapp"] != false || got.Defaults["alerts.whatsapp"] != false {
		t.Fatalf("alerts.whatsapp = %v / %v, want false", got.Values["alerts.whatsapp"], got.Defaults["alerts.whatsapp"])
	}
	if got.Values["alerts.quiet_start"] != "00:00" || got.Values["alerts.quiet_end"] != "07:00" {
		t.Fatalf("quiet hours = %v..%v", got.Values["alerts.quiet_start"], got.Values["alerts.quiet_end"])
	}

	res := member.send(t, "PUT", "/api/settings", map[string]any{"values": map[string]any{
		"alerts.whatsapp":     true,
		"alerts.kinds":        []string{"refund", "blocked"},
		"alerts.quiet_start":  "23:00",
		"alerts.quiet_end":    "07:30",
		"alerts.max_per_hour": 10,
	}})
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("member save alerts = %d, want 200", res.StatusCode)
	}
	live := cfg.Live().Alerts
	if !live.WhatsApp || !slices.Equal(live.Kinds, []string{"refund", "blocked"}) || live.QuietStart != "23:00" || live.QuietEnd != "07:30" || live.MaxPerHour != 10 {
		t.Fatalf("live alerts = %+v", live)
	}

	bad := member.send(t, "PUT", "/api/settings", map[string]any{"values": map[string]any{
		"alerts.kinds": []string{"bogus"},
	}})
	bad.Body.Close()
	if bad.StatusCode != 400 {
		t.Fatalf("bad alert kind = %d, want 400", bad.StatusCode)
	}
}
