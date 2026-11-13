package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestWithSettings(t *testing.T) {
	c := Default()
	c.Groups = []string{"a@g.us", "b@g.us"}
	n, err := c.WithSettings(Settings{
		"sell.show_price":    raw(false),
		"sell.post_gap":      raw(30),
		"sell.group_count":   raw(2),
		"sell.buyers":        raw("unsaved_only"),
		"sold.words":         raw([]string{" sold ", "", "sold", "gone"}),
		"meals.times":        raw(map[string]MealTime{"lunch": {"12:00", "15:00"}}),
		"messes.veg_only":    raw(map[string]bool{"bakul": true}),
		"sales.monthly_fee":  raw(3200),
		"sell.repost_after":  raw(0),
		"dedupe.window":      raw(12 * 3600),
		"sell.max_reposts":   raw(4),
		"mcp.sell_tasks":     raw(true),
		"sold.closed_emojis": raw([]string{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.Sell.ShowPrice || n.Sell.PostGap.Duration != 30*time.Second || n.Sell.GroupCount != 2 || !n.Sell.Buyers.UnsavedOnly() {
		t.Fatalf("sell = %+v", n.Sell)
	}
	if strings.Join(n.Sold.Words, ",") != "sold,gone" {
		t.Fatalf("words = %q", n.Sold.Words)
	}
	if n.Meals[1].Starts != "12:00" || n.Meals[1].Ends != "15:00" || n.Meals[1].Keywords[0] != "lunxh" {
		t.Fatalf("lunch = %+v", n.Meals[1])
	}
	if !n.VegOnly("bakul") || !n.VegOnly("palash") {
		t.Fatal("veg_only not applied")
	}
	if n.Sales.MonthlyFee != 3200 || n.Sell.RepostAfter.Duration != 0 || n.Dedupe.Window.Duration != 12*time.Hour || n.Sell.MaxReposts != 4 || !n.MCP.SellTasks {
		t.Fatalf("config = %+v", n)
	}
	// c itself is untouched, including the slices the copy changed.
	if !c.Sell.ShowPrice || c.Meals[1].Starts != "12:30" || c.VegOnly("bakul") {
		t.Fatal("WithSettings changed the original")
	}
	// Values reads back what was set.
	if v := n.Values(); v["sell.post_gap"] != int64(30) || v["sell.buyers"] != BuyersUnsavedOnly {
		t.Fatalf("values = %v", v)
	}
}

func TestWithSettingsRefuses(t *testing.T) {
	c := Default()
	c.Groups = []string{"a@g.us"}
	for name, s := range map[string]Settings{
		"unknown key":            {"sell.nope": raw(true)},
		"wrong type":             {"sell.show_price": raw("yes")},
		"post gap too short":     {"sell.post_gap": raw(1)},
		"repost too soon":        {"sell.repost_after": raw(60)},
		"silence too short":      {"sell.negotiation_silence": raw(30)},
		"silence too long":       {"sell.negotiation_silence": raw(31 * 60)},
		"too many reposts":       {"sell.max_reposts": raw(13)},
		"group count over":       {"sell.group_count": raw(2)},
		"groups and count":       {"sell.group_count": raw(1), "sell.groups": raw([]string{"a@g.us"})},
		"bad handoff":            {"sell.handoff": raw("on_agreement")},
		"no sold emoji":          {"sold.emojis": raw([]string{" "})},
		"short retention":        {"dms.retention": raw(60)},
		"negative fee":           {"sales.monthly_fee": raw(-1)},
		"meal ends before start": {"meals.times": raw(map[string]MealTime{"lunch": {"14:00", "13:00"}})},
		"bad clock":              {"meals.times": raw(map[string]MealTime{"lunch": {"noon", "13:00"}})},
		"seasons overlap":        {"seasons": raw([]Season{{Name: "A", Starts: "01-01", Ends: "02-01"}, {Name: "B", Starts: "01-15", Ends: "03-01"}})},
		"season extra field":     {"seasons": raw([]map[string]any{{"name": "A", "starts": "01-01", "ends": "02-01", "colour": "red"}})},
	} {
		if _, err := c.WithSettings(s); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestApplySavedSkipsStaleKeys(t *testing.T) {
	c := Default()
	c.Groups = []string{"a@g.us"}
	skipped := c.ApplySaved(Settings{
		"sell.show_price":  raw(false),
		"sell.group_count": raw(3), // config.toml now lists one group
		"old.key":          raw(1),
	})
	if c.Sell.ShowPrice || c.Sell.GroupCount != 0 {
		t.Fatalf("sell = %+v", c.Sell)
	}
	if len(skipped) != 2 || skipped["sell.group_count"] == nil || skipped["old.key"] == nil {
		t.Fatalf("skipped = %v", skipped)
	}
	if !c.File().Sell.ShowPrice {
		t.Fatal("File() should be config.toml's values")
	}
}

func TestUpdateSwapsLive(t *testing.T) {
	c := Default()
	c.ApplySaved(Settings{"sell.show_price": raw(false)})
	if c.Live() != c {
		t.Fatal("Live before any Update should be the config itself")
	}
	n, err := c.Update(Settings{"sell.mention_extras": raw(true)})
	if err != nil {
		t.Fatal(err)
	}
	live := c.Live()
	if live != n || !live.Sell.MentionExtras {
		t.Fatal("Update didn't swap the live config")
	}
	// Update starts from config.toml, so the earlier saved key is gone.
	if !live.Sell.ShowPrice {
		t.Fatal("show_price should be back to config.toml's value")
	}
	// The started config keeps its values; Live on a live copy is itself.
	if c.Sell.ShowPrice || c.Sell.MentionExtras || live.Live() != live {
		t.Fatal("Update changed the running config")
	}
	if _, err := c.Update(Settings{"sell.post_gap": raw(0)}); err == nil || c.Live() != live {
		t.Fatal("a bad Update should leave the live config alone")
	}
}

func TestNeedsRestart(t *testing.T) {
	a := Default()
	b, _ := a.WithSettings(Settings{"messes.veg_only": raw(map[string]bool{"bakul": true})})
	if !NeedsRestart("messes.veg_only") || NeedsRestart("sell.show_price") {
		t.Fatal("restart keys")
	}
	if SameValue("messes.veg_only", a, b) || !SameValue("meals.times", a, b) {
		t.Fatal("SameValue")
	}
}
