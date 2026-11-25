// Package wording holds every line the bot sends a buyer, as templates
// each account can rewrite on the settings page.
//
// A slot is one moment in a sale, such as the first price or a counter
// offer. It has variants, and the bot picks one at random each time. A
// variant is one or more messages sent back to back, like "25" then
// "final offer".
//
// Templates fill {name} with a variable. Square brackets mark an optional
// bit: "its {price}[, for {day}]" drops ", for {day}" when the meal is
// today's and {day} is empty.
package wording

import (
	"fmt"
	"regexp"
	"strings"
)

// Var is one variable a template can use.
type Var struct {
	Name    string `json:"name"`
	Example string `json:"example"`
	About   string `json:"about"`
	// CanBeEmpty says the bot sometimes has no value for it, so a
	// template should wrap it in [ ].
	CanBeEmpty bool `json:"can_be_empty,omitempty"`
}

// Vars are all the variables, in the order the help lists them.
var Vars = []Var{
	{"meal", "kadamba non-veg lunch", "Mess, diet and meal together", false},
	{"mess", "kadamba", "The mess", false},
	{"diet", "non-veg", "veg, non-veg or egg", true},
	{"mealtime", "lunch", "breakfast, lunch, snacks or dinner", false},
	{"day", "tmrw", "Which day the meal is for. Empty for today's meal", true},
	{"price", "35", "The price the bot is asking right now", false},
	{"buyer", "Asha", "The buyer's WhatsApp name. Might be a nickname or an emoji", true},
	{"extras", "chicken 65, lassi", "Extras registered for this meal", true},
	{"ends", "2:30", "When the sale stops", false},
	{"left", "25 min", "Time left until the sale stops", false},
	{"was", "40", "The price the bot asked before this one", true},
	{"wait", "2 min", "How long a buyer has to pay before the next one gets a turn", false},
	{"paid", "3", "What the buyer's payment screenshots add up to so far", false},
	{"short", "32", "How much more the buyer still has to pay", false},
}

// common are the variables every slot can use.
var common = []string{"meal", "mess", "diet", "mealtime", "day", "buyer", "extras", "ends", "left"}

// Slot is one thing the bot says.
type Slot struct {
	ID    string `json:"id"`
	Group string `json:"group"`
	Name  string `json:"name"`
	When  string `json:"when"`
	// Uses are the variables this slot has on top of the common ones.
	Uses []string `json:"uses"`
	// Required must appear in every variant, because the bot's next step
	// depends on the buyer having seen them.
	Required []string `json:"required"`
	// Single slots are one message per variant.
	Single bool `json:"single,omitempty"`
	// NeedsText slots need words of their own outside variables. The
	// sold line does: an ended task may send only text that matches it.
	NeedsText bool       `json:"needs_text,omitempty"`
	Default   [][]string `json:"default"`
}

// Allowed lists every variable s can use, required ones first.
func (s Slot) Allowed() []string {
	out := append([]string(nil), s.Required...)
	for _, v := range append(append([]string(nil), s.Uses...), common...) {
		if !contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func v(parts ...string) []string { return parts }

// Slots are everything the bot says to a buyer, grouped the way the
// settings page lists them.
var Slots = []Slot{
	{ID: "have", Group: "Starting", Name: "I have it", Single: true,
		When:    "First DM to someone whose buy post asked for this meal, sent as a reply to their post. The opening price follows on its own.",
		Uses:    []string{"price"},
		Default: [][]string{v("have {meal}[ for {day}]"), v("i have {meal}[ for {day}]"), v("{meal}[ for {day}] available")}},
	{ID: "opening", Group: "Starting", Name: "Opening price",
		When: "The first price a buyer sees. The bot never says it's negotiable.",
		Uses: []string{"price"}, Required: []string{"price"},
		Default: [][]string{v("{price}[, its for {day}]"), v("its {price}[, its for {day}]"), v("{price} for it[, its for {day}]")}},

	{ID: "counter", Group: "Haggling", Name: "Counter offer",
		When: "The bot's first move down in price.",
		Uses: []string{"price", "was"}, Required: []string{"price"},
		Default: [][]string{v("how about {price}"), v("can do {price}"), v("{price} works?"), v("i can do {price}")}},
	{ID: "counter_again", Group: "Haggling", Name: "Counter again",
		When: "Later moves down in price. These ask instead of offering.",
		Uses: []string{"price", "was"}, Required: []string{"price"},
		Default: [][]string{v("{price}?"), v("{price} then?"), v("ok {price}?"), v("would {price} work?")}},
	{ID: "lowest", Group: "Haggling", Name: "Lowest price",
		When: "The floor. The bot won't go below this.",
		Uses: []string{"price", "was"}, Required: []string{"price"},
		Default: [][]string{v("{price}", "final offer"), v("{price} last"), v("cant go below {price}"), v("{price} only", "cant do lower sorry")}},
	{ID: "which", Group: "Haggling", Name: "Which mess?",
		When: `Answers questions like "nv?" or "kadamba?".`,
		Uses: []string{"price"}, Required: []string{"price"},
		Default: [][]string{v("{meal} for {price}"), v("its {meal}, {price}"), v("{meal}", "{price}")}},
	{ID: "other_meal", Group: "Haggling", Name: "Wrong meal",
		When:    `A buyer who already has a price asks for another meal, like "lunch?" to a dinner seller. Said once; asking for another meal again ends the chat.`,
		Default: [][]string{v("its {mealtime}"), v("its for {mealtime}"), v("this is {mealtime}")}},
	{ID: "other_meal_price", Group: "Haggling", Name: "Wrong meal, first message",
		When: "Same as Wrong meal, but the buyer hasn't heard a price yet, so it quotes one too.",
		Uses: []string{"price"}, Required: []string{"price"},
		Default: [][]string{v("its {mealtime}, {price}"), v("its for {mealtime}, {price}")}},
	{ID: "restate", Group: "Haggling", Name: "Didn't get that",
		When: "Answers the first message from a buyer the bot couldn't read.",
		Uses: []string{"price"}, Required: []string{"price"},
		Default: [][]string{v("{price}, want it?"), v("{price}, do you want it?")}},

	{ID: "nudge", Group: "Quiet buyers", Name: "Nudge",
		When:    "One follow-up when a buyer says nothing after the bot's first message.",
		Uses:    []string{"price"},
		Default: [][]string{v("yes or no?"), v("are you buying?"), v("interested?")}},
	{ID: "quiet_drop", Group: "Quiet buyers", Name: "Quiet price drop",
		When: "A lower price when the nudge also gets no answer. The bot stops waiting on them after this.",
		Uses: []string{"price", "was"}, Required: []string{"price"},
		Default: [][]string{v("can do {price} if you want it"), v("ok {price} for you"), v("{price} if you take it now")}},

	{ID: "pay", Group: "Paying", Name: "Pay now",
		When: "The buyer agreed and it's their turn. The QR promise follows in the same reply.",
		Uses: []string{"price"}, Required: []string{"price"},
		Default: [][]string{
			v("ok {price}", "pay using whatsapp pay pls, not gpay/upi"),
			v("ok {price}", "whatsapp pay pls, not gpay/upi"),
			v("ok {price}", "use whatsapp pay pls, not gpay/upi"),
			v("ok {price}", "send through whatsapp pay pls, not gpay/upi"),
			v("ok {price}", "pay on whatsapp pay pls, not gpay/upi"),
		}},
	{ID: "pay_images", Group: "Paying", Name: "Pay now, screenshots count",
		When: "Pay now, when screenshots count as payment. It asks for WhatsApp Pay without ruling out GPay or UPI. The QR promise follows in the same reply.",
		Uses: []string{"price"}, Required: []string{"price"},
		Default: [][]string{
			v("ok {price}", "pay using whatsapp pay pls"),
			v("ok {price}", "whatsapp pay pls"),
			v("ok {price}", "use whatsapp pay pls"),
			v("ok {price}", "send through whatsapp pay pls"),
			v("ok {price}", "pay on whatsapp pay pls"),
		}},
	{ID: "pay_upi", Group: "Paying", Name: "Pay on my UPI QR",
		When: "Pay now, when screenshots count and you saved a UPI ID. Your UPI QR, with the price filled in, follows as a picture. Ask for the screenshot here; no QR promise follows.",
		Uses: []string{"price"}, Required: []string{"price"},
		Default: [][]string{
			v("ok {price}", "pay on this qr and send the screenshot, ill send the meal qr after"),
			v("ok {price}", "scan this and send me the screenshot, meal qr comes right after"),
			v("ok {price}", "pay here and send the screenshot pls, then ill send the qr"),
			v("ok {price}", "send it on this qr, screenshot pls and ill send the meal qr"),
		}},
	{ID: "pay_qr", Group: "Paying", Name: "QR promise",
		When: "Ends the pay message, saying the QR comes once they've paid.",
		Default: [][]string{v("ill send the qr right after"), v("ill send the qr once youve paid"),
			v("once the payment comes through ill send the qr"), v("ill send the qr as soon as i get the payment")}},
	{ID: "pay_test", Group: "Paying", Name: "Test QR notice",
		When:    "Ends the pay message instead of the QR promise when the QR is the test placeholder.",
		Default: [][]string{v("its the test qr"), v("this ones a test qr"), v("just the test qr btw")}},
	{ID: "pay_other", Group: "Paying", Name: "Other payment app",
		When:    "A buyer wants to pay some way the bot can't see. Said once, then the chat comes to you.",
		Default: [][]string{v("only whatsapp pay sorry"), v("whatsapp pay only pls"), v("can only take whatsapp pay sorry"), v("has to be whatsapp pay sorry")}},
	{ID: "pay_other_upi", Group: "Paying", Name: "Other payment, UPI QR",
		When:    "A buyer wants to pay some way other than UPI (cash, later, bank) and the pay message carries your UPI QR. Said once, then the chat comes to you.",
		Default: [][]string{v("only upi sorry"), v("upi only pls"), v("has to be upi sorry"), v("can only take upi sorry")}},
	{ID: "no_screenshots", Group: "Paying", Name: "Screenshot sent",
		When: "A buyer sends an image while paying and screenshots don't count as payment.",
		Default: [][]string{v("cant check screenshots, pay using whatsapp pay pls"), v("cant verify screenshots, whatsapp pay pls"),
			v("cant confirm payment from a screenshot, use whatsapp pay pls")}},
	{ID: "pay_number", Group: "Paying", Name: "No WhatsApp Pay",
		When: "Screenshots count as payment and the paying buyer says they don't have WhatsApp Pay or asks to use another app. Said once.",
		Default: [][]string{v("send to my number then"), v("just send it to my number"), v("pay to my number then"),
			v("upi to this number works"), v("send it to this number on gpay/upi")}},
	{ID: "ask_screenshot", Group: "Paying", Name: "Paid, no screenshot",
		When: "Screenshots count as payment and the paying buyer says they paid but sent no screenshot. Said once; their next payment screenshot gets the QR.",
		Default: [][]string{v("can u send payment screenshot"), v("send the payment screenshot pls"), v("can you send a screenshot of the payment"),
			v("pls send the screenshot")}},
	{ID: "pay_short", Group: "Paying", Name: "Paid too little",
		When: "The bot reads payment screenshots, and the buyer's add up to less than the price. The meal QR waits for the rest.",
		Uses: []string{"paid", "price"}, Required: []string{"short"},
		Default: [][]string{v("got {paid}, {short} more pls"), v("only {paid} came through, send {short} more"), v("thats {paid}, need {short} more pls")}},
	{ID: "pay_unclear", Group: "Paying", Name: "Not a payment screenshot",
		When: "The bot reads payment screenshots, and the buyer's image doesn't show a finished payment to you: another picture, a pending or failed payment, someone else's name, or too blurry to read. Said at most twice.",
		Default: [][]string{v("cant see a payment to me in that, can u send the payment screenshot"), v("that doesnt look like the payment screenshot, can u send it"),
			v("cant see the payment in that, send the screenshot from your upi app pls")}},

	{ID: "queued", Group: "Queue", Name: "Someone's paying",
		When: "The buyer agreed but another buyer holds the turn.",
		Uses: []string{"price", "wait"}, Required: []string{"price"},
		Default: [][]string{v("ok {price}", "someone else is paying rn, if they dont in {wait} its yours")}},
	{ID: "queued_handoff", Group: "Queue", Name: "Someone's buying from you",
		When: "The buyer agreed while you're selling to another buyer by hand.",
		Uses: []string{"price"}, Required: []string{"price"},
		Default: [][]string{v("ok {price}", "someone else is buying rn, ill msg u if it falls through")}},
	{ID: "outbid", Group: "Queue", Name: "Outbid",
		When:    "The buyer was asked to pay, then someone offered more.",
		Uses:    []string{"wait"},
		Default: [][]string{v("wait dont pay yet", "someone offered more, if they dont pay in {wait} ill msg u")}},
	{ID: "outbid_handoff", Group: "Queue", Name: "Outbid, handed to you",
		When:    "Someone offered more than a buyer the bot handed to you.",
		Default: [][]string{v("wait dont pay yet", "someone offered more, ill msg u if it falls through")}},
	{ID: "dropped", Group: "Queue", Name: "Dropped",
		When:    "Autopilot dropped this buyer before they paid.",
		Default: [][]string{v("wait dont pay", "cant do this one sorry")}},

	{ID: "sold", Group: "Ending", Name: "Sold",
		When: "The meal went to someone else. One message only.", Single: true, NeedsText: true,
		Default: [][]string{v("sold sorry"), v("already sold sorry"), v("someone else bought it sorry"), v("its sold bro sorry")}},
}

// Find returns the slot with id.
func Find(id string) (Slot, bool) {
	for _, s := range Slots {
		if s.ID == id {
			return s, true
		}
	}
	return Slot{}, false
}

// Book is an account's own wording: variants by slot ID. A slot it leaves
// out uses the default.
type Book map[string][][]string

// Variants returns slot's variants: b's when it has some, else the
// defaults.
func (b Book) Variants(slot string) [][]string {
	if vs := b[slot]; len(vs) > 0 {
		return vs
	}
	s, _ := Find(slot)
	return s.Default
}

// Limits on what a book may hold.
const (
	MaxVariants = 20
	MaxParts    = 4
	MaxLen      = 300
)

var (
	varRe    = regexp.MustCompile(`\{([^{}]*)\}`)
	optionRe = regexp.MustCompile(`\[([^\[\]]*)\]`)
	spaces   = regexp.MustCompile(` {2,}`)
)

// Clean trims every part and drops slots set to exactly their defaults,
// so a later change to a default reaches accounts that never edited it.
func (b Book) Clean() Book {
	out := Book{}
	for id, vs := range b {
		var keep [][]string
		for _, parts := range vs {
			var p []string
			for _, s := range parts {
				p = append(p, strings.TrimSpace(s))
			}
			keep = append(keep, p)
		}
		if s, ok := Find(id); ok && same(keep, s.Default) {
			continue
		}
		out[id] = keep
	}
	return out
}

// Check reports the first problem in b, in words for the settings page.
func (b Book) Check() error {
	for _, s := range Slots {
		vs, ok := b[s.ID]
		if !ok {
			continue
		}
		if len(vs) == 0 {
			return fmt.Errorf("%s: needs at least one variant", s.Name)
		}
		if len(vs) > MaxVariants {
			return fmt.Errorf("%s: at most %d variants", s.Name, MaxVariants)
		}
		for i, parts := range vs {
			if err := s.checkVariant(parts); err != nil {
				return fmt.Errorf("%s, variant %d: %w", s.Name, i+1, err)
			}
		}
	}
	for id := range b {
		if _, ok := Find(id); !ok {
			return fmt.Errorf("there's no message called %q", id)
		}
	}
	return nil
}

func (s Slot) checkVariant(parts []string) error {
	switch {
	case len(parts) == 0:
		return fmt.Errorf("is empty")
	case s.Single && len(parts) > 1:
		return fmt.Errorf("must be a single message")
	case len(parts) > MaxParts:
		return fmt.Errorf("at most %d messages", MaxParts)
	}
	allowed := s.Allowed()
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("has an empty message")
		}
		if len(p) > MaxLen {
			return fmt.Errorf("a message is over %d characters", MaxLen)
		}
		if err := checkBrackets(p); err != nil {
			return err
		}
		for _, m := range varRe.FindAllStringSubmatch(p, -1) {
			if !contains(allowed, m[1]) {
				return fmt.Errorf("{%s} isn't available here", m[1])
			}
		}
		if strings.Count(p, "{") != strings.Count(p, "}") || len(varRe.FindAllString(p, -1)) != strings.Count(p, "{") {
			return fmt.Errorf("has a { without its }")
		}
		if strings.TrimSpace(optionRe.ReplaceAllString(p, "")) == "" {
			return fmt.Errorf("a message is all optional, so it could come out empty")
		}
		if s.NeedsText && len(strings.TrimSpace(varRe.ReplaceAllString(optionRe.ReplaceAllString(p, ""), ""))) < 3 {
			return fmt.Errorf("needs some words of its own, not just variables")
		}
	}
	all := strings.Join(parts, "\n")
	for _, r := range s.Required {
		if !strings.Contains(all, "{"+r+"}") {
			return fmt.Errorf("needs {%s} so the buyer sees it", r)
		}
	}
	return nil
}

// checkBrackets refuses nested or unclosed [ ].
func checkBrackets(p string) error {
	open := false
	for _, c := range p {
		switch c {
		case '[':
			if open {
				return fmt.Errorf("has [ ] inside [ ]")
			}
			open = true
		case ']':
			if !open {
				return fmt.Errorf("has a ] without its [")
			}
			open = false
		}
	}
	if open {
		return fmt.Errorf("has a [ without its ]")
	}
	return nil
}

// Values are the variables' values for one message.
type Values map[string]string

// Fill renders tmpl with vals. An optional bit with any empty variable in
// it is dropped, and so is an unknown variable.
func Fill(tmpl string, vals Values) string {
	out := optionRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		inner := m[1 : len(m)-1]
		for _, v := range varRe.FindAllStringSubmatch(inner, -1) {
			if vals[v[1]] == "" {
				return ""
			}
		}
		return inner
	})
	out = varRe.ReplaceAllStringFunc(out, func(m string) string { return vals[m[1:len(m)-1]] })
	return strings.TrimSpace(spaces.ReplaceAllString(out, " "))
}

// Matches reports whether text could be tmpl filled with some values.
func Matches(tmpl, text string) bool {
	var re strings.Builder
	re.WriteString("^")
	rest := tmpl
	for {
		loc := optionRe.FindStringIndex(rest)
		if loc == nil {
			re.WriteString(pattern(rest))
			break
		}
		re.WriteString(pattern(rest[:loc[0]]))
		re.WriteString("(?:" + pattern(rest[loc[0]+1:loc[1]-1]) + ")?")
		rest = rest[loc[1]:]
	}
	re.WriteString("$")
	r, err := regexp.Compile(re.String())
	return err == nil && r.MatchString(text)
}

// pattern is s as a regexp: variables match anything and, since Fill
// squeezes spaces, a space matches any number of them.
func pattern(s string) string {
	lit := func(t string) string { return strings.ReplaceAll(regexp.QuoteMeta(t), " ", " *") }
	var b strings.Builder
	last := 0
	for _, loc := range varRe.FindAllStringIndex(s, -1) {
		b.WriteString(lit(s[last:loc[0]]))
		b.WriteString("(?s:.*?)")
		last = loc[1]
	}
	b.WriteString(lit(s[last:]))
	return b.String()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func same(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.Join(a[i], "\x00") != strings.Join(b[i], "\x00") || len(a[i]) != len(b[i]) {
			return false
		}
	}
	return true
}
