package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"

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
	got = setupSell{}
	json.NewDecoder(res.Body).Decode(&got)
	res.Body.Close()
	if res.StatusCode != 200 || !got.Saved || got.Handoff != "on_reply" || got.Buyers != "unsaved_only" || !got.Groups[0].Picked || got.Groups[1].Picked {
		t.Fatalf("PUT = %d %+v", res.StatusCode, got)
	}
	if g := got.Groups; g[2].Order != 1 || g[0].Order != 2 || g[1].Order != 0 {
		t.Fatalf("PUT orders = %+v", g)
	}
	if l := cfg.Live(); len(l.Sell.Groups) != 2 || l.Sell.Groups[0] != "c@g.us" || l.Sell.GroupCount != 0 {
		t.Fatalf("live sell = %+v", l.Sell)
	}

	got = setupSell{}
	if res := c.get(t, "/api/setup-sell", &got); res.StatusCode != 200 {
		t.Fatalf("GET after PUT = %d", res.StatusCode)
	}
	if g := got.Groups; g[2].Order != 1 || g[0].Order != 2 || g[1].Order != 0 {
		t.Fatalf("GET orders = %+v", g)
	}

	t.Run("group_count_fallback", func(t *testing.T) {
		cfg := config.Default()
		cfg.Groups = []string{"a@g.us", "b@g.us", "c@g.us"}
		cfg.Sell.GroupCount = 2
		cfg.ApplySaved(nil)
		st := newTestStore(t)
		ms := mcpserver.New(cfg, st, nil)
		d := startDashboard(t, cfg, st, ms, NewHub())

		jar, _ := cookiejar.New(nil)
		c := &testClient{Client: &http.Client{Jar: jar}, d: d}
		res := c.send(t, "POST", "/api/signup", map[string]string{"name": "asha", "password": "asha's password"})
		res.Body.Close()

		var got setupSell
		if res := c.get(t, "/api/setup-sell", &got); res.StatusCode != 200 {
			t.Fatalf("GET = %d", res.StatusCode)
		}
		if g := got.Groups; len(g) != 3 || !g[0].Picked || g[0].Order != 1 || !g[1].Picked || g[1].Order != 2 || g[2].Picked || g[2].Order != 0 {
			t.Fatalf("groups = %+v", g)
		}
	})
}

func TestSetupUPI(t *testing.T) {
	cfg := config.Default()
	cfg.ApplySaved(nil)
	st := newTestStore(t)
	ms := mcpserver.New(cfg, st, nil)
	d := startDashboard(t, cfg, st, ms, NewHub())

	// A new account, not approved yet, saves its UPI ID from the welcome page.
	jar, _ := cookiejar.New(nil)
	c := &testClient{Client: &http.Client{Jar: jar}, d: d}
	res := c.send(t, "POST", "/api/signup", map[string]string{"name": "asha", "password": "asha's password"})
	var me struct{ CSRF string }
	json.NewDecoder(res.Body).Decode(&me)
	res.Body.Close()
	c.csrf = me.CSRF

	var got struct{ UPI string }
	if res := c.get(t, "/api/setup-upi", &got); res.StatusCode != 200 || got.UPI != "" {
		t.Fatalf("GET = %d %+v", res.StatusCode, got)
	}
	for _, bad := range []string{"", "  ", "not a upi"} {
		res := c.send(t, "PUT", "/api/setup-upi", map[string]string{"upi": bad})
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Errorf("PUT %q = %d, want 400", bad, res.StatusCode)
		}
	}
	res = c.send(t, "PUT", "/api/setup-upi", map[string]string{"upi": " Asha@OkAxis "})
	json.NewDecoder(res.Body).Decode(&got)
	res.Body.Close()
	if res.StatusCode != 200 || got.UPI != "asha@okaxis" {
		t.Fatalf("PUT = %d %+v", res.StatusCode, got)
	}
	// The UPI QR only goes out when screenshots count, so saving turns
	// that on.
	if l := cfg.Live(); l.Sell.UPI != "asha@okaxis" || !l.Sell.AcceptPaymentImages {
		t.Fatalf("live sell = %+v", l.Sell)
	}

	// The sample chat's QR pays the UPI ID typed so far.
	res, err := c.Get(d.URL + "/api/upi-qr-preview?upi=Asha@OkAxis")
	if err != nil {
		t.Fatal(err)
	}
	png, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if got, err := readUPIQR(png); res.StatusCode != 200 || err != nil || got != "asha@okaxis" {
		t.Fatalf("preview = %d, %q, %v", res.StatusCode, got, err)
	}
	if res := c.get(t, "/api/upi-qr-preview?upi=nope", nil); res.StatusCode != 400 {
		t.Errorf("bad preview = %d, want 400", res.StatusCode)
	}
	// The settings page's test QR asks for the amount it was given.
	if res, err = c.Get(d.URL + "/api/upi-qr-preview?upi=asha@okaxis&amount=32.5"); err != nil {
		t.Fatal(err)
	}
	png, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if text := decodeQRText(t, png); !strings.Contains(text, "am=32.50") || !strings.Contains(text, "pa=asha%40okaxis") {
		t.Errorf("preview with amount = %q", text)
	}
	for _, bad := range []string{"0", "-5", "abc", "100001", "1.234"} {
		if res := c.get(t, "/api/upi-qr-preview?upi=asha@okaxis&amount="+url.QueryEscape(bad), nil); res.StatusCode != 400 {
			t.Errorf("amount %q = %d, want 400", bad, res.StatusCode)
		}
	}
}

// decodeQRText returns the text inside a QR PNG.
func decodeQRText(t *testing.T, png []byte) string {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(png))
	if err != nil {
		t.Fatal(err)
	}
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatal(err)
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.GetText()
}
