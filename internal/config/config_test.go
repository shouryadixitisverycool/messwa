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
	if !c.Sell.Repost || c.Sell.RepostAfter.Minutes() != 10 || c.Sell.RepostMessages != 10 || c.Sell.RepostListings != 3 || c.Sell.MaxReposts != 6 {
		t.Errorf("repost settings = %+v", c.Sell)
	}
	c, err = load("[sell]\nrepost = false\nrepost_after = \"0s\"\nrepost_messages = 15\nmax_reposts = 0\n")
	if err != nil || c.Sell.Repost || c.Sell.RepostAfter.Duration != 0 || c.Sell.RepostMessages != 15 || c.Sell.RepostListings != 3 || c.Sell.MaxReposts != 0 {
		t.Fatalf("repost settings = %+v, %v", c.Sell, err)
	}
	for name, body := range map[string]string{
		"both":          "groups = [\"1@g.us\"]\n[sell]\ngroups = [\"Mess\"]\ngroup_count = 1\n",
		"count high":    "groups = [\"1@g.us\"]\n[sell]\ngroup_count = 2\n",
		"count low":     "[sell]\ngroup_count = -1\n",
		"empty group":   "[sell]\ngroups = [\" \"]\n",
		"gap too short": "[sell]\npost_gap = \"1s\"\n",
		"repost soon":   "[sell]\nrepost_after = \"1m\"\n",
		"bad count":     "[sell]\nrepost_listings = -1\n",
		"many reposts":  "[sell]\nmax_reposts = 13\n",
	} {
		if _, err := load(body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
