package main

import "strings"

// Buyers decline in a hurry: "Not intrested", "nt interested", "sorry not
// interseted anymore". Both the payment window (buyerPassed) and the
// negotiation parser read these as a pass.

// notWords stand for "not" before "interested".
var notWords = map[string]bool{"not": true, "nt": true, "no": true}

// notInterested reports whether words, already lowercased and squeezed,
// contain "not interested" with a typo allowed in either word.
func notInterested(words []string) bool {
	for i := 0; i+1 < len(words); i++ {
		if notWords[words[i]] && interestedTypo(words[i+1]) {
			return true
		}
	}
	return false
}

// interestedTypo matches "interested" and misspellings of it such as
// "intrested", "intersted", "interseted" and "intrstd", but not words like
// "interesting" or "internet".
func interestedTypo(w string) bool {
	const want = "interested"
	if w == want {
		return true
	}
	if len(w) < 6 || !strings.HasPrefix(w, "int") || !strings.HasSuffix(w, "d") {
		return false
	}
	return editDistance(w, want) <= 3
}

// editDistance is the Levenshtein distance between a and b, by byte. Both
// are ASCII here.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// passPhrases are the whole messages that give up a turn, once filler is
// dropped. A message with anything else in it ("don't cancel, I paid")
// is not a pass, because a buyer who passes loses the turn and a payment
// they send after that gets no QR.
var passPhrases = squeezedSet("pass", "cancel", "no thanks", "no thank you", "no thnx", "no thanx",
	"not interested", "not needed", "not required", "nvm", "never mind", "nevermind", "no need",
	"dont want", "don't want", "do not want", "donot want", "dont need", "don't need", "do not need", "donot need",
	"wont buy", "won't buy", "will not buy", "not buying",
	"got one", "already got", "alrdy got", "got already",
	"bought already", "bought another", "bought another one", "bought elsewhere",
	"bought from other", "bought from someone", "bought from someone else",
	"got from other", "got from someone", "got from someone else",
	"purchased another", "purchased another one")

// passFiller may surround a pass: "sorry bro not interested anymore".
var passFiller = squeezedSet("sorry", "sry", "anymore", "now", "ok", "okay", "mb")

// squeezedSet holds entries squeezed word by word, as passMessage reads
// messages: "sorry" is stored as "sory".
func squeezedSet(entries ...string) map[string]bool {
	set := make(map[string]bool, len(entries))
	for _, e := range entries {
		fields := strings.Fields(e)
		for i := range fields {
			fields[i] = squeeze(fields[i])
		}
		set[strings.Join(fields, " ")] = true
	}
	return set
}

// passMessage reports whether text, as a whole, declines the meal.
// Questions ("got one?") and messages naming a price ("won't buy 60") do not
// match here: those belong to the negotiation / backout handlers.
func passMessage(text string) bool {
	if strings.ContainsAny(text, "?0123456789") {
		return false
	}
	norm := strings.ReplaceAll(strings.ToLower(text), "’", "'")
	var words []string
	for _, w := range wordRe.FindAllString(norm, -1) {
		w = squeeze(strings.Trim(w, "'"))
		if w == "" || filler.words[w] || passFiller[w] {
			continue
		}
		words = append(words, w)
	}
	if len(words) == 2 && notWords[words[0]] && interestedTypo(words[1]) {
		return true
	}
	return passPhrases[strings.Join(words, " ")]
}
