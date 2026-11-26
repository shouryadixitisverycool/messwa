package notify

import (
	"fmt"
	"strings"
	"unicode"
)

const maxDetailRunes = 160

type formattedEntry struct {
	text string
	id   string
}

// FormatBatch renders a batch of attention items into a short, plain-word alert
// message with dashboard links (no em dashes). Multiple "post" failures for the
// same task are collapsed into a single line such as:
//
//	⚠️ Lunch offer didn't post in 2 groups. Fix: https://<dashboard>/#attn-<id>
func FormatBatch(batch Batch) string {
	entries := collapseEntries(batch.Items)
	if len(entries) == 0 {
		return ""
	}
	baseURL := strings.TrimRight(strings.TrimSpace(batch.BaseURL), "/")
	if len(entries) == 1 {
		return "⚠️ " + withLink(entries[0], baseURL)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "⚠️ %d messwa alerts need you:", len(entries))
	for _, e := range entries {
		sb.WriteString("\n- ")
		sb.WriteString(withLink(e, baseURL))
	}
	return sb.String()
}

func withLink(e formattedEntry, baseURL string) string {
	text := ensureSentence(e.text)
	if baseURL == "" || e.id == "" {
		return text
	}
	return fmt.Sprintf("%s Fix: %s/#attn-%s", text, baseURL, e.id)
}

func collapseEntries(items []Item) []formattedEntry {
	type postKey struct {
		task int64
		date string
		meal string
	}
	postIndex := map[postKey]int{}
	postCounts := map[postKey]int{}
	postFirst := map[postKey]Item{}

	var out []formattedEntry
	for _, it := range items {
		if it.Kind == "post" {
			k := postKey{task: it.Task, date: it.Date, meal: baseMeal(it.Meal)}
			if idx, ok := postIndex[k]; ok {
				postCounts[k]++
				out[idx].text = formatPostLine(postFirst[k], postCounts[k])
				continue
			}
			postIndex[k] = len(out)
			postCounts[k] = 1
			postFirst[k] = it
			out = append(out, formattedEntry{
				text: formatPostLine(it, 1),
				id:   it.ID,
			})
			continue
		}
		out = append(out, formattedEntry{
			text: formatItemLine(it),
			id:   it.ID,
		})
	}
	return out
}

func formatPostLine(it Item, count int) string {
	meal := capWord(baseMeal(it.Meal))
	prefix := "Offer didn't post"
	if meal != "" {
		prefix = meal + " offer didn't post"
	}
	if count > 1 {
		return fmt.Sprintf("%s in %d groups", prefix, count)
	}
	if d := cleanDetail(it.Detail); d != "" {
		return fmt.Sprintf("%s (%s)", prefix, d)
	}
	return prefix
}

func formatItemLine(it Item) string {
	meal := baseMeal(it.Meal)
	mealCap := capWord(meal)
	detail := cleanDetail(it.Detail)

	switch it.Kind {
	case "refund":
		head := "Refund due"
		if meal != "" {
			head += " (" + meal + ")"
		}
		if detail != "" {
			return head + ": " + detail
		}
		return head
	case "blocked":
		head := "Sale blocked"
		if mealCap != "" {
			head = mealCap + " sale blocked"
		}
		if detail != "" {
			return head + ": " + detail
		}
		return head
	case "schedule":
		head := "Schedule failed"
		if mealCap != "" {
			head = mealCap + " schedule failed"
		}
		if detail != "" {
			return head + ": " + detail
		}
		return head
	case "delivery":
		head := "Check QR delivery"
		if meal != "" {
			head = "Check " + meal + " QR delivery"
		}
		if detail != "" {
			return head + ": " + detail
		}
		return head
	case "rule":
		head := "Recurring rule needs a look"
		if detail != "" {
			return head + ": " + detail
		}
		return head
	case "whatsapp":
		if detail != "" {
			return "WhatsApp disconnected: " + detail
		}
		return "WhatsApp disconnected"
	default:
		head := cleanDetail(it.Title)
		if head == "" {
			head = "Alert"
		}
		if detail != "" {
			return head + ": " + detail
		}
		return head
	}
}

func baseMeal(meal string) string {
	m, _, _ := strings.Cut(strings.TrimSpace(meal), "#")
	return strings.ToLower(m)
}

func capWord(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func cleanDetail(s string) string {
	s = strings.ReplaceAll(s, "\u2014", " - ") // em dash
	s = strings.ReplaceAll(s, "\u2013", " - ") // en dash
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	rs := []rune(s)
	if len(rs) > maxDetailRunes {
		s = strings.TrimSpace(string(rs[:maxDetailRunes-3])) + "..."
	}
	return s
}

func ensureSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	last := s[len(s)-1]
	if last == '.' || last == '!' || last == '?' {
		return s
	}
	return s + "."
}
