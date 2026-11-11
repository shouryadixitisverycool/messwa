package portal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func extrasClient(t *testing.T, body string, check func(*http.Request)) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MESS_API_URL", srv.URL+"/api")
	c, err := New("test-key")
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = srv.Client().Transport
	return c
}

func TestExtraRegistrationsContract(t *testing.T) {
	c := extrasClient(t, `{"data":[
		{"id":"x1","extra":"e1","meal_date":"2026-11-04","meal_type":"lunch","meal_mess":"kadamba-nonveg","user_id":"private-user","extra_name":" Chicken 65 ","extra_rate":"4000","availed_at":null},
		{"id":"x2","meal_date":"2026-11-04T00:00:00Z","meal_type":"lunch","meal_mess":"kadamba","extra_name":"Lassi"},
		{"id":"x3","meal_date":"2026-11-05","meal_type":"lunch","meal_mess":"kadamba","extra_name":"Other day"},
		{"id":"x4","meal_date":"2026-11-04","meal_type":"dinner","meal_mess":"kadamba","extra_name":"Other meal"}
	]}`, func(r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/registrations/extras" || r.URL.Query().Get("meal") != "lunch" || r.URL.Query().Get("date") != "2026-11-04" || r.Header.Get("Authorization") != "test-key" {
			t.Errorf("wrong extras request: %s %s", r.Method, r.URL)
		}
	})
	rows, err := c.ExtraRegistrations(context.Background(), "2026-11-04", "lunch")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Name != "Chicken 65" || rows[1].Name != "Lassi" || rows[0].Availed() {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestExtraRegistrationsRejectsBadInput(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"wrong shape", `{"data":{}}`},
		{"no name", `{"data":[{"meal_date":"2026-11-04","meal_type":"lunch"}]}`},
		{"missing data", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := extrasClient(t, tc.body, nil)
			if _, err := c.ExtraRegistrations(context.Background(), "2026-11-04", "lunch"); err == nil {
				t.Fatal("bad response accepted")
			}
		})
	}
	c := extrasClient(t, `{"data":null}`, nil)
	if rows, err := c.ExtraRegistrations(context.Background(), "2026-11-04", "lunch"); err != nil || len(rows) != 0 {
		t.Fatalf("null data: %v %v", rows, err)
	}
	if _, err := c.ExtraRegistrations(context.Background(), "4 Nov", "lunch"); err == nil {
		t.Fatal("bad date accepted")
	}
	if _, err := c.ExtraRegistrations(context.Background(), "2026-11-04", "brunch"); err == nil {
		t.Fatal("bad meal accepted")
	}
}
