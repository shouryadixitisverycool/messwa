package portal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

func TestSummarizeExtrasAndRequireExtra(t *testing.T) {
	used := "2026-11-25T19:45:00Z"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/registrations/extras":
			fmt.Fprintf(w, `{"data":[
				{"id":"e1","extra":"egg","meal_date":"2026-11-25","meal_type":"dinner","meal_mess":"kadamba-nonveg","extra_name":"Egg","extra_rate":"1500","availed_at":null},
				{"id":"e2","extra":"egg","meal_date":"2026-11-25","meal_type":"dinner","meal_mess":"kadamba-nonveg","extra_name":" egg ","extra_rate":1500,"availed_at":null},
				{"id":"e3","extra":"egg","meal_date":"2026-11-25","meal_type":"dinner","meal_mess":"kadamba-nonveg","extra_name":"Egg","extra_rate":"1500","availed_at":%q}
			]}`, used)
		case "/api/registrations":
			// Main dinner is ALREADY availed; extra QR verification must still pass!
			fmt.Fprintf(w, `{"data":[{"meal_date":"2026-11-25","meal_type":"dinner","meal_mess":"kadamba-nonveg","category":"registered","availed_at":%q}]}`, used)
		case "/api/auth/me":
			fmt.Fprint(w, `{"data":{"id":"u1","token":"qr-tok-123"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MESS_API_URL", srv.URL+"/api")
	c, err := New("test-key")
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = srv.Client().Transport

	rows, err := c.ExtraRegistrations(context.Background(), "2026-11-25", "dinner")
	if err != nil {
		t.Fatal(err)
	}
	sums := SummarizeExtras(rows)
	if len(sums) != 1 || sums[0].Name != "Egg" || sums[0].Mess != "kadamba" || sums[0].Total != 3 || sums[0].Availed != 1 || sums[0].Unavailed != 2 || sums[0].RatePaise != 1500 {
		t.Fatalf("summaries = %+v", sums)
	}
	if _, err := c.RequireRegistration(context.Background(), "2026-11-25", "dinner"); err == nil {
		t.Fatal("expected availed main dinner to fail RequireRegistration")
	}
	qr, unavailed, err := c.QRForExtra(context.Background(), "2026-11-25", "dinner", "egg", 2, "")
	if err != nil {
		t.Fatalf("QRForExtra failed even though 2 eggs are unavailed: %v", err)
	}
	if unavailed != 2 || len(qr.PNG) == 0 || qr.Registration.Mess != "kadamba" {
		t.Fatalf("QRForExtra = %+v, unavailed=%d", qr.Registration, unavailed)
	}
	if _, _, err := c.QRForExtra(context.Background(), "2026-11-25", "dinner", "egg", 3, ""); err == nil {
		t.Fatal("expected error requesting 3 eggs when only 2 unavailed")
	}
}

func TestExtraRateAndAvailableExtras(t *testing.T) {
	if got := ExtraRate("1500").Paise(); got != 1500 {
		t.Fatalf("integer paise = %d, want 1500", got)
	}
	if got := ExtraRate("36.00").Paise(); got != 3600 {
		t.Fatalf("decimal rupees = %d, want 3600", got)
	}

	eatenEarly := "2026-11-25T19:30:00Z"
	scannedAfter := "2026-11-25T19:45:00Z"
	soldAt := time.Date(2026, 11, 25, 19, 40, 0, 0, time.UTC)

	// 3 registered, owner ate 1 at 19:30 BEFORE selling 1 at 19:40 (buyer unscanned):
	// only 1 egg is actually free, NOT 2!
	rowsOwnerAte := []ExtraRegistration{
		{Name: "Egg"},
		{Name: "Egg"},
		{Name: "Egg", AvailedAt: &eatenEarly},
	}
	total, availed, unavailed, avail := AvailableExtras(rowsOwnerAte, "Egg", []time.Time{soldAt}, 0, time.UTC)
	if total != 3 || availed != 1 || unavailed != 2 || avail != 1 {
		t.Fatalf("owner ate before sale: got total=%d availed=%d unavailed=%d avail=%d, want 3/1/2/1", total, availed, unavailed, avail)
	}

	// 3 registered, sold 1 at 19:40 and buyer scanned at 19:45: 2 eggs remain free.
	rowsBuyerScanned := []ExtraRegistration{
		{Name: "Egg"},
		{Name: "Egg"},
		{Name: "Egg", AvailedAt: &scannedAfter},
	}
	_, _, _, avail = AvailableExtras(rowsBuyerScanned, "Egg", []time.Time{soldAt}, 0, time.UTC)
	if avail != 2 {
		t.Fatalf("buyer scanned after sale: avail=%d, want 2", avail)
	}

	// Delivery still in sending state (zero sent_at) must NEVER match an availed row.
	_, _, _, avail = AvailableExtras(rowsBuyerScanned, "Egg", []time.Time{{}}, 0, time.UTC)
	if avail != 1 {
		t.Fatalf("unsent delivery matched availed row: avail=%d, want 1", avail)
	}

	// Naive IST timestamp without zone offset parses in loc, not UTC.
	ist := time.FixedZone("IST", 5*3600+30*60)
	naiveIST := "2026-11-25T19:30:00" // 14:00 UTC, BEFORE 14:10 UTC send time
	soldAtIST := time.Date(2026, 11, 25, 19, 40, 0, 0, ist)
	_, _, _, avail = AvailableExtras([]ExtraRegistration{{Name: "Egg"}, {Name: "Egg"}, {Name: "Egg", AvailedAt: &naiveIST}}, "Egg", []time.Time{soldAtIST}, 0, ist)
	if avail != 1 {
		t.Fatalf("naive IST availed_at parsed wrong timezone: avail=%d, want 1", avail)
	}

	// Owner scan 10 seconds BEFORE buyer QR delivery must NOT be credited to the buyer.
	eatenJustBefore := "2026-11-25T19:39:50Z"
	_, _, _, avail = AvailableExtras([]ExtraRegistration{{Name: "Egg"}, {Name: "Egg", AvailedAt: &eatenJustBefore}}, "Egg", []time.Time{soldAt}, 0, time.UTC)
	if avail != 0 {
		t.Fatalf("owner scan 10s before QR delivery matched buyer: avail=%d, want 0", avail)
	}

	// 2 registered, 1 sold unscanned, 1 cancelled on portal -> 1 unavailed, 0 free.
	rowsCancelled := []ExtraRegistration{{Name: "Egg"}}
	_, _, _, avail = AvailableExtras(rowsCancelled, "Egg", []time.Time{soldAt}, 0, time.UTC)
	if avail != 0 {
		t.Fatalf("cancelled after unscanned sale: avail=%d, want 0", avail)
	}
}

// A scan in the same second as the QR send may be the owner's, so it is not
// credited to the buyer.
func TestAvailableExtrasSameSecondScanNotCreditedToBuyer(t *testing.T) {
	sentAt := time.Date(2026, 11, 25, 19, 40, 0, 0, time.UTC)
	sameSecond := "2026-11-25T19:40:00Z"
	later := "2026-11-25T19:40:01Z"
	rows := []ExtraRegistration{{Name: "Egg"}, {Name: "Egg", AvailedAt: &sameSecond}}
	if _, _, _, avail := AvailableExtras(rows, "Egg", []time.Time{sentAt}, 0, time.UTC); avail != 0 {
		t.Fatalf("same-second scan credited to the buyer: avail=%d, want 0", avail)
	}
	rows[1].AvailedAt = &later
	if _, _, _, avail := AvailableExtras(rows, "Egg", []time.Time{sentAt}, 0, time.UTC); avail != 1 {
		t.Fatalf("scan a second after the send: avail=%d, want 1", avail)
	}
}
