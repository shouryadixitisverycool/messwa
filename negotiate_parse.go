package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Buyer messages are read with fixed rules, built from a study of real mess
// trades in DMs. The text is untrusted: all it can do is pick one of these
// intents and, for an offer, one number. Every reply comes from a template,
// and the floor is checked again in the store.

type intentKind int

const (
	intentUnknown  intentKind = iota
	intentOpener              // "available?", "price?", "buy", "?": wants it, or the price
	intentQuestion            // "nv?", "kadamba?": which mess or diet
	intentOK                  // "ok", "fine", "done": a yes only right after a price
	intentAccept              // "same number?", "gpay?", "paying": a yes whatever came before
	intentLower               // "too much", "negotiable?": cheaper, no number
	intentPayOther            // "upi id?", "send qr": wants to pay some other way
	intentOffer               // "40?", "buy @50", "45 final": names a price
	intentPaid                // "paid", "sent", "💸✅": says they paid
	intentPass                // "nvm", "sorry bought": not buying
)

// intentRank orders intents when one burst of messages has several. A pass
// beats everything; a later offer replaces an earlier one.
var intentRank = map[intentKind]int{
	intentUnknown: 0, intentOpener: 1, intentOK: 2, intentQuestion: 3, intentAccept: 4,
	intentLower: 5, intentPayOther: 6, intentOffer: 7, intentPaid: 8, intentPass: 9,
}

type intent struct {
	kind  intentKind
	offer int  // rupees, for intentOffer
	firm  bool // "45 final", "max 45", "last 45"
	done  bool // intentOK from "done" or "dn", which also means "paid" once agreed
}

// combineIntents reads a burst of messages as one: the highest-ranked
// intent wins, and among equals the latest.
func combineIntents(texts []string) intent {
	var out intent
	for _, t := range texts {
		in := parseBuyer(t)
		if intentRank[in.kind] >= intentRank[out.kind] {
			out = in
		}
	}
	return out
}

// squeeze collapses repeated letters, so "okkk", "plsss" and "broooo" read
// as "ok", "pls" and "bro". Vocabulary is squeezed the same way.
func squeeze(w string) string {
	var b strings.Builder
	var last rune
	for _, r := range w {
		if r == last && r >= 'a' && r <= 'z' {
			continue
		}
		b.WriteRune(r)
		last = r
	}
	return b.String()
}

type vocab struct {
	words   map[string]bool
	phrases []string
}

func newVocab(entries ...string) vocab {
	v := vocab{words: map[string]bool{}}
	for _, e := range entries {
		fields := strings.Fields(e)
		for i := range fields {
			fields[i] = squeeze(fields[i])
		}
		if len(fields) == 1 {
			v.words[fields[0]] = true
		} else {
			v.phrases = append(v.phrases, " "+strings.Join(fields, " ")+" ")
		}
	}
	return v
}

// in reports whether any entry appears in m as whole words.
func (v vocab) in(m msgWords) bool {
	for _, w := range m.words {
		if v.words[w] {
			return true
		}
	}
	for _, p := range v.phrases {
		if strings.Contains(m.joined, p) {
			return true
		}
	}
	return false
}

// only reports whether every word of m is in v or filler.
func (v vocab) only(m msgWords) bool {
	if len(m.words) == 0 {
		return false
	}
	for _, w := range m.words {
		if !v.words[w] && !filler.words[w] {
			return false
		}
	}
	return true
}

var (
	filler = newVocab("bro", "bhai", "bhaiya", "yaar", "yar", "sir", "dude", "man", "boss", "ji", "hai", "then", "pls", "plz", "please", "so",
		"hi", "hey", "hello", "oh", "ohh", "ah", "um", "hm", "hmm", "lol", "na", "re", "and", "the", "it", "is", "its", "it's", "that", "this", "just", "a", "i", "im")

	// Numbers next to these words are not offers: "sold at 60", "people
	// are selling at 70", "i bought for 20 from someone else", "i paid 30".
	notOfferContext = newVocab("sold", "bought", "buyed", "purchased", "paid", "sent", "sending", "transferred", "getting", "selling",
		"norm", "milra", "milrha", "mil", "people", "someone", "somebody", "others", "everyone", "group", "roll", "number", "no.", "phone")
	paidWords = newVocab("paid", "payed", "sent", "transferred", "dn", "payment done", "money sent", "done paying", "sent money", "sending money",
		"paid via", "paid using", "sent it")
	passWords = newVocab("nvm", "nevermind", "never mind", "not needed", "not interested", "no thanks", "no thank you", "no thanx", "no thnx",
		"no sorry", "leave it", "lite", "chodo", "chhodo", "rehne do", "rehne de", "rehnedo", "rehen do", "i'll pass", "ill pass", "pass",
		"cancel", "bought", "purchased", "got from", "got another", "got one", "got other", "got elsewhere", "got it from", "already got",
		"mil gaya", "mil gya", "milgaya", "mil gayi", "not required", "dont want", "don't want", "do not want", "no need", "skip")
	refuseFirst   = newVocab("no", "nope", "nah", "na", "nahi", "nhi")
	payOtherWords = newVocab("upi id", "upi qr", "qr code", "scanner", "scan", "send qr", "send your qr", "send the qr", "your qr", "payment qr",
		"cant pay", "can't pay", "cannot pay", "not working", "cash", "pay later", "later", "tomorrow", "tmrw", "tmr", "account number",
		"bank", "send upi", "send number", "number send", "upi not")
	lowerWords = newVocab("too much", "to much", "too high", "too costly", "costly", "expensive", "mehenga", "mehnga", "mehanga", "kam", "kum",
		"less", "lower", "reduce", "discount", "negotiable", "nego", "negotiate", "best price", "final price", "last price", "lowest",
		"half", "fair", "cmon", "c'mon", "come on", "thoda", "cheaper", "cheap", "bargain", "selling at", "norm", "milra", "people",
		"others", "someone", "for old", "kuch kam", "high", "much?")
	pleading    = newVocab("pls", "plz", "please", "plis", "pwease", "pleaseee", "pleez")
	acceptWords = newVocab("same number", "same no", "samenumber", "same num", "same nmbr", "this number", "this no", "this num",
		"pay here", "pay on this", "pay to this", "pay this", "pay on same", "gpay", "g pay", "google pay", "phonepe", "phone pe", "paytm",
		"upi", "upi?", "whatsapp pay", "paying", "sending", "send", "i'll take", "ill take", "i will take", "im taking", "i'm taking",
		"taking it", "take it", "i take", "deal", "bhejo", "bhej", "bhejta", "karta hu", "kar raha", "kr rha", "doing")
	okWords = newVocab("ok", "okay", "okie", "oki", "okey", "okayy", "k", "kk", "fine", "cool", "works", "work", "sure", "done", "alright",
		"alr", "aight", "awesome", "great", "haa", "haan", "han", "ha", "yes", "ya", "yeah", "yep", "yup", "yea", "yo", "bet", "chalega",
		"chalo", "theek", "thik", "thk", "perfect", "wait", "sec", "fast", "bolo", "tell", "quick", "dn", "agreed", "noted", "go")
	questionWords = newVocab("veg", "nonveg", "non", "nv", "egg", "chicken", "paneer", "kadamba", "kadamb", "kdmb", "kadam", "yuktahar",
		"yukta", "yukthahar", "palash", "bakul", "which", "mess", "extras", "extra", "menu", "what's", "whats", "kya", "special")
	openerWords = newVocab("available", "availble", "avaliable", "availiable", "avail", "avl", "price", "how much", "much", "cost", "rate",
		"buy", "want", "wnt", "need", "interested", "intrested", "still", "there", "selling", "sell", "for", "token", "qr", "sold",
		"lunch", "dinner", "breakfast", "bf", "snacks", "nashta", "up", "have", "kitne", "kitna", "how", "cost?")
	firmWords  = newVocab("final", "max", "maximum", "last", "lowest", "at most", "atmost", "and done", "done", "only", "fix", "fixed")
	rangeWords = newVocab("or", "to")
)

type msgWords struct {
	words  []string // squeezed lowercase words
	joined string   // " w1 w2 ... "
}

var (
	wordRe  = regexp.MustCompile(`[a-z']+`)
	rangeRe = regexp.MustCompile(`\d\s*-\s*\d`)
	// Currency marks glued to a number: "80rs", "rs80", "₹40", "50/-".
	currencyAfter  = regexp.MustCompile(`(\d)\s*(rs|inr|rupees|rupee|bucks|/-)`)
	currencyBefore = regexp.MustCompile(`(rs\.?|inr)\s*(\d)`)
)

var numberWords = map[string]int{
	"ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17,
	"eighteen": 18, "nineteen": 19, "twenty": 20, "thirty": 30, "forty": 40, "fourty": 40, "fifty": 50, "sixty": 60,
	"seventy": 70, "eighty": 80, "ninety": 90, "hundred": 100,
}
var unitWords = map[string]int{"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9}

// wordNumbers finds prices written as words: "sixty", "sixty five",
// "hundred", "two hundred".
func wordNumbers(words []string) []int {
	var out []int
	for i := 0; i < len(words); i++ {
		w := words[i]
		if n, ok := numberWords[w]; ok {
			if n == 100 && i > 0 {
				if u, ok := unitWords[words[i-1]]; ok {
					n = u * 100
				}
			} else if n >= 20 && i+1 < len(words) {
				if u, ok := unitWords[words[i+1]]; ok {
					n += u
					i++
				}
			}
			out = append(out, n)
		}
	}
	return out
}

// parseBuyer reads one buyer message.
func parseBuyer(text string) intent {
	raw := strings.ToLower(strings.TrimSpace(text))
	paidMark := strings.ContainsAny(raw, "💸✅")
	raw = strings.NewReplacer("₹", " rs ", "’", "'", "@", " @ ", "\n", " ").Replace(raw)
	raw = currencyAfter.ReplaceAllString(raw, "$1 rs ")
	raw = currencyBefore.ReplaceAllString(raw, "rs $2")

	var rawWords []string
	m := msgWords{joined: " "}
	for _, w := range wordRe.FindAllString(raw, -1) {
		w = strings.Trim(w, "'")
		if w == "" {
			continue
		}
		rawWords = append(rawWords, w)
		m.words = append(m.words, squeeze(w))
		m.joined += squeeze(w) + " "
	}

	numbers := digitNumbers(raw)
	numbers = append(numbers, wordNumbers(rawWords)...)

	if notOfferContext.in(m) {
		if len(numbers) > 0 && (paidWords.in(m) || strings.Contains(m.joined, " sending ")) {
			return intent{kind: intentPaid}
		}
		numbers = nil
	}
	if len(numbers) > 0 {
		n, ok := pickNumber(numbers, raw, m)
		if !ok {
			return intent{kind: intentUnknown}
		}
		return intent{kind: intentOffer, offer: n, firm: firmWords.in(m)}
	}

	switch {
	case paidMark || paidWords.in(m):
		return intent{kind: intentPaid}
	case passWords.in(m):
		return intent{kind: intentPass}
	case len(m.words) > 0 && refuseFirst.words[m.words[0]] && !lowerWords.in(m) && !openerWords.in(m):
		return intent{kind: intentPass}
	case payOtherWords.in(m):
		return intent{kind: intentPayOther}
	case lowerWords.in(m), pleading.only(m) && !okWords.in(m):
		return intent{kind: intentLower}
	case acceptWords.in(m):
		return intent{kind: intentAccept}
	case openerWords.in(m):
		return intent{kind: intentOpener}
	case questionWords.in(m):
		return intent{kind: intentQuestion}
	case okWords.in(m):
		return intent{kind: intentOK, done: strings.Contains(m.joined, " done ") || strings.Contains(m.joined, " dn ")}
	case len(m.words) == 0 && !strings.ContainsAny(raw, "0123456789"):
		// "?", ".", "......": an opener with no words.
		return intent{kind: intentOpener}
	}
	return intent{kind: intentUnknown}
}

// digitNumbers finds prices written in digits. A number stands on its own:
// "40?", "@50", "rs 25", "45 final". One glued to letters ("5his", "n9",
// "x2"), with a decimal ("37.21") or longer than 3 digits (a phone or roll
// number) is not a price.
func digitNumbers(raw string) []int {
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	isLetter := func(c byte) bool { return c >= 'a' && c <= 'z' }
	var out []int
	for i := 0; i < len(raw); {
		if !isDigit(raw[i]) {
			i++
			continue
		}
		j := i
		for j < len(raw) && isDigit(raw[j]) {
			j++
		}
		before, after := byte(' '), byte(' ')
		if i > 0 {
			before = raw[i-1]
		}
		if j < len(raw) {
			after = raw[j]
		}
		decimal := (after == '.' && j+1 < len(raw) && isDigit(raw[j+1])) || (before == '.' && i >= 2 && isDigit(raw[i-2]))
		if !decimal && !isLetter(before) && !isLetter(after) && j-i <= 3 {
			if n, err := strconv.Atoi(raw[i:j]); err == nil && n > 0 {
				out = append(out, n)
			}
		}
		i = j
	}
	return out
}

// pickNumber chooses the offer among the numbers in one message. One
// number, or the same number twice, is the offer. Two numbers joined by
// "-", "or" or "to" are a range, and the higher end counts. Anything else
// is ambiguous.
func pickNumber(numbers []int, raw string, m msgWords) (int, bool) {
	first := numbers[0]
	same := true
	for _, n := range numbers[1:] {
		same = same && n == first
	}
	if same {
		return first, true
	}
	if len(numbers) == 2 && (rangeRe.MatchString(raw) || rangeWords.in(m)) {
		return max(numbers[0], numbers[1]), true
	}
	return 0, false
}
