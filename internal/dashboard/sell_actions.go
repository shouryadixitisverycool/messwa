package dashboard

import (
	"bytes"
	"image"
	_ "image/jpeg" // QR uploads
	_ "image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
	"messwa/internal/store"
)

// maxQR is the largest friend's QR image accepted, as WhatsApp sends it.
const maxQR = 5 << 20

// startedResp answers a sell start. At is when a scheduled task starts,
// with ID the schedule's; 0 means the task started now and ID is its own.
type startedResp struct {
	ID   int64  `json:"id"`
	Slot string `json:"slot,omitempty"`
	At   int64  `json:"at,omitempty"`
}

// sellActionRoutes shares the task operations with MCP: starting, repricing
// and stopping a task from the sales ledger, selling a friend's meal, and
// the MY SALE box's actions. Session middleware applies the same origin and
// CSRF checks as every other dashboard write.
func sellActionRoutes(mux *http.ServeMux, cfg *config.Config, srv *mcpserver.Server) {
	mux.Handle("POST /api/friend-sale", requireRole(accounts.Member, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !srv.Scoped() && !accounts.UserFrom(r.Context()).IsAdmin() {
			jsonError(w, "admins only", http.StatusForbidden)
			return
		}
		m, min, max, err := readFriendSale(w, r, cfg)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, slot, at, err := srv.StartFriendSale(r.Context(), accounts.UserFrom(r.Context()).ID, m, min, max)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, r, startedResp{ID: id, Slot: slot, At: unix(at)})
	})))

	mux.Handle("POST /api/sell-action", requireRole(accounts.Member, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Action string `json:"action"`
			ID     int64  `json:"id"`
			Buyer  string `json:"buyer"`
			Name   string `json:"name"`
			Price  int    `json:"price"`
			Note   string `json:"note"`
			Group  string `json:"group"` // resend: the group's JID
			// start, prices: the meal and the price range in rupees.
			Date string `json:"date"`
			Meal string `json:"meal"`
			Min  int    `json:"min"`
			Max  int    `json:"max"`
			// start: Now starts the task straight away even when the
			// meal hasn't begun, instead of scheduling it. Autopilot
			// starts it on autopilot; autopilot: turns it on or off for
			// schedule ID.
			Now       bool `json:"now"`
			Autopilot bool `json:"autopilot"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if !srv.Scoped() && !accounts.UserFrom(r.Context()).IsAdmin() {
			jsonError(w, "admins only", http.StatusForbidden)
			return
		}
		switch in.Action {
		case "start":
			if !picksGroups(cfg.Live()) {
				jsonError(w, "pick the groups to post in first, under Settings, Offer", http.StatusBadRequest)
				return
			}
			var id int64
			var at time.Time
			var err error
			userID := accounts.UserFrom(r.Context()).ID
			if in.Now {
				var sc mcpserver.SellScheduler
				if sc, err = srv.SellScheduler(); err == nil {
					id, err = sc.StartNow(r.Context(), userID, in.Date, in.Meal, in.Min, in.Max, in.Autopilot)
				}
			} else {
				var sc mcpserver.SellScheduler
				if sc, err = srv.SellScheduler(); err == nil {
					id, at, err = sc.StartOrSchedule(r.Context(), userID, in.Date, in.Meal, in.Min, in.Max, in.Autopilot)
				}
			}
			if err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, r, startedResp{ID: id, At: unix(at)})
			return
		case "autopilot":
			sc, err := srv.SellScheduler()
			if err == nil {
				err = sc.SetScheduleAutopilot(r.Context(), in.ID, in.Autopilot)
			}
			if err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, r, struct{}{})
			return
		case "unschedule":
			sc, err := srv.SellScheduler()
			if err == nil {
				err = sc.CancelSchedule(r.Context(), in.ID)
			}
			if err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, r, struct{}{})
			return
		case "prices", "cancel":
			sell, err := srv.SellControl()
			if err == nil {
				switch in.Action {
				case "prices":
					err = sell.SetPrices(r.Context(), in.ID, in.Min, in.Max)
				case "cancel":
					err = sell.Cancel(r.Context(), in.ID, true)
				}
			}
			if err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, r, struct{}{})
			return
		}
		op, err := srv.SellOperator()
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch in.Action {
		case "sold":
			err = op.MarkSold(r.Context(), in.ID, in.Buyer, in.Name, in.Price, in.Note)
		case "add":
			err = op.AddBuyer(r.Context(), in.ID, in.Buyer, in.Name)
		case "mute", "unmute":
			err = op.MuteBuyer(r.Context(), in.ID, in.Buyer, in.Action == "mute")
		case "resend":
			err = op.ResendOffer(r.Context(), in.ID, in.Group)
		default:
			jsonError(w, "unknown sell action", http.StatusBadRequest)
			return
		}
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, r, struct{}{})
	})))
}

// readFriendSale reads the "sell my friend's meal" form: a multipart body
// with date, meal, mess, diet, friend, extras (one per line or comma),
// min, max and the QR image as qr.
func readFriendSale(w http.ResponseWriter, r *http.Request, cfg *config.Config) (store.FriendMeal, int, int, error) {
	var m store.FriendMeal
	r.Body = http.MaxBytesReader(w, r.Body, maxQR+64<<10)
	if err := r.ParseMultipartForm(maxQR + 64<<10); err != nil {
		return m, 0, 0, errBad("the form or QR image is too large (5 MB at most)")
	}
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	mess, diet := strings.ToLower(v("mess")), v("diet")
	known := false
	for _, x := range cfg.Messes {
		known = known || x.Name == mess
	}
	if !known {
		return m, 0, 0, errBad("pick one of the messes")
	}
	switch {
	case cfg.VegOnly(mess):
		diet = "veg"
	case diet != "veg" && diet != "nonveg" && diet != "egg":
		return m, 0, 0, errBad("diet must be veg, nonveg or egg")
	}
	min, err1 := strconv.Atoi(v("min"))
	max, err2 := strconv.Atoi(v("max"))
	if err1 != nil || err2 != nil {
		return m, 0, 0, errBad("min and max must be whole rupees")
	}
	f, _, err := r.FormFile("qr")
	if err != nil {
		return m, 0, 0, errBad("attach your friend's QR image")
	}
	defer f.Close()
	qr, err := io.ReadAll(io.LimitReader(f, maxQR+1))
	if err != nil {
		return m, 0, 0, err
	}
	if len(qr) > maxQR {
		return m, 0, 0, errBad("the QR image is over 5 MB")
	}
	if c, format, err := image.DecodeConfig(bytes.NewReader(qr)); err != nil || (format != "png" && format != "jpeg") || c.Width > 8192 || c.Height > 8192 {
		return m, 0, 0, errBad("the QR must be a PNG or JPEG image")
	}
	var extras []string
	for _, e := range strings.FieldsFunc(v("extras"), func(c rune) bool { return c == ',' || c == '\n' }) {
		if e = strings.TrimSpace(e); e != "" {
			extras = append(extras, e)
		}
	}
	if len(extras) > 10 {
		return m, 0, 0, errBad("ten extras at most")
	}
	friend := v("friend")
	if len(friend) > 60 {
		friend = friend[:60]
	}
	return store.FriendMeal{Date: v("date"), Slot: v("meal"), Mess: mess + "-" + diet, Friend: friend, Extras: extras, QR: qr}, min, max, nil
}

type errBad string

func (e errBad) Error() string { return string(e) }
