package classify

import (
	"strings"
	"testing"
)

var messes = []Named{
	{Name: "kadamba", Aliases: []string{"kadamb", "kdm", "kdmb", "kdmba", "kadam"}, Fuzzy: true},
	{Name: "yuktahar", Aliases: []string{"yukthar", "yukt", "ykth", "yukhtar"}, Fuzzy: true},
	{Name: "palash", Aliases: []string{"palas", "plsh"}, Fuzzy: true},
	{Name: "bakul", Aliases: []string{"bkl"}, Fuzzy: true},
}

var meals = []Named{
	{Name: "breakfast", Aliases: []string{"bf", "b/f", "bfast", "bkft"}, Fuzzy: true},
	{Name: "lunch", Aliases: []string{"lunxh"}, Fuzzy: true},
	{Name: "snacks", Aliases: []string{"snack"}, Fuzzy: true},
	{Name: "dinner", Fuzzy: true},
}

func names(hs []Hit) string {
	var s []string
	for _, h := range hs {
		s = append(s, h.Name)
	}
	return strings.Join(s, ",")
}

// Real posts from the IIITH mess groups.
func TestRealPosts(t *testing.T) {
	cases := []struct {
		text, kind, mess, diet, meal string
		price                        int // -1 for none
	}{
		{"Sell kadamb veg lunch", "sell", "kadamba", "veg", "lunch", -1},
		{"sell bakul veg 25", "sell", "bakul", "veg", "", 25},
		{"Sell kadamba nv @100", "sell", "kadamba", "nonveg", "", 100},
		{"Buy KDM v", "buy", "kadamba", "veg", "", -1},
		{"Buy kadamb nb", "buy", "kadamba", "nonveg", "", -1},
		{"But kadamb nv", "buy", "kadamba", "nonveg", "", -1},
		{"Sell papash", "sell", "palash", "", "", -1},
		{"Palas selll", "sell", "palash", "", "", -1},
		{"Sell yukthahar dinner x3", "sell", "yuktahar", "", "dinner", -1},
		{"Sell yukhtahar bf", "sell", "yuktahar", "", "breakfast", -1},
		{"Sell kadhamba omlette", "sell", "kadamba", "egg", "", -1},
		{"sell kdmb veg lunch@35", "sell", "kadamba", "veg", "lunch", 35},
		{"Sell Kadamb veg lunxh", "sell", "kadamba", "veg", "lunch", -1},
		{"Bakul giveaway", "sell", "bakul", "", "", 0},
		{"Sell Bakul vef", "sell", "bakul", "veg", "", -1},
		{"Sell kadamba nv@175", "sell", "kadamba", "nonveg", "", 175},
		{"Sell yuk dinner 30/-", "sell", "yuktahar", "", "dinner", 30},
		{"Exchange Have bakul veg Want kadamba veg/yukthar", "exchange", "", "veg", "", -1},
		{"good morning", "", "", "", "", -1},
	}
	for _, c := range cases {
		tx := Prepare(c.text)
		ms := Pick(tx, messes)
		mess := ""
		if len(ms) > 0 && c.kind != "exchange" {
			mess = ms[0].Name
		}
		meal := ""
		if m := Pick(tx, meals); len(m) > 0 {
			meal = m[0].Name
		}
		price := -1
		if Free(tx) {
			price = 0
		} else if v, ok := Price(tx); ok {
			price = v
		}
		got := []string{Kind(tx), mess, Diet(tx), meal}
		want := []string{c.kind, c.mess, c.diet, c.meal}
		if strings.Join(got, "|") != strings.Join(want, "|") || price != c.price {
			t.Errorf("%q: got %v price %d, want %v price %d", c.text, got, price, want, c.price)
		}
	}
}

func TestExchange(t *testing.T) {
	cases := map[string]string{
		"Exchange Have bakul veg Want kadamba veg/yukthar":     "bakul>kadamba,yuktahar",
		"Exchange bakul veg for kadamba non veg":               "bakul>kadamba",
		"Exchange Have Kadamba Wants palash":                   "kadamba>palash",
		"Exchange palash for kadamb":                           "palash>kadamba",
		"Exchange want palash have kadamb":                     "kadamba>palash",
		"Buy yuktahar or exchange with bakul":                  "bakul>yuktahar",
		"Buy Yuktahar\nOR\nEXCHANGE: Have Bakul want Yuktahar": "bakul>yuktahar",
		"Sell bakul veg\nOr exchange bakul for palash":         "bakul>palash",
	}
	for in, want := range cases {
		tx := Prepare(in)
		if Kind(tx) != "exchange" {
			t.Errorf("%q not an exchange", in)
		}
		have, w := Exchange(tx, Pick(tx, messes))
		if got := have + ">" + strings.Join(w, ","); got != want {
			t.Errorf("%q: got %s, want %s", in, got, want)
		}
	}
}

func TestQuantityAndNoise(t *testing.T) {
	if q := Quantity(Prepare("Sell yukthar dinner x3")); q != 3 {
		t.Errorf("x3 = %d", q)
	}
	if q := Quantity(Prepare("sell 2x kadamb")); q != 2 {
		t.Errorf("2x = %d", q)
	}
	if _, ok := Price(Prepare("Sell yukthar dinner x3")); ok {
		t.Error("x3 is a quantity, not a price")
	}
	if p, ok := Price(Prepare("WTB Bakul lunch at 12:30")); ok {
		t.Errorf("12:30 is a time, not a price %d", p)
	}
	if p, ok := Price(Prepare("sell bakul lunch 12:30 40")); !ok || p != 40 {
		t.Errorf("price after a time = %d, %v", p, ok)
	}
	if Prepare("bfast").Has("bf") {
		t.Error("Has must match whole words")
	}
	if Kind(Prepare("well ok")) != "" {
		t.Error("'well' must not read as sell")
	}
	if got := names(Pick(Prepare("sell palash/yuktahaar"), messes)); got != "palash,yuktahar" {
		t.Errorf("order: %s", got)
	}
}
