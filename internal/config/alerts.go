package config

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"
)

// ValidAlertKinds lists every AttentionItem.Kind value that alerts.kinds accepts.
var ValidAlertKinds = []string{
	"refund",
	"blocked",
	"schedule",
	"delivery",
	"post",
	"rule",
	"whatsapp",
}

// DefaultAlertKinds are the kinds alerted on by default: the urgent/actionable
// ones. "whatsapp" (link offline) is excluded from the default because it
// cannot be sent over a dead WhatsApp link; future channels like Web Push can
// include it.
var DefaultAlertKinds = []string{
	"refund",
	"blocked",
	"schedule",
	"delivery",
	"post",
}

// MinAlertsPerHour and MaxAlertsPerHour bound alerts.max_per_hour.
const (
	DefaultAlertsPerHour = 6
	MinAlertsPerHour     = 1
	MaxAlertsPerHour     = 60
)

// Alerts holds per-user notification preferences. Everything that sends
// outbound messages is opt-in and off by default.
type Alerts struct {
	// WhatsApp enables sending attention alerts to the user's own WhatsApp
	// number from their linked device. Off by default.
	WhatsApp bool `toml:"whatsapp"`
	// Kinds lists which attention item kinds trigger alerts.
	Kinds []string `toml:"kinds"`
	// QuietStart and QuietEnd define quiet hours in 24-hour "HH:MM" local time
	// (e.g. "00:00" to "07:00"). Non-refund alerts are held until quiet hours
	// end; refund alerts always go through. Leave both empty to disable.
	QuietStart string `toml:"quiet_start"`
	QuietEnd   string `toml:"quiet_end"`
	// MaxPerHour caps how many alert messages a channel may send per hour.
	MaxPerHour int `toml:"max_per_hour"`
}

func defaultAlerts() Alerts {
	return Alerts{
		WhatsApp:   false,
		Kinds:      append([]string(nil), DefaultAlertKinds...),
		QuietStart: "00:00",
		QuietEnd:   "07:00",
		MaxPerHour: DefaultAlertsPerHour,
	}
}

// ParseAlertKinds normalizes and validates a list of attention item kinds.
func ParseAlertKinds(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		k := strings.ToLower(strings.TrimSpace(raw))
		if k == "" {
			continue
		}
		if !slices.Contains(ValidAlertKinds, k) {
			return nil, fmt.Errorf("unknown alert kind %q (valid: %s)", k, strings.Join(ValidAlertKinds, ", "))
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out, nil
}

// AllowsKind reports whether kind is enabled in Alerts.Kinds.
func (a Alerts) AllowsKind(kind string) bool {
	return slices.Contains(a.Kinds, kind)
}

// InQuietHours reports whether t falls inside the configured quiet hours window.
// When both QuietStart and QuietEnd are empty, or equal, quiet hours are off.
func (a Alerts) InQuietHours(t time.Time) bool {
	if a.QuietStart == "" || a.QuietEnd == "" {
		return false
	}
	start, err1 := ParseClock(a.QuietStart)
	end, err2 := ParseClock(a.QuietEnd)
	if err1 != nil || err2 != nil || start == end {
		return false
	}
	cur := t.Hour()*60 + t.Minute()
	if start < end {
		return cur >= start && cur < end
	}
	// Overnight range, e.g. 23:00 to 07:00.
	return cur >= start || cur < end
}

func (c *Config) validateAlerts() error {
	a := &c.Alerts
	kinds, err := ParseAlertKinds(a.Kinds)
	if err != nil {
		return fmt.Errorf("alerts.kinds: %w", err)
	}
	a.Kinds = kinds

	a.QuietStart = strings.TrimSpace(a.QuietStart)
	a.QuietEnd = strings.TrimSpace(a.QuietEnd)
	if (a.QuietStart == "") != (a.QuietEnd == "") {
		return fmt.Errorf("alerts.quiet_start and alerts.quiet_end must both be set or both be empty")
	}
	if a.QuietStart != "" {
		if _, err := ParseClock(a.QuietStart); err != nil {
			return fmt.Errorf("alerts.quiet_start: %w", err)
		}
		if _, err := ParseClock(a.QuietEnd); err != nil {
			return fmt.Errorf("alerts.quiet_end: %w", err)
		}
	}
	if a.MaxPerHour < MinAlertsPerHour || a.MaxPerHour > MaxAlertsPerHour {
		return fmt.Errorf("alerts.max_per_hour must be between %d and %d", MinAlertsPerHour, MaxAlertsPerHour)
	}
	return nil
}

func (c *Config) validateDash() error {
	raw := strings.TrimSpace(c.Dash.PublicURL)
	if raw == "" {
		c.Dash.PublicURL = ""
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("dashboard.public_url %q: must be an http:// or https:// URL with a host", raw)
	}
	c.Dash.PublicURL = strings.TrimRight(raw, "/")
	return nil
}

// BaseURL returns the dashboard base URL without a trailing slash, preferring
// PublicURL and falling back to "http://" + Listen when Listen is a reachable
// non-loopback address (e.g. LAN or Tailscale). Loopback listen addresses
// return "" so phone alerts never carry broken 127.0.0.1 links.
func (d Dash) BaseURL() string {
	if u := strings.TrimRight(strings.TrimSpace(d.PublicURL), "/"); u != "" {
		return u
	}
	l := strings.TrimSpace(d.Listen)
	if l == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(l)
	if err != nil {
		return ""
	}
	if host == "" || strings.EqualFold(host, "localhost") {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return ""
	}
	return "http://" + l
}
