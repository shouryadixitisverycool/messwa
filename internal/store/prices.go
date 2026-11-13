package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// Listings are pruned after two weeks, but the sales page compares your
// prices with the market over four. market_prices keeps every serving's
// asking prices past that.
const priceSchema = `
CREATE TABLE IF NOT EXISTS market_prices (
	date   TEXT NOT NULL,
	meal   TEXT NOT NULL,
	mess   TEXT NOT NULL,
	diet   TEXT NOT NULL DEFAULT 'veg',
	prices TEXT NOT NULL,
	n      INTEGER NOT NULL,
	PRIMARY KEY (date, meal, mess, diet)
);`

// migratePrices rebuilds a market_prices from before the diet column. The
// old rows mix veg and non-veg asks, so they go, and the snapshot is
// taken again from the listings that haven't been pruned yet.
func migratePrices(db *sql.DB) error {
	if _, err := db.Exec(`SELECT diet FROM market_prices LIMIT 0`); err == nil {
		return nil
	}
	if _, err := db.Exec(`DROP TABLE market_prices`); err != nil {
		return err
	}
	if _, err := db.Exec(priceSchema); err != nil {
		return err
	}
	return (&Store{db: db}).SnapshotPrices(context.Background(), "")
}

// PriceDiet is the diet an asking price counts under: nonveg, egg or veg.
// A listing that doesn't say is counted as veg, which is what such posts
// almost always are.
func PriceDiet(diet string) string {
	if diet == "nonveg" || diet == "egg" {
		return diet
	}
	return "veg"
}

// ServingPrices is every stated asking price for one serving of one meal at
// one mess, for one diet (see PriceDiet). Each sell listing counts once, at
// its latest price; free and unpriced listings and exchanges don't count.
type ServingPrices struct {
	Date, Meal, Mess, Diet string
	Prices                 []int
}

func (s *Store) livePrices(ctx context.Context, from, to string) (map[[4]string][]int, error) {
	rows, err := s.boardDB().QueryContext(ctx, `SELECT meal_date, meal, mess, diet, price FROM listings
		WHERE kind='sell' AND price > 0 AND meal != '' AND mess != '' AND meal_date >= ? AND meal_date <= ?
		ORDER BY meal_date, meal, mess, price`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[4]string][]int{}
	for rows.Next() {
		var k [4]string
		var p int
		if err := rows.Scan(&k[0], &k[1], &k[2], &k[3], &p); err != nil {
			return nil, err
		}
		k[3] = PriceDiet(k[3])
		out[k] = append(out[k], p)
	}
	return out, rows.Err()
}

// SnapshotPrices saves the asking prices of every serving from `from` on.
// A serving whose listings are partly pruned keeps its fuller snapshot.
func (s *Store) SnapshotPrices(ctx context.Context, from string) error {
	live, err := s.livePrices(ctx, from, "9999-12-31")
	if err != nil {
		return err
	}
	for k, ps := range live {
		if _, err := s.boardDB().ExecContext(ctx, `INSERT INTO market_prices(date,meal,mess,diet,prices,n) VALUES(?,?,?,?,?,?)
			ON CONFLICT(date,meal,mess,diet) DO UPDATE SET prices=excluded.prices, n=excluded.n WHERE excluded.n >= market_prices.n`,
			k[0], k[1], k[2], k[3], joinInts(ps), len(ps)); err != nil {
			return err
		}
	}
	return nil
}

// Prices returns the asking prices for servings dated from..to, inclusive,
// from live listings or the snapshot, whichever has more of them.
func (s *Store) Prices(ctx context.Context, from, to string) ([]ServingPrices, error) {
	all, err := s.livePrices(ctx, from, to)
	if err != nil {
		return nil, err
	}
	rows, err := s.boardDB().QueryContext(ctx, `SELECT date, meal, mess, diet, prices FROM market_prices WHERE date >= ? AND date <= ?`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k [4]string
		var csv string
		if err := rows.Scan(&k[0], &k[1], &k[2], &k[3], &csv); err != nil {
			return nil, err
		}
		if ps := splitInts(csv); len(ps) > len(all[k]) {
			all[k] = ps
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ServingPrices, 0, len(all))
	for k, ps := range all {
		out = append(out, ServingPrices{Date: k[0], Meal: k[1], Mess: k[2], Diet: k[3], Prices: ps})
	}
	return out, nil
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, ",")
}

func splitInts(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ",") {
		if v, err := strconv.Atoi(p); err == nil {
			out = append(out, v)
		}
	}
	return out
}
