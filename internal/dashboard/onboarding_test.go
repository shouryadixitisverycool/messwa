package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"testing"

	"messwa/internal/config"
	"messwa/internal/mcpserver"
)

// groupsDevice is a linked WhatsApp that is in the groups joined lists.
type groupsDevice struct {
	linkDevice
	joined map[string]string
}

func (d *groupsDevice) JoinedGroups(context.Context) (map[string]string, error) { return d.joined, nil }

func TestSetupSell(t *testing.T) {
	cfg := config.Default()
	cfg.Groups = []string{"a@g.us", "b@g.us", "c@g.us"}
	cfg.ApplySaved(nil)
	st := newTestStore(t)
	ms := mcpserver.New(cfg, st, nil)
	ms.Device = &groupsDevice{linkDevice{linked: true}, map[string]string{"a@g.us": "Mess Buy Sell", "c@g.us": "Mess 2.0", "x@g.us": "Family"}}
	d := startDashboard(t, cfg, st, ms, NewHub())

	// A new account, not approved yet, sets up selling from the welcome page.
	jar, _ := cookiejar.New(nil)
	c := &testClient{Client: &http.Client{Jar: jar}, d: d}
	res := c.send(t, "POST", "/api/signup", map[string]string{"name": "asha", "password": "asha's password"})
	var me struct{ CSRF string }
	json.NewDecoder(res.Body).Decode(&me)
	res.Body.Close()
	c.csrf = me.CSRF

	var got setupSell
	if res := c.get(t, "/api/setup-sell", &got); res.StatusCode != 200 {
		t.Fatalf("GET = %d", res.StatusCode)
	}
	if !got.Checked || got.Saved || len(got.Groups) != 3 || got.Handoff != "never" || got.Buyers != "anyone" {
		t.Fatalf("state = %+v", got)
	}
	if g := got.Groups; !g[0].Joined || g[0].Name != "Mess Buy Sell" || g[1].Joined || !g[2].Joined {
		t.Fatalf("groups = %+v", g)
	}

	for _, bad := range []map[string]any{
		{"groups": []string{}},
		{"groups": []string{"x@g.us"}},
		{"groups": []string{"a@g.us"}, "handoff": "later"},
	} {
		res := c.send(t, "PUT", "/api/setup-sell", bad)
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Errorf("PUT %v = %d, want 400", bad, res.StatusCode)
		}
	}

	res = c.send(t, "PUT", "/api/setup-sell", map[string]any{"groups": []string{"c@g.us", "a@g.us"}, "handoff": "on_reply", "buyers": "unsaved_only"})
	json.NewDecoder(res.Body).Decode(&got)
	res.Body.Close()
	if res.StatusCode != 200 || !got.Saved || got.Handoff != "on_reply" || got.Buyers != "unsaved_only" || !got.Groups[0].Picked || got.Groups[1].Picked {
		t.Fatalf("PUT = %d %+v", res.StatusCode, got)
	}
	if l := cfg.Live(); len(l.Sell.Groups) != 2 || l.Sell.Groups[0] != "c@g.us" || l.Sell.GroupCount != 0 {
		t.Fatalf("live sell = %+v", l.Sell)
	}
}
