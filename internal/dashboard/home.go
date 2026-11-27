package dashboard

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/mcpserver"
	"messwa/internal/store"
)

type homeVisitResp struct {
	Since     int64   `json:"since"`
	Sold      int     `json:"sold"`
	Earned    float64 `json:"earned"`
	Refunds   int     `json:"refunds"`
	WeekTotal float64 `json:"week_total"`
	WeekSold  int     `json:"week_sold"`
}

func homeRoutes(mux *http.ServeMux, cfg *config.Config, srv *mcpserver.Server) {
	wrap := func(fn http.HandlerFunc) http.Handler {
		return requireRole(accounts.Member, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !srv.Scoped() && !accounts.UserFrom(r.Context()).IsAdmin() {
				jsonError(w, "admins only", 403)
				return
			}
			fn(w, r)
		}))
	}
	handle := func(ping bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			userID := accounts.UserFrom(r.Context()).ID
			resp, err := buildHomeVisit(r.Context(), cfg, srv, userID, ping)
			if err != nil {
				jsonError(w, "Could not load home summary", 500)
				return
			}
			writeJSON(w, r, resp)
		}
	}
	mux.Handle("POST /api/home/visit", wrap(handle(true)))
	mux.Handle("GET /api/home/visit", wrap(handle(false)))
}

func buildHomeVisit(ctx context.Context, cfg *config.Config, srv *mcpserver.Server, userID int64, ping bool) (homeVisitResp, error) {
	var since int64
	var err error
	if ping {
		since, err = srv.PingHomeVisit(ctx, userID)
	} else {
		since, err = srv.HomeVisitSince(ctx, userID)
	}
	if err != nil {
		return homeVisitResp{}, err
	}

	ds, err := srv.Deliveries(ctx)
	if err != nil {
		return homeVisitResp{}, err
	}

	loc := cfg.Location()
	local := srv.Now().In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	offset := (int(today.Weekday()) + 6) % 7
	// Monday to today, as Home's own count from api/sales: a meal sold
	// ahead counts on its day.
	weekStart := today.AddDate(0, 0, -offset).Format("2006-01-02")
	weekEnd := today.Format("2006-01-02")

	resp := homeVisitResp{Since: since}
	for _, d := range ds {
		if d.State != "sent" && d.State != "sending" {
			continue
		}
		if isFriendDelivery(d) {
			continue
		}
		amount, _ := strconv.ParseFloat(d.Amount, 64)
		if since > 0 && saleTimestamp(d) > since {
			resp.Sold++
			resp.Earned += amount
		}
		if d.Date >= weekStart && d.Date <= weekEnd {
			resp.WeekSold++
			resp.WeekTotal += amount
		}
	}
	resp.Earned = math.Round(resp.Earned*100) / 100
	resp.WeekTotal = math.Round(resp.WeekTotal*100) / 100

	if since > 0 {
		refunds, err := srv.ResolvedAttentionCount(ctx, userID, "refund", since)
		if err != nil {
			return homeVisitResp{}, err
		}
		resp.Refunds = refunds
	}
	return resp, nil
}

func isExtraDelivery(d store.DeliveryRecord) bool {
	return store.IsExtraSlot(d.Meal) || d.Extra != ""
}

func isFriendDelivery(d store.DeliveryRecord) bool {
	return !isExtraDelivery(d) && strings.Contains(d.Meal, "#")
}

func saleTimestamp(d store.DeliveryRecord) int64 {
	if !d.PaidAt.IsZero() {
		return d.PaidAt.Unix()
	}
	if !d.SentAt.IsZero() {
		return d.SentAt.Unix()
	}
	if !d.ArmedAt.IsZero() {
		return d.ArmedAt.Unix()
	}
	return 0
}
