package market

import (
	"math"
	"time"
)

// TimePressure is how long before a meal ends a negotiating sell task
// starts coming down from its target toward its floor.
const TimePressure = 3 * time.Hour

// PriceUnit is the step a negotiation's prices round to: ₹5, or ₹1 when
// the opening price is under ₹20.
func PriceUnit(maxRupees int) int {
	if maxRupees < 20 {
		return 1
	}
	return 5
}

// Walk is the lowest price, in rupees, a negotiating sell task accepts at
// now: target until TimePressure before the meal ends, then falling to min
// in unit steps. While several buyers are interested it stays at target.
// The negotiation and the dashboard both use it, so the board shows the
// price the bot would actually take.
func Walk(minRupees, target, unit int, end, now time.Time, competing bool) int {
	if competing {
		return target
	}
	frac := min(max(float64(end.Sub(now))/float64(TimePressure), 0), 1)
	w := minRupees + int(math.Ceil(frac*float64(target-minRupees)/float64(unit)))*unit
	return min(max(w, minRupees), target)
}
