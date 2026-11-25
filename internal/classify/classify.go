// Package classify pulls structure out of free-text mess posts.
//
// The rules come from reading real posts in the IIITH mess groups. Posts are
// terse ("Sell kadamb nv @30", "Buy bakul veg", "Exchange have palash want
// kadamba") and full of typos ("yukthahar", "kadhamba", "papash", "lunxh"),
// so names are matched with a small edit-distance and prefix tolerance.
package classify

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Normalize lowercases, turns everything that isn't a letter or digit into a
// space, and collapses runs of spaces. Emojis and punctuation disappear.
func Normalize(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// Text is a post prepared for matching.
type Text struct {
	raw    string
	padded string
	tokens []string
}

func Prepare(s string) Text {
	n := Normalize(s)
	return Text{raw: strings.ToLower(s), padded: " " + n + " ", tokens: strings.Fields(n)}
}

func (t Text) Words() int { return len(t.tokens) }

// Has reports whether any phrase appears as whole words, exactly.
func (t Text) Has(phrases ...string) bool {
	for _, p := range phrases {
		if n := Normalize(p); n != "" && strings.Contains(t.padded, " "+n+" ") {
			return true
		}
	}
	return false
}

// index returns the token index where any alias first matches, or -1.
// Multi-word aliases match exactly; single words match loosely if fuzzy.
func (t Text) index(aliases []string, fuzzy bool) int {
	best := -1
	for _, a := range aliases {
		a = Normalize(a)
		if a == "" {
			continue
		}
		if strings.Contains(a, " ") {
			if i := strings.Index(t.padded, " "+a+" "); i >= 0 {
				pos := len(strings.Fields(t.padded[:i]))
				if best < 0 || pos < best {
					best = pos
				}
			}
			continue
		}
		for i, tok := range t.tokens {
			if best >= 0 && i >= best {
				break
			}
			if tok == a || fuzzy && near(tok, a) {
				best = i
				break
			}
		}
	}
	return best
}

// near allows abbreviations ("yuk", "kadam") and small typos ("kadhamba",
// "papash"). Short words must match exactly.
func near(tok, alias string) bool {
	if len(tok) >= 3 && len(tok) < len(alias) && strings.HasPrefix(alias, tok) {
		return true
	}
	if len(tok) < 4 || len(alias) < 4 {
		return false
	}
	max := 1
	if len(alias) >= 6 {
		max = 2
	}
	return lev(tok, alias) <= max
}

func lev(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// Named is anything that can be spotted in text by a list of aliases.
type Named struct {
	Name    string
	Aliases []string
	Fuzzy   bool
}

type Hit struct {
	Name string
	Pos  int // token index
}

// Pick returns the names that appear in t, earliest mention first.
func Pick(t Text, options []Named) []Hit {
	var hits []Hit
	for _, o := range options {
		if p := t.index(append([]string{o.Name}, o.Aliases...), o.Fuzzy); p >= 0 {
			hits = append(hits, Hit{o.Name, p})
		}
	}
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].Pos < hits[j-1].Pos; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	return hits
}

var (
	exchangeWords = []string{"exchange", "exch", "xchange", "swap", "barter"}
	strongBuy     = []string{"anyone selling", "anybody selling", "any one selling", "someone selling", "want to buy", "wanna buy", "wtb", "looking for", "lf"}
	sellWords     = []string{"sell", "selling", "sells", "selll", "sel", "seel", "sellin", "sale", "for sale", "available", "avail", "anyone want", "anybody want", "anyone wants", "wts", "giving", "giveaway", "give away", "free"}
	weakBuy       = []string{"buy", "buying", "need", "needed", "want", "wanted", "require", "required"}
	freeWords     = []string{"giveaway", "give away", "free", "freely"}
	// In "Buy yuktahar or exchange with bakul" the bought mess is wanted.
	haveMarkers = []string{"have", "has", "giving", "give", "sell", "selling"}
	wantMarkers = []string{"buy", "buying", "need", "needed"}
	// Words that separate "have" from "want" in an exchange post.
	wantWords = []string{"want", "wants", "wanted", "need", "to"}
)

// Kind returns "sell", "buy", "exchange", or "" when the post says none.
func Kind(t Text) string {
	switch {
	case t.Has(exchangeWords...) || t.Has("have") && t.Has("want", "wants", "need"):
		return "exchange"
	case t.Has(strongBuy...):
		return "buy"
	case t.Has(sellWords...):
		return "sell"
	case len(t.tokens) > 0 && t.tokens[0] == "but": // "But kadamb nv"
		return "buy"
	case t.Has(weakBuy...):
		return "buy"
	}
	return ""
}

func Free(t Text) bool { return t.Has(freeWords...) }

// Diet returns "nonveg", "egg", "veg", or "".
func Diet(t Text) string {
	switch {
	case t.Has("nonveg", "non veg", "nv", "nb", "chicken", "mutton", "fish"):
		return "nonveg"
	case t.Has("egg", "eggs", "omelette", "omlette", "omlete", "omelet", "omlet"):
		return "egg"
	case t.Has("veg", "v", "vef", "vveg", "vg"):
		return "veg"
	}
	return ""
}

// Exchange splits the messes in an exchange post into what the poster has
// and what they want. Each mess belongs to the nearest marker before it:
// "have palash want kadamba", "want palash have kadamb", "bakul for kadamba".
// Without markers the first mess is the one offered.
func Exchange(t Text, messes []Hit) (have string, want []string) {
	marker := make([]string, len(t.tokens)) // "have", "want", or "" per token
	cur := ""
	for i, tok := range t.tokens {
		switch {
		case tok == "for" || tok == "with":
			// "bakul for kadamba": what follows is wanted.
			// "buy yuktahar or exchange with bakul": what follows is offered.
			if cur == "want" {
				cur = "have"
			} else {
				cur = "want"
			}
		case contains(haveMarkers, tok):
			cur = "have"
		case contains(wantWords, tok) || contains(wantMarkers, tok):
			cur = "want"
		}
		marker[i] = cur
	}
	for _, m := range messes {
		side := marker[m.Pos]
		if side == "" {
			side = "have"
			if have != "" {
				side = "want"
			}
		}
		if side == "have" && have == "" {
			have = m.Name
		} else if side == "want" && !contains(want, m.Name) && m.Name != have {
			want = append(want, m.Name)
		}
	}
	return have, want
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

var (
	atPrice     = regexp.MustCompile(`@\s*(\d{1,4})`)
	rupeePrice  = regexp.MustCompile(`(?:rs\.?|₹|inr)\s*(\d{1,4})|(\d{1,4})\s*(?:rs\b|/-|₹|rupees)`)
	clockTime   = regexp.MustCompile(`\b\d{1,2}:\d{2}\b`)
	quantityPat = regexp.MustCompile(`\bx\s?([2-9])\b|\b([2-9])\s?x\b`)
)

// Price returns the asking price, if the post states one: "@30", "rs 30",
// "30/-", or a lone number like "Sell bakul veg 20".
func Price(t Text) (int, bool) {
	for _, re := range []*regexp.Regexp{atPrice, rupeePrice} {
		if m := re.FindStringSubmatch(t.raw); m != nil {
			for _, g := range m[1:] {
				if v, err := strconv.Atoi(g); err == nil {
					return v, true
				}
			}
		}
	}
	// A clock time like "12:30" is not a lone price.
	tokens := t.tokens
	if clockTime.MatchString(t.raw) {
		tokens = strings.Fields(Normalize(clockTime.ReplaceAllString(t.raw, " ")))
	}
	for _, tok := range tokens {
		if v, err := strconv.Atoi(tok); err == nil && v >= 5 && v <= 1000 {
			return v, true
		}
	}
	return 0, false
}

// Quantity returns how many registrations are on offer ("x2", "3x"), default 1.
func Quantity(t Text) int {
	if m := quantityPat.FindStringSubmatch(t.raw); m != nil {
		for _, g := range m[1:] {
			if v, err := strconv.Atoi(g); err == nil {
				return v
			}
		}
	}
	return 1
}
