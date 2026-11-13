package store

import (
	"context"
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
	prices TEXT NOT NULL,
	n      INTEGER NOT NULL,
	PRIMARY KEY (date, meal, mess)
);`

// ServingPrices is every stated asking price for one serving of one meal at
// one mess. Each sell listing counts once, at its latest price; free and
// unpriced listings and exchanges don't count.
type ServingPrices struct {
	Date, Meal, Mess string
	Prices           []int
}

func (s *Store) livePrices(ctx context.Context, from, to string) (map[[3]string][]int, error) {
	rows, err := s.boardDB().QueryContext(ctx, `SELECT meal_date, meal, mess, price FROM listings
		WHERE kind='sell' AND price > 0 AND meal != '' AND mess != '' AND meal_date >= ? AND meal_date <= ?
		ORDER BY meal_date, meal, mess, price`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[3]string][]int{}
	for rows.Next() {
		var k [3]string
		var p int
		if err := rows.Scan(&k[0], &k[1], &k[2], &p); err != nil {
			return nil, err
		}
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
		if _, err := s.boardDB().ExecContext(ctx, `INSERT INTO market_prices(date,meal,mess,prices,n) VALUES(?,?,?,?,?)
			ON CONFLICT(date,meal,mess) DO UPDATE SET prices=excluded.prices, n=excluded.n WHERE excluded.n >= market_prices.n`,
			k[0], k[1], k[2], joinInts(ps), len(ps)); err != nil {
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
	rows, err := s.boardDB().QueryContext(ctx, `SELECT date, meal, mess, prices FROM market_prices WHERE date >= ? AND date <= ?`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k [3]string
		var csv string
		if err := rows.Scan(&k[0], &k[1], &k[2], &csv); err != nil {
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
		out = append(out, ServingPrices{Date: k[0], Meal: k[1], Mess: k[2], Prices: ps})
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
