package main

import (
	"regexp"
	"slices"
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
	firm  bool // "45 final", "max 45", "last 45", "can't go above 50"
	done  bool // intentOK from "done" or "dn", which also means "paid" once agreed
	// upi marks a buyer who has no WhatsApp Pay or names a UPI app ("i
	// can do upi", "dont have whatsapp pay", "send qr"): a payment to the
	// number or UPI QR they can screenshot.
	upi bool
	// cantPayApp marks replies that mean "I don't have / can't do that
	// payment method" once a payment prompt has gone out ("ion have it",
	// "i do not have sorry", "cannot do"), with no price or explicit pass.
	cantPayApp bool
	// apologyOnly marks an intentPass that came solely from an apology
	// ("sorry", "i do not have sorry"), not an explicit decline like "nvm".
	apologyOnly bool
	// bought marks an intentPass from a buyer who got a meal elsewhere
	// ("bought already", "got from other"): no lower price brings them back.
	bought bool
	// boughtOnly marks an intentPass that came solely from bought-elsewhere
	// words (not an explicit refusal like "nvm"), so a follow-up "I want
	// another for a friend" in the same burst can supersede it.
	boughtOnly bool
	// buyingMore marks a non-pass message that asks for another meal ("want
	// another", "for a friend") despite an earlier purchase.
	buyingMore bool
	// multi marks a request/offer for more than one meal ("120 for both?",
	// "2 meals for 120"), which must go to you instead of agreeing for one.
	multi bool
	// singleMeal marks an explicit single-meal phrase ("for one", "1 meal")
	// that overrides an earlier multi-meal cue in the same burst.
	singleMeal bool
	// dismiss marks a polite sign-off ("np", "nw", "oops", "no worries",
	// "next time") that ends the chat cleanly once the bot stated its lowest.
	dismiss bool
	// lower marks a price objection accompanying an intentPayOther message
	// or burst ("too much, can't do whatsapp pay"), so UPI acceptance does
	// not override haggling.
	lower bool
	// meals are the meals the buyer named, such as "breakfast" in "i
	// texted for breakfast". Naming only other meals than the one on sale
	// ends the chat (see decide).
	meals []string
}

// name is how the dashboard shows what the bot read: the meaning only,
// never the words.
func (in intent) name() string {
	n := map[intentKind]string{
		intentUnknown: "unknown", intentOpener: "opener", intentQuestion: "question", intentOK: "ok",
		intentAccept: "accept", intentLower: "lower", intentPayOther: "pay_other", intentOffer: "offer",
		intentPaid: "paid", intentPass: "pass",
	}[in.kind]
	if in.kind == intentOffer && in.firm {
		n = "offer_final"
	}
	if in.kind == intentPass && in.bought {
		n = "pass_bought"
	}
	return n
}

// combineIntents reads a burst of messages as one: the highest-ranked
// intent wins, and among equals the latest. A pass is bought if any
// message in the burst says so: "sorry bought" then "nvm" is still bought.
func combineIntents(texts []string) intent {
	var out intent
	bought, upi, cantPayApp, burstMulti, hasOffer, hasLower, hasPayOther, dismiss := false, false, false, false, false, false, false, false
	explicitPass := false
	var named []string
	// An apology-only pass ("sorry", "i do not have sorry") is a fallback
	// that only beats unknown/opener/ok, matching parseKind's precedence so
	// a separate apology message never overrides a price objection or offer.
	rank := func(in intent) int {
		if in.apologyOnly {
			return intentRank[intentOK]
		}
		return intentRank[in.kind]
	}
	ins := make([]intent, len(texts))
	lastBuyingMore := -1
	for i, t := range texts {
		ins[i] = parseBuyer(t)
		if ins[i].buyingMore {
			lastBuyingMore = i
		}
	}
	for i, in := range ins {
		if in.boughtOnly && i < lastBuyingMore {
			continue
		}
		bought = bought || in.bought
		upi = upi || in.upi
		cantPayApp = cantPayApp || in.cantPayApp
		dismiss = dismiss || in.dismiss
		if in.kind == intentPass && !in.apologyOnly {
			explicitPass = true
		}
		if in.kind == intentLower || in.lower {
			hasLower = true
		}
		if in.kind == intentPayOther {
			hasPayOther = true
		}
		if in.kind == intentOffer {
			hasOffer = true
			if in.multi {
				burstMulti = true
			} else if in.singleMeal {
				burstMulti = false
			}
		} else if in.multi {
			burstMulti = true
		}
		for _, m := range in.meals {
			if !slices.Contains(named, m) {
				named = append(named, m)
			}
		}
		if rank(in) >= rank(out) {
			out = in
		}
	}
	out.bought = out.kind == intentPass && bought
	out.upi = upi
	out.lower = out.kind == intentPayOther && hasLower
	out.apologyOnly = out.kind == intentPass && !explicitPass && !out.bought && out.apologyOnly
	out.cantPayApp = cantPayApp && !hasLower && !hasOffer && !hasPayOther && (out.kind == intentUnknown || out.apologyOnly)
	if out.kind == intentOffer {
		if !out.multi && burstMulti {
			out.multi = true
		}
	} else if !hasOffer && out.kind != intentPass && out.kind != intentPaid {
		out.multi = burstMulti
	}
	out.dismiss = (out.kind == intentUnknown || out.kind == intentLower) && dismiss
	out.meals = named
	return out
}

// wantsOtherMeal reports whether in names meals and meal isn't one of them.
func (in intent) wantsOtherMeal(meal string) bool {
	return meal != "" && len(in.meals) > 0 && !slices.Contains(in.meals, meal)
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

// only reports whether every word of m is in v or filler, and at least one
// word is in v itself (pure filler like "hello" or "oh" does not match).
func (v vocab) only(m msgWords) bool {
	matched := false
	for _, w := range m.words {
		if v.words[w] {
			matched = true
			continue
		}
		if !filler.words[w] {
			return false
		}
	}
	return matched
}

var (
	filler = newVocab("bro", "bhai", "bhaiya", "yaar", "yar", "sir", "dude", "man", "boss", "ji", "hai", "then", "pls", "plz", "please", "so",
		"hi", "hey", "hello", "hlo", "oh", "ohh", "ah", "um", "hm", "hmm", "lol", "na", "re", "and", "the", "it", "is", "its", "it's", "that", "this", "just", "a", "i", "im")

	// Numbers next to these words are not offers: "sold at 60", "people
	// are selling at 70", "i bought for 20 from someone else", "i paid 30",
	// "got it for 50", "i already got @25".
	notOfferContext = newVocab("got it for", "got for", "got one for", "already got", "alrdy got", "got it already", "got already",
		"sold", "bought", "buyed", "purchased", "paid", "sent", "sending", "transferred", "getting", "selling",
		"norm", "milra", "milrha", "mil", "people", "someone", "somebody", "others", "everyone", "group", "roll", "number", "no.", "phone")
	paidWords = newVocab("paid", "payed", "sent", "transferred", "dn", "payment done", "money sent", "done paying", "sent money", "sending money",
		"paid via", "paid using", "sent it")
	passWords = newVocab("nvm", "nevermind", "never mind", "not needed", "not interested", "no thanks", "no thank you", "no thanx", "no thnx",
		"no sorry", "leave it", "lite", "chodo", "chhodo", "rehne do", "rehne de", "rehnedo", "rehen do", "i'll pass", "ill pass", "pass",
		"cancel", "not required", "dont want", "don't want", "do not want", "donot want", "no need", "skip",
		"not req", "not reqd", "no req", "nt req", "not reqired",
		"dont need", "don't need", "do not need", "donot need", "dnt need", "nahi chahiye", "nhi chahiye",
		// Polite withdrawals can start with "okay"; read the refusal first.
		"sorry then", "sry then")
	// boughtPassWords decline because the buyer already got a meal elsewhere.
	// Kept separate from passWords so wantsAnother ("i bought mine for 50,
	// i want another for a friend") suppresses only bought-elsewhere passes,
	// never explicit refusals like "nvm" or "don't need another".
	boughtPassWords = newVocab("bought", "buyed", "purchased", "got it for", "got for", "got one for", "got from", "got another", "got one", "got other",
		"got elsewhere", "got it from", "already got", "alrdy got", "got it already", "got already",
		"mil gaya", "mil gya", "milgaya", "mil gayi",
		"sorry got it", "sry got it", "sorry i got it", "sry i got it")
	// wontBuyWords reject buying outright when no price is named ("won't buy"),
	// or reject a specific price ("won't buy at 90" -> intentLower).
	wontBuyWords = newVocab("wont buy", "won't buy", "will not buy")
	// wantsAnother marks a buyer who already bought one meal for themselves
	// and explicitly wants another ("i bought mine for 50, i want another for
	// a friend though"). negatedAnother keeps "don't need another" a decline.
	wantsAnother = newVocab("want another", "need another", "take another", "taking another", "buy another", "get another",
		"want one more", "need one more", "take one more", "buy one more", "for a friend", "for my friend", "for friend")
	negatedAnother = newVocab("dont want another", "don't want another", "do not want another", "donot want another",
		"dont need another", "don't need another", "do not need another", "donot need another", "no need another", "not need another")
	// Within a pass, these mean the buyer has a meal already. Bare "got it"
	// alone stays unknown (it can mean "understood"); explicitGotItBought
	// checks for clear bought-elsewhere markers around "got it".
	boughtWords = newVocab("bought", "buyed", "purchased", "got for", "got from", "got another", "got one", "got other", "got elsewhere",
		"got it", "already got", "alrdy got", "got it already", "got already", "mil gaya", "mil gya", "milgaya", "mil gayi")
	gotItBoughtCues = newVocab("mb", "already", "alrdy", "told u", "told you", "sry", "sorry")
	refuseFirst     = newVocab("no", "nope", "nah", "na", "nahi", "nhi")
	// An apology with nothing else to say is a no: "okay sorry then" after
	// "cant go below 115". One that excuses something isn't: "sorry for the
	// late reply, ok".
	apology        = newVocab("sorry", "sry", "sorri", "soz")
	apologyExcuses = newVocab("sorry for", "sry for", "sorry late", "sry late", "sorry was", "sry was")
	// dismissOnly and dismissPhrases are polite sign-offs ("np", "nw", "oops",
	// "no worries", "next time") that end the chat once the bot stated its lowest.
	dismissOnly    = newVocab("np", "nw", "mb", "isok", "oops")
	dismissPhrases = newVocab("next time", "no worries", "no prob", "no problem", "no issue")
	payOtherWords  = newVocab("upi id", "upi qr", "qr code", "scanner", "scan", "send qr", "send your qr", "send the qr", "your qr", "payment qr", "pay on qr",
		"cant pay", "can't pay", "cannot pay", "not working", "cash", "pay later", "later", "tomorrow", "tmrw", "tmr", "account number",
		"bank", "send upi", "send number", "number send", "upi not", "other way", "other ways", "another way", "different way", "other method",
		"other mode", "some other way", "dont have whatsapp pay", "don't have whatsapp pay", "do not have whatsapp pay", "no whatsapp pay",
		"not have whatsapp pay", "dont use whatsapp pay", "don't use whatsapp pay", "whatsapp pay not", "whatsapp pay isnt",
		"whatsapp pay isn't", "whatsapp pay doesnt", "whatsapp pay doesn't", "whatsapp pay nahi", "whatsapp pay nhi", "dont have wa pay",
		"no wa pay", "dont have whatsapp payment", "don't have whatsapp payment",
		"dont have wtsapp pay", "don't have wtsapp pay", "do not have wtsapp pay", "no wtsapp pay",
		"dont have whatsapp pag", "don't have whatsapp pag", "no whatsapp pag",
		"cant do whatsapp pay", "can't do whatsapp pay", "cannot do whatsapp pay", "famapp", "fampay")
	// upiWords mean the buyer can pay the number or UPI QR, or has no
	// WhatsApp Pay; see intent.upi.
	upiWords = newVocab("upi", "upi?", "upi id", "upi qr", "send upi", "gpay", "g pay", "google pay", "phonepe", "phone pe", "paytm", "bhim", "famapp", "fampay",
		"scanner", "scan", "qr code", "send qr", "send the qr", "send your qr", "your qr", "payment qr", "pay on qr", "send number", "number send",
		"dont have whatsapp pay", "don't have whatsapp pay", "do not have whatsapp pay", "no whatsapp pay",
		"not have whatsapp pay", "dont use whatsapp pay", "don't use whatsapp pay", "whatsapp pay not", "whatsapp pay isnt",
		"whatsapp pay isn't", "whatsapp pay doesnt", "whatsapp pay doesn't", "whatsapp pay nahi", "whatsapp pay nhi", "dont have wa pay",
		"no wa pay", "dont have whatsapp payment", "don't have whatsapp payment",
		"dont have wtsapp pay", "don't have wtsapp pay", "do not have wtsapp pay", "no wtsapp pay",
		"dont have whatsapp pag", "don't have whatsapp pag", "no whatsapp pag",
		"cant do whatsapp pay", "can't do whatsapp pay", "cannot do whatsapp pay")
	// cantPayAppWords mean "I don't have / can't do WhatsApp Pay" when sent
	// right after a payment prompt, without naming a price or explicit pass.
	cantPayAppWords = newVocab("cannot do", "cant do", "can't do", "dont have it", "don't have it", "do not have it",
		"do not have sorry", "do not have sry", "dont have sorry", "don't have sorry", "dont have sry", "don't have sry", "ion have")
	// priceObjectionWords are unambiguous price objections used to prevent an
	// accompanying payment-method phrase ("too much, can't do whatsapp pay")
	// from agreeing or sending payment instructions, without tripping on words
	// like "less balance", "half later" or "ask someone else".
	priceObjectionWords = newVocab("too much", "to much", "too high", "too costly", "costly", "expensive",
		"mehenga", "mehnga", "mehanga", "cheaper", "cheap", "discount", "reduce", "lower", "kam", "kuch kam",
		"negotiable", "nego", "negotiate")
	lowerWords = newVocab("too much", "to much", "too high", "too costly", "costly", "expensive", "mehenga", "mehnga", "mehanga", "kam", "kum",
		"less", "lower", "reduce", "discount", "negotiable", "nego", "negotiate", "best price", "final price", "last price", "lowest",
		"half", "fair", "cmon", "c'mon", "come on", "thoda", "cheaper", "cheap", "bargain", "selling at", "getting it for", "getting for",
		"norm", "milra", "people", "others", "someone", "for old", "kuch kam", "high", "much?")
	pleading    = newVocab("pls", "plz", "please", "plis", "pwease", "pleaseee", "pleez")
	acceptWords = newVocab("same number", "same no", "samenumber", "same num", "same nmbr", "samr no", "samr number",
		"this number", "this no", "this num", "this whatsapp no", "this whatsapp number", "whatsapp no", "whatsapp number",
		"pay here", "pay on this", "pay to this", "pay this", "pay on same", "gpay", "g pay", "google pay", "phonepe", "phone pe", "paytm",
		"upi", "upi?", "whatsapp pay", "wtsapp pay", "whatsapp pag", "paying", "sending", "send", "i'll take", "ill take", "i will take", "im taking", "i'm taking",
		"taking it", "take it", "i take", "deal", "bhejo", "bhej", "bhejta", "karta hu", "kar raha", "kr rha", "doing")
	okWords = newVocab("ok", "okay", "okie", "oki", "okey", "okayy", "okies", "okz", "k", "kk", "fine", "cool", "works", "work", "sure", "done", "alright",
		"alr", "alryt", "alrite", "alrt", "aryt", "aight", "awesome", "great", "haa", "haan", "han", "ha", "yes", "ya", "yeah", "yep", "yup", "yea", "yo", "bet", "chalega",
		"chalo", "theek", "thik", "thk", "perfect", "wait", "sec", "fast", "bolo", "tell", "quick", "dn", "agreed", "noted", "go")
	questionWords = newVocab("veg", "nonveg", "non", "nv", "egg", "omelette", "omlette", "omlet", "gravy", "chicken", "paneer", "kadamba", "kadamb", "kdmb", "kadam", "yuktahar",
		"yukta", "yukthahar", "palash", "bakul", "which", "mess", "extras", "extra", "menu", "what's", "whats", "kya", "special")
	openerWords = newVocab("available", "availble", "avaliable", "availiable", "avail", "avl", "price", "prize", "amount",
		"how much", "hw much", "hw mch", "hwmuch", "how mcuh", "much", "cost", "rate",
		"buy", "want", "wnt", "need", "interested", "intrested", "still", "still there", "r u there", "u there", "are you there",
		"still up", "do you have", "u have", "you have", "selling", "sell", "token", "qr", "sold",
		"lunch", "dinner", "breakfast", "bf", "snacks", "nashta", "kitne", "kitna", "kitni", "cost?")
	bareOpener = newVocab("for", "there", "up", "have")
	// mealWords name each meal the way buyers write it.
	mealWords = map[string]vocab{
		"breakfast": newVocab("breakfast", "brekfast", "breakfst", "bf", "bfast", "bkft", "brkfst", "nashta"),
		"lunch":     newVocab("lunch", "lunxh", "lnch"),
		"snacks":    newVocab("snacks", "snack", "snaks"),
		"dinner":    newVocab("dinner", "dinr", "dinnr"),
	}
	firmWords = newVocab("final", "max", "maximum", "last", "lowest", "at most", "atmost", "at best", "and done", "done", "only", "fix", "fixed",
		"cant go above", "can't go above", "cannot go above", "wont go above", "won't go above", "not above",
		"cant pay above", "can't pay above", "wont pay above", "won't pay above", "wont buy above", "won't buy above", "not more than",
		"cant pay more than", "can't pay more than", "wont pay more than", "won't pay more than", "cant do more than", "can't do more than")
	rangeWords = newVocab("or", "to")
)

type msgWords struct {
	words  []string // squeezed lowercase words
	joined string   // " w1 w2 ... "
}

const (
	subHundredTok = `(?:(?:twenty|thirty|forty|fourty|fifty|sixty|seventy|eighty|ninety)(?:\s+(?:one|two|three|four|five|six|seven|eight|nine))?|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|one|two|three|four|five|six|seven|eight|nine)`
	numTok        = `(?:(?:(?:100|200|(?:(?:one|two|three|four|five|six|seven|eight|nine)\s+)?hundred)(?:\s+(?:and\s+)?` + subHundredTok + `)?)|(?:\d{1,3}(?:\.\d+)?)|(?:(?:twenty|thirty|forty|fourty|fifty|sixty|seventy|eighty|ninety)(?:\s+(?:one|two|three|four|five|six|seven|eight|nine))?|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen))`
)

var (
	wordRe  = regexp.MustCompile(`[a-z']+`)
	rangeRe = regexp.MustCompile(`\d\s*-\s*\d`)
	// Currency marks glued to a number: "80rs", "rs80", "₹40", "50/-".
	currencyAfter  = regexp.MustCompile(`(\d)\s*(rs|inr|rupees|rupee|bucks|/-)`)
	currencyBefore = regexp.MustCompile(`(rs\.?|inr)\s*(\d)`)
	// A 2-3 digit price glued to a common trailing word: "20at best",
	// "20possible pls?", "50final", "30mins". Single digits stay glued so
	// typos like "5his" or "2x" never become offers.
	gluedPrice = regexp.MustCompile(`(^|[^a-z0-9])(\d{2,3})(at|possible|posible|pls|plz|please|last|final|max|only|works|work|ok|okay|fine|bro|bhai|done|fix|fixed|deal|best|min|mins|minute|minutes|hr|hrs|hour|hours|sec|secs)\b`)
	// Durations are not rupee offers: "after 30 mins", "in 5 min". Bare "50 min"
	// (which can mean "minimum 50") is left alone unless preceded by a time word.
	durationUnitRe = regexp.MustCompile(`\b(?:\d{1,3}(?:\.\d+)?|one|two)\s*(mins|minute|minutes|hr|hrs|hour|hours|sec|secs|second|seconds)\b`)
	durationMinRe  = regexp.MustCompile(`\b(in|after|wait|within|about|like|around|for)\s+(?:\d{1,3}(?:\.\d+)?|one|two)\s*(min)\b`)
	// Normalize "one/two hundred" (and "1/2 hundred") before quantity patterns
	// so "for two hundred" stays ₹200 instead of matching "for two" as 2 meals.
	oneHundredRe = regexp.MustCompile(`\b(?:1|one)\s+hundred\b`)
	twoHundredRe = regexp.MustCompile(`\b(?:2|two)\s+hundred\b`)
	// Single-meal quantity, not a ₹1 offer: "Buy 1", "want 1", "1 meal", "for one".
	singleQtyRe = regexp.MustCompile(`\b(?:(buy|want|need|take|get)\s+1|1\s+(meal|qr|token|plate|one)|for\s+(?:1|one))\b`)
	// Multi-meal quantities and cues: "120 for both ?", "2 meals for 120", "120 for 2".
	multiQtyRe  = regexp.MustCompile(`\b(?:(?:2|two)\s+(meals?|qrs?|tokens?|plates?)|for\s+(?:2|two))\b`)
	multiMealRe = regexp.MustCompile(`\b(?:both|for\s+(?:2|two)|(?:2|two)\s+(?:meals?|qrs?|tokens?|plates?))\b`)
	// A price immediately before a complaint ("60 is too much", "sixty is too
	// expensive", "90 is very high") is the seller's ask being rejected, not an offer.
	complainedPrice = regexp.MustCompile(`\b` + numTok + `\s+(?:rs\s+)?(?:(?:is|its|it's|seems|feels|looks)\s+)?(?:(?:way|very)\s+)?(too\s+much|to\s+much|too\s+high|too\s+costly|too\s+expensive|costly|expensive|mehenga|mehnga|mehanga|high)\b`)
	// A price immediately after a refusal ("won't buy at 90", "won't buy at
	// sixty", "cant pay 90", "not for 90") rejects that price. Max-cap phrases
	// ("can't go above 50") and counter-questions ("why not 50?", "can you not
	// do it for 50?") do not match and stay offers.
	negatedPriceModal = regexp.MustCompile(`\b(won't|wont|will\s+not|can't|cant|cannot|don't|dont|do\s+not)\s+(?:(buy|buying|pay|paying|do|take|taking|give|giving|want|go\s+for)\s+)?(?:it\s+)?(?:(?:at|for|@)\s+)?(?:rs\s+)?` + numTok + `\b`)
	negatedPriceNot   = regexp.MustCompile(`\bnot\s+(?:(buying|paying|taking|giving)\s+(?:it\s+)?(?:(?:at|for|@)\s+)?|(?:at|for|@)\s+)(?:rs\s+)?` + numTok + `\b`)
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

// stripQuotedLines drops WhatsApp markdown quote lines ("> ...") when
// non-quote text follows, so a quoted post like "> sell ... @70\nAvailable?"
// doesn't read @70 as the buyer's offer.
func stripQuotedLines(text string) string {
	if !strings.Contains(text, "\n") {
		return text
	}
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "> ") || t == ">" {
			continue
		}
		if t != "" {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		return text
	}
	return strings.Join(kept, "\n")
}

// explicitGotItBought reports whether m uses "got it" with a clear
// bought-elsewhere cue ("mb bru got it", "told u i got it", "i alrdy got it",
// "got it sry"). Bare "got it" and "got it thanks" stay unknown so they never
// cancel a queued buyer.
func explicitGotItBought(m msgWords) bool {
	return strings.Contains(m.joined, " got it ") && gotItBoughtCues.in(m) && !apologyExcuses.in(m) &&
		!okWords.in(m) && !acceptWords.in(m) && !openerWords.in(m) && !questionWords.in(m) && !lowerWords.in(m) && !payOtherWords.in(m)
}

// parseBuyer reads one buyer message.
func parseBuyer(text string) intent {
	clean := stripQuotedLines(text)
	in := parseIntent(clean)
	m := words(clean)
	for _, meal := range []string{"breakfast", "lunch", "snacks", "dinner"} {
		if mealWords[meal].in(m) {
			in.meals = append(in.meals, meal)
		}
	}
	return in
}

// words splits text into lowercase, squeezed words.
func words(text string) msgWords {
	m := msgWords{joined: " "}
	for _, w := range wordRe.FindAllString(strings.ToLower(text), -1) {
		if w = strings.Trim(w, "'"); w != "" {
			m.words = append(m.words, squeeze(w))
			m.joined += squeeze(w) + " "
		}
	}
	return m
}

// parseIntent is parseBuyer without the meals.
func parseIntent(text string) intent {
	in := parseKind(text)
	norm := strings.ReplaceAll(text, "’", "'")
	m := words(norm)
	in.upi = upiWords.in(m)
	in.buyingMore = in.kind != intentPass && wantsAnother.in(m) && !negatedAnother.in(m)
	if !strings.ContainsAny(norm, "0123456789") && cantPayAppWords.in(m) &&
		(in.kind == intentUnknown || (in.kind == intentPass && in.apologyOnly && !in.bought)) {
		in.cantPayApp = true
	}
	if (in.kind == intentUnknown || in.kind == intentLower) && (dismissOnly.only(m) || dismissPhrases.in(m)) &&
		!okWords.in(m) && !acceptWords.in(m) && !questionWords.in(m) {
		in.dismiss = true
	}
	return in
}

// parseKind is parseIntent without intent.upi.
func parseKind(text string) intent {
	raw := strings.ToLower(strings.TrimSpace(text))
	paidMark := strings.ContainsAny(raw, "💸✅")
	raw = strings.NewReplacer("₹", " rs ", "’", "'", "@", " @ ", "\n", " ").Replace(raw)
	raw = currencyAfter.ReplaceAllString(raw, "$1 rs ")
	raw = currencyBefore.ReplaceAllString(raw, "rs $2")
	raw = gluedPrice.ReplaceAllString(raw, "${1}${2} ${3}")

	m := msgWords{joined: " "}
	for _, w := range wordRe.FindAllString(raw, -1) {
		w = strings.Trim(w, "'")
		if w == "" {
			continue
		}
		m.words = append(m.words, squeeze(w))
		m.joined += squeeze(w) + " "
	}

	numRaw := durationUnitRe.ReplaceAllString(raw, " $1 ")
	numRaw = durationMinRe.ReplaceAllString(numRaw, " $1 $2 ")
	hasDuration := numRaw != raw
	numRaw = oneHundredRe.ReplaceAllString(numRaw, "100")
	numRaw = twoHundredRe.ReplaceAllString(numRaw, "200")
	hasSingleMeal := singleQtyRe.MatchString(numRaw)
	hasMulti := multiMealRe.MatchString(numRaw)
	hasNegated := negatedPriceModal.MatchString(numRaw) || negatedPriceNot.MatchString(numRaw)
	numRaw = singleQtyRe.ReplaceAllString(numRaw, " $1 $2 ")
	numRaw = multiQtyRe.ReplaceAllString(numRaw, " $1 ")
	numRaw = complainedPrice.ReplaceAllString(numRaw, " $1 ")
	numRaw = negatedPriceModal.ReplaceAllString(numRaw, " $1 $2 ")
	numRaw = negatedPriceNot.ReplaceAllString(numRaw, " $1 ")
	var numWords []string
	for _, w := range wordRe.FindAllString(numRaw, -1) {
		if w = strings.Trim(w, "'"); w != "" {
			numWords = append(numWords, w)
		}
	}
	numbers := digitNumbers(numRaw)
	numbers = append(numbers, wordNumbers(numWords)...)

	droppedNumbers := false
	if notOfferContext.in(m) {
		if len(numbers) > 0 && (paidWords.in(m) || strings.Contains(m.joined, " sending ")) {
			return intent{kind: intentPaid}
		}
		droppedNumbers = len(numbers) > 0
		numbers = nil
	}
	if len(numbers) > 0 {
		n, ok := pickNumber(numbers, numRaw, m)
		if !ok {
			return intent{kind: intentUnknown}
		}
		return intent{kind: intentOffer, offer: n, firm: firmWords.in(m), multi: hasMulti, singleMeal: hasSingleMeal}
	}

	buyingMore := wantsAnother.in(m) && !negatedAnother.in(m)
	switch {
	case paidMark || paidWords.in(m):
		return intent{kind: intentPaid}
	case passWords.in(m) || notInterested(m.words):
		return intent{kind: intentPass, bought: boughtWords.in(m)}
	case !buyingMore && (boughtPassWords.in(m) || explicitGotItBought(m)):
		return intent{kind: intentPass, bought: true, boughtOnly: true}
	case hasNegated:
		return intent{kind: intentLower}
	case wontBuyWords.in(m):
		return intent{kind: intentPass}
	case len(m.words) > 0 && refuseFirst.words[m.words[0]] && !lowerWords.in(m) && !openerWords.in(m) && !payOtherWords.in(m) && !dismissPhrases.in(m):
		return intent{kind: intentPass, bought: boughtWords.in(m)}
	case payOtherWords.in(m):
		return intent{kind: intentPayOther, lower: priceObjectionWords.in(m), multi: hasMulti}
	case lowerWords.in(m), pleading.only(m) && !okWords.in(m):
		return intent{kind: intentLower, multi: hasMulti}
	case acceptWords.in(m) && !(buyingMore && droppedNumbers):
		return intent{kind: intentAccept, multi: hasMulti}
	case openerWords.in(m), bareOpener.only(m):
		return intent{kind: intentOpener, multi: hasMulti}
	case questionWords.in(m):
		return intent{kind: intentQuestion, multi: hasMulti}
	case apology.in(m) && !apologyExcuses.in(m):
		return intent{kind: intentPass, apologyOnly: true}
	case okWords.in(m) && !hasDuration:
		return intent{kind: intentOK, done: strings.Contains(m.joined, " done ") || strings.Contains(m.joined, " dn "), multi: hasMulti}
	case len(m.words) == 0 && !strings.ContainsAny(raw, "0123456789"):
		// "?", ".", "......": an opener with no words.
		return intent{kind: intentOpener}
	}
	return intent{kind: intentUnknown, multi: hasMulti}
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
