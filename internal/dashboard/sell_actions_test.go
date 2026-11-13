package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	pngenc "image/png"
	"mime/multipart"
	"net/http"
	"testing"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
)

type operatorCall struct {
	id                int64
	buyer, name, note string
	price             int
	muted             bool
}
type dashboardOperator struct {
	calls []operatorCall
	tasks []string // start, prices and cancel calls, as text
	// failStart makes Start fail.
	failStart error
}

func (o *dashboardOperator) Start(_ context.Context, user int64, date, meal string, min, max int, handoff string) (int64, error) {
	if o.failStart != nil {
		return 0, o.failStart
	}
	o.tasks = append(o.tasks, fmt.Sprintf("start %d %s %s %d-%d %q", user, date, meal, min, max, handoff))
	return 9, nil
}
func (o *dashboardOperator) Cancel(_ context.Context, id int64, revoke bool) error {
	o.tasks = append(o.tasks, fmt.Sprintf("cancel %d %v", id, revoke))
	return nil
}
func (o *dashboardOperator) SetPrices(_ context.Context, id int64, min, max int) error {
	o.tasks = append(o.tasks, fmt.Sprintf("prices %d %d-%d", id, min, max))
	return nil
}
func (o *dashboardOperator) MarkSold(_ context.Context, id int64, buyer, name string, price int, note string) error {
	o.calls = append(o.calls, operatorCall{id: id, buyer: buyer, name: name, price: price, note: note})
	return nil
}
func (o *dashboardOperator) AddBuyer(_ context.Context, id int64, buyer, name string) error {
	o.calls = append(o.calls, operatorCall{id: id, buyer: buyer, name: name})
	return nil
}
func (o *dashboardOperator) MuteBuyer(_ context.Context, id int64, buyer string, muted bool) error {
	o.calls = append(o.calls, operatorCall{id: id, buyer: buyer, muted: muted})
	return nil
}

func TestSellActionsAPI(t *testing.T) {
	cfg := config.Default()
	cfg.MCP.SellTasks = true
	st := newTestStore(t)
	srv := mcpserver.New(cfg, st, nil)
	op := &dashboardOperator{}
	srv.SetSellTasks(op)
	d := startDashboard(t, cfg, st, srv, NewHub())
	admin, member := d.as(t, accounts.Admin), d.as(t, accounts.Member)
	for _, action := range []string{"sold", "add", "mute", "unmute"} {
		in := map[string]any{"action": action, "id": 7, "buyer": "+911111111111", "name": "Friend", "price": 55, "note": "cash"}
		if res := member.send(t, "POST", "/api/sell-action", in); res.StatusCode != 403 {
			t.Fatalf("member %s = %d", action, res.StatusCode)
		}
		old := admin.csrf
		admin.csrf = ""
		if res := admin.send(t, "POST", "/api/sell-action", in); res.StatusCode != 403 {
			t.Fatalf("no CSRF %s = %d", action, res.StatusCode)
		}
		admin.csrf = old
		if res := admin.send(t, "POST", "/api/sell-action", in); res.StatusCode != 200 {
			t.Fatalf("admin %s = %d", action, res.StatusCode)
		}
	}
	if len(op.calls) != 4 || op.calls[0].price != 55 || op.calls[0].note != "cash" || op.calls[1].name != "Friend" || !op.calls[2].muted || op.calls[3].muted {
		t.Fatalf("calls = %+v", op.calls)
	}
	for _, c := range op.calls {
		if c.id != 7 || c.buyer != "+911111111111" {
			t.Fatalf("call = %+v", c)
		}
	}
	if res := admin.send(t, "POST", "/api/sell-action", map[string]any{"action": "bad"}); res.StatusCode != 400 {
		t.Fatalf("bad action = %d", res.StatusCode)
	}
	// The ledger starts, reprices and stops tasks as the signed-in user.
	for _, in := range []map[string]any{
		{"action": "start", "date": "2026-11-03", "meal": "dinner", "min": 50, "max": 80},
		{"action": "prices", "id": 9, "min": 45, "max": 75},
		{"action": "cancel", "id": 9},
	} {
		if res := member.send(t, "POST", "/api/sell-action", in); res.StatusCode != 403 {
			t.Fatalf("member %s = %d", in["action"], res.StatusCode)
		}
		res := admin.send(t, "POST", "/api/sell-action", in)
		if res.StatusCode != 200 {
			t.Fatalf("admin %s = %d", in["action"], res.StatusCode)
		}
		if in["action"] == "start" {
			var out struct{ ID int64 }
			json.NewDecoder(res.Body).Decode(&out)
			if out.ID != 9 {
				t.Errorf("start returned task %d", out.ID)
			}
		}
		res.Body.Close()
	}
	want := []string{fmt.Sprintf("start %d 2026-11-03 dinner 50-80 \"\"", admin.user.ID), "prices 9 45-75", "cancel 9 true"}
	if fmt.Sprint(op.tasks) != fmt.Sprint(want) {
		t.Fatalf("task calls %q, want %q", op.tasks, want)
	}

	cfg.MCP.SellTasks = false
	// Live config without saved settings reads the config itself.
	if res := admin.send(t, "POST", "/api/sell-action", map[string]any{"action": "add"}); res.StatusCode != 400 {
		t.Fatalf("disabled = %d", res.StatusCode)
	}
}

func TestFriendSaleAPI(t *testing.T) {
	cfg := config.Default()
	cfg.MCP.SellTasks = true
	st := newTestStore(t)
	srv := mcpserver.New(cfg, st, nil)
	op := &dashboardOperator{}
	srv.SetSellTasks(op)
	d := startDashboard(t, cfg, st, srv, NewHub())
	admin, member := d.as(t, accounts.Admin), d.as(t, accounts.Member)

	var png bytes.Buffer
	if err := pngenc.Encode(&png, image.NewGray(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	post := func(c *testClient, fields map[string]string, qr []byte) *http.Response {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		for k, v := range fields {
			mw.WriteField(k, v)
		}
		if qr != nil {
			fw, _ := mw.CreateFormFile("qr", "qr.png")
			fw.Write(qr)
		}
		mw.Close()
		req, _ := http.NewRequest("POST", d.URL+"/api/friend-sale", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Origin", d.URL)
		req.Header.Set("X-CSRF-Token", c.csrf)
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	mess := cfg.Messes[0].Name
	form := map[string]string{"date": "2026-11-13", "meal": "dinner", "mess": mess, "diet": "nonveg", "friend": "Ravi", "extras": "2 omelette, lassi", "min": "50", "max": "80"}
	if res := post(member, form, png.Bytes()); res.StatusCode != 403 {
		t.Fatalf("member = %d", res.StatusCode)
	}
	for name, bad := range map[string]func(map[string]string) ([]byte, map[string]string){
		"no qr":      func(f map[string]string) ([]byte, map[string]string) { return nil, f },
		"not image":  func(f map[string]string) ([]byte, map[string]string) { return []byte("hello"), f },
		"bad mess":   func(f map[string]string) ([]byte, map[string]string) { f["mess"] = "nowhere"; return png.Bytes(), f },
		"bad diet":   func(f map[string]string) ([]byte, map[string]string) { f["diet"] = "vegan"; return png.Bytes(), f },
		"bad prices": func(f map[string]string) ([]byte, map[string]string) { f["min"] = "x"; return png.Bytes(), f },
	} {
		f := map[string]string{}
		for k, v := range form {
			f[k] = v
		}
		qr, f := bad(f)
		if res := post(admin, f, qr); res.StatusCode != 400 {
			t.Errorf("%s = %d", name, res.StatusCode)
		}
	}
	if len(op.tasks) != 0 {
		t.Fatalf("bad forms started tasks: %q", op.tasks)
	}

	res := post(admin, form, png.Bytes())
	if res.StatusCode != 200 {
		t.Fatalf("admin = %d", res.StatusCode)
	}
	var out struct {
		ID   int64
		Slot string
	}
	json.NewDecoder(res.Body).Decode(&out)
	res.Body.Close()
	if out.ID != 9 || out.Slot != "dinner#f1" {
		t.Fatalf("out %+v", out)
	}
	want := fmt.Sprintf("start %d 2026-11-13 dinner#f1 50-80 \"\"", admin.user.ID)
	if len(op.tasks) != 1 || op.tasks[0] != want {
		t.Fatalf("tasks %q, want %q", op.tasks, want)
	}
	m, err := st.FriendMeal(context.Background(), "2026-11-13", "dinner#f1")
	if err != nil || m == nil || m.Mess != mess+"-nonveg" || m.Friend != "Ravi" || len(m.Extras) != 2 || m.Extras[1] != "lassi" || !bytes.Equal(m.QR, png.Bytes()) {
		t.Fatalf("saved %+v %v", m, err)
	}

	// A task that fails to start leaves nothing saved.
	op.failStart = errors.New("meal has already ended")
	if res := post(admin, form, png.Bytes()); res.StatusCode != 400 {
		t.Fatalf("failed start = %d", res.StatusCode)
	}
	if m, _ := st.FriendMeal(context.Background(), "2026-11-13", "dinner#f2"); m != nil {
		t.Fatal("a friend's meal stayed saved after its task failed to start")
	}
}
