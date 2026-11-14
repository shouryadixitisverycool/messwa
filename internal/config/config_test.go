package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExampleConfig(t *testing.T) {
	c, err := Load("../../config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Meals) != 4 || len(c.Messes) != 4 {
		t.Errorf("defaults: %d meals, %d messes", len(c.Meals), len(c.Messes))
	}
	if c.Dedupe.Window.Hours() != 24 || c.DMs.Retention.Hours() != 72 {
		t.Errorf("durations: %v %v", c.Dedupe.Window, c.DMs.Retention)
	}
}

func TestTablesReplaceDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte("[[messes]]\nname = \"north\"\n"), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Messes) != 1 || len(c.Meals) != 4 {
		t.Errorf("got %d messes, %d meals", len(c.Messes), len(c.Meals))
	}
}

func TestSellSettings(t *testing.T) {
	c := Default()
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if len(c.Sell.Groups) != 0 || c.Sell.GroupCount != 0 || !c.Sell.ShowPrice || c.Sell.MentionExtras || c.Sell.PostGap.Seconds() != 15 {
		t.Errorf("defaults changed today's offer: %+v", c.Sell)
	}
	load := func(body string) (*Config, error) {
		p := filepath.Join(t.TempDir(), "c.toml")
		os.WriteFile(p, []byte(body), 0o600)
		return Load(p)
	}
	c, err := load("groups = [\"1@g.us\", \"2@g.us\"]\n[sell]\ngroup_count = 2\nshow_price = false\nmention_extras = true\npost_gap = \"30s\"\n")
	if err != nil || c.Sell.GroupCount != 2 || c.Sell.ShowPrice || !c.Sell.MentionExtras || c.Sell.PostGap.Seconds() != 30 {
		t.Fatalf("sell = %+v, %v", c.Sell, err)
	}
	if !c.Sell.Repost || c.Sell.RepostMessages != 10 || c.Sell.RepostListings != 3 || c.Sell.MaxReposts != 6 {
		t.Errorf("repost settings = %+v", c.Sell)
	}
	// repost_after is gone; an old config.toml that still sets it loads.
	c, err = load("[sell]\nrepost = false\nrepost_after = \"10m\"\nrepost_messages = 15\nmax_reposts = 0\n")
	if err != nil || c.Sell.Repost || c.Sell.RepostMessages != 15 || c.Sell.RepostListings != 3 || c.Sell.MaxReposts != 0 {
		t.Fatalf("repost settings = %+v, %v", c.Sell, err)
	}
	for name, body := range map[string]string{
		"both":          "groups = [\"1@g.us\"]\n[sell]\ngroups = [\"Mess\"]\ngroup_count = 1\n",
		"count high":    "groups = [\"1@g.us\"]\n[sell]\ngroup_count = 2\n",
		"count low":     "[sell]\ngroup_count = -1\n",
		"empty group":   "[sell]\ngroups = [\" \"]\n",
		"gap too short": "[sell]\npost_gap = \"1s\"\n",
		"bad count":     "[sell]\nrepost_listings = -1\n",
		"many reposts":  "[sell]\nmax_reposts = 13\n",
		"silence short": "[sell]\nnegotiation_silence = \"30s\"\n",
		"silence long":  "[sell]\nnegotiation_silence = \"31m\"\n",
	} {
		if _, err := load(body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestHiddenMeal(t *testing.T) {
	c := Default()
	if c.HiddenMeal("snacks") {
		t.Error("snacks hidden with hide_snacks off")
	}
	c.HideSnacks = true
	for meal, want := range map[string]bool{"snacks": true, "snacks#f1": true, "dinner": false, "": false} {
		if got := c.HiddenMeal(meal); got != want {
			t.Errorf("HiddenMeal(%q) = %v, want %v", meal, got, want)
		}
	}
}
