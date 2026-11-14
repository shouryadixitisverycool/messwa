package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Settings are values an admin saved on the dashboard's settings page, by
// key ("sell.show_price"). They go on top of config.toml: a key that isn't
// saved keeps the file's value. Durations are whole seconds.
type Settings map[string]json.RawMessage

// setting is one key the settings page can change.
type setting struct {
	key string
	get func(*Config) any
	set func(*Config, json.RawMessage) error
	// restart means only a restart applies it: run builds its listing
	// classifier and meal schedule from it once, at start.
	restart bool
}

func boolSetting(key string, f func(*Config) *bool) setting {
	return setting{key: key,
		get: func(c *Config) any { return *f(c) },
		set: func(c *Config, v json.RawMessage) error { return strictJSON(v, f(c)) }}
}

// intSetting is a whole number. config.validate checks the ranges that
// depend on other settings, such as group_count.
func intSetting(key string, min int, f func(*Config) *int) setting {
	return setting{key: key,
		get: func(c *Config) any { return *f(c) },
		set: func(c *Config, v json.RawMessage) error {
			var n int
			if err := strictJSON(v, &n); err != nil {
				return err
			}
			if n < min {
				return fmt.Errorf("must be at least %d", min)
			}
			*f(c) = n
			return nil
		}}
}

// secondsSetting is a duration in whole seconds, at least min unless it is
// 0 and zero allows that.
func secondsSetting(key string, min time.Duration, zero bool, f func(*Config) *duration) setting {
	return setting{key: key,
		get: func(c *Config) any { return int64(f(c).Seconds()) },
		set: func(c *Config, v json.RawMessage) error {
			var s int64
			if err := strictJSON(v, &s); err != nil {
				return err
			}
			d := time.Duration(s) * time.Second
			if d < min && !(zero && d == 0) {
				return fmt.Errorf("must be at least %s", min)
			}
			f(c).Duration = d
			return nil
		}}
}

// listSetting replaces a list of strings. Blank entries are dropped and
// repeats kept once. It needs at least min entries.
func listSetting(key string, min int, f func(*Config) *[]string) setting {
	return setting{key: key,
		get: func(c *Config) any { return nonNil(*f(c)) },
		set: func(c *Config, v json.RawMessage) error {
			var in []string
			if err := strictJSON(v, &in); err != nil {
				return err
			}
			out := []string{}
			seen := map[string]bool{}
			for _, s := range in {
				s = strings.TrimSpace(s)
				if s != "" && !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
			if len(out) < min {
				return fmt.Errorf("needs at least %d", min)
			}
			*f(c) = out
			return nil
		}}
}

// MealTime is a meal's window on the settings page.
type MealTime struct {
	Starts string `json:"starts"`
	Ends   string `json:"ends"`
}

var settings = []setting{
	listSetting("sell.groups", 0, func(c *Config) *[]string { return &c.Sell.Groups }),
	intSetting("sell.group_count", 0, func(c *Config) *int { return &c.Sell.GroupCount }),
	secondsSetting("sell.post_gap", MinPostGap, false, func(c *Config) *duration { return &c.Sell.PostGap }),
	boolSetting("sell.show_price", func(c *Config) *bool { return &c.Sell.ShowPrice }),
	boolSetting("sell.mention_extras", func(c *Config) *bool { return &c.Sell.MentionExtras }),
	{key: "sell.handoff",
		get: func(c *Config) any { h, _ := ParseHandoff(string(c.Sell.Handoff)); return h },
		set: func(c *Config, v json.RawMessage) error {
			var s string
			if err := strictJSON(v, &s); err != nil {
				return err
			}
			h, err := ParseHandoff(s)
			c.Sell.Handoff = h
			return err
		}},
	boolSetting("sell.accept_plain_dms", func(c *Config) *bool { return &c.Sell.AcceptPlainDMs }),
	boolSetting("sell.message_buy_posts", func(c *Config) *bool { return &c.Sell.MessageBuyPosts }),
	boolSetting("sell.nudge", func(c *Config) *bool { return &c.Sell.Nudge }),
	secondsSetting("sell.list_at", MinListAt, false, func(c *Config) *duration { return &c.Sell.ListAt }),
	{key: "sell.buyers",
		get: func(c *Config) any { b, _ := ParseBuyers(string(c.Sell.Buyers)); return b },
		set: func(c *Config, v json.RawMessage) error {
			var s string
			if err := strictJSON(v, &s); err != nil {
				return err
			}
			b, err := ParseBuyers(s)
			c.Sell.Buyers = b
			return err
		}},
	boolSetting("sell.repost", func(c *Config) *bool { return &c.Sell.Repost }),
	intSetting("sell.repost_messages", 0, func(c *Config) *int { return &c.Sell.RepostMessages }),
	intSetting("sell.repost_listings", 0, func(c *Config) *int { return &c.Sell.RepostListings }),
	intSetting("sell.max_reposts", 0, func(c *Config) *int { return &c.Sell.MaxReposts }),
	secondsSetting("sell.negotiation_silence", MinNegotiationSilence, false, func(c *Config) *duration { return &c.Sell.NegotiationSilence }),
	boolSetting("sell.accept_payment_images", func(c *Config) *bool { return &c.Sell.AcceptPaymentImages }),
	boolSetting("mcp.sell_tasks", func(c *Config) *bool { return &c.MCP.SellTasks }),
	listSetting("sold.emojis", 1, func(c *Config) *[]string { return &c.Sold.Emojis }),
	listSetting("sold.closed_emojis", 0, func(c *Config) *[]string { return &c.Sold.ClosedEmojis }),
	boolSetting("sold.admins_count", func(c *Config) *bool { return &c.Sold.AdminsCount }),
	listSetting("sold.words", 0, func(c *Config) *[]string { return &c.Sold.Words }),
	secondsSetting("dedupe.window", time.Hour, false, func(c *Config) *duration { return &c.Dedupe.Window }),
	secondsSetting("dms.retention", time.Hour, false, func(c *Config) *duration { return &c.DMs.Retention }),
	boolSetting("hide_snacks", func(c *Config) *bool { return &c.HideSnacks }),
	intSetting("sales.monthly_fee", 0, func(c *Config) *int { return &c.Sales.MonthlyFee }),
	{key: "seasons",
		get: func(c *Config) any { return nonNil(c.Seasons) },
		set: func(c *Config, v json.RawMessage) error {
			var ss []Season
			if err := strictJSON(v, &ss); err != nil {
				return err
			}
			for i := range ss {
				ss[i].Name = strings.TrimSpace(ss[i].Name)
			}
			c.Seasons = ss
			return nil
		}},
	// Meals and messes keep their names, keywords and aliases from
	// config.toml; the page changes only their times and veg_only.
	{key: "meals.times", restart: true,
		get: func(c *Config) any {
			m := map[string]MealTime{}
			for _, x := range c.Meals {
				m[x.Name] = MealTime{x.Starts, x.Ends}
			}
			return m
		},
		set: func(c *Config, v json.RawMessage) error {
			var m map[string]MealTime
			if err := strictJSON(v, &m); err != nil {
				return err
			}
			meals := append([]Meal(nil), c.Meals...)
			for i, x := range meals {
				t, ok := m[x.Name]
				if !ok {
					continue
				}
				st, err := ParseClock(t.Starts)
				if err != nil {
					return fmt.Errorf("%s starts: %w", x.Name, err)
				}
				en, err := ParseClock(t.Ends)
				if err != nil {
					return fmt.Errorf("%s ends: %w", x.Name, err)
				}
				if en <= st {
					return fmt.Errorf("%s must end after it starts", x.Name)
				}
				meals[i].Starts, meals[i].Ends = t.Starts, t.Ends
			}
			c.Meals = meals
			return nil
		}},
	{key: "messes.veg_only", restart: true,
		get: func(c *Config) any {
			m := map[string]bool{}
			for _, x := range c.Messes {
				m[x.Name] = x.VegOnly
			}
			return m
		},
		set: func(c *Config, v json.RawMessage) error {
			var m map[string]bool
			if err := strictJSON(v, &m); err != nil {
				return err
			}
			messes := append([]Mess(nil), c.Messes...)
			for i, x := range messes {
				if veg, ok := m[x.Name]; ok {
					messes[i].VegOnly = veg
				}
			}
			c.Messes = messes
			return nil
		}},
}

func findSetting(key string) (setting, bool) {
	for _, s := range settings {
		if s.key == key {
			return s, true
		}
	}
	return setting{}, false
}

// SettingKeys lists every key the settings page can change.
func SettingKeys() []string {
	keys := make([]string, len(settings))
	for i, s := range settings {
		keys[i] = s.key
	}
	return keys
}

// NeedsRestart reports whether key only takes effect when run restarts.
func NeedsRestart(key string) bool {
	s, ok := findSetting(key)
	return ok && s.restart
}

// Values returns every setting key's value in c, ready for JSON.
func (c *Config) Values() map[string]any {
	out := make(map[string]any, len(settings))
	for _, s := range settings {
		out[s.key] = s.get(c)
	}
	return out
}

// SameValue reports whether key has the same value in a and b.
func SameValue(key string, a, b *Config) bool {
	s, ok := findSetting(key)
	if !ok {
		return true
	}
	x, _ := json.Marshal(s.get(a))
	y, _ := json.Marshal(s.get(b))
	return bytes.Equal(x, y)
}

// retired are settings messwa no longer has. A saved value for one is
// ignored, so an older save doesn't stop messwa starting or saving.
var retired = map[string]bool{"sell.repost_after": true}

// WithSettings returns a copy of c with saved applied, checked like
// config.toml. c itself is left alone.
func (c *Config) WithSettings(saved Settings) (*Config, error) {
	n := *c
	keys := make([]string, 0, len(saved))
	for k := range saved {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s, ok := findSetting(k)
		if !ok && retired[k] {
			continue
		}
		if !ok {
			return nil, fmt.Errorf("unknown setting %q", k)
		}
		if err := s.set(&n, saved[k]); err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
	}
	if err := n.validate(); err != nil {
		return nil, err
	}
	return &n, nil
}

// ApplySaved puts saved settings over c's own values, for a command
// starting up. A key that doesn't fit the file any more, such as a
// group_count above the groups it now lists, is skipped and returned in
// skipped with why, so one stale key can't stop messwa starting.
func (c *Config) ApplySaved(saved Settings) (skipped map[string]error) {
	keep := Settings{}
	for _, k := range SettingKeys() {
		v, ok := saved[k]
		if !ok {
			continue
		}
		keep[k] = v
		if _, err := c.WithSettings(keep); err != nil {
			delete(keep, k)
			if skipped == nil {
				skipped = map[string]error{}
			}
			skipped[k] = err
		}
	}
	for k := range saved {
		if _, ok := findSetting(k); !ok && !retired[k] {
			if skipped == nil {
				skipped = map[string]error{}
			}
			skipped[k] = fmt.Errorf("unknown setting")
		}
	}
	file := *c
	n, _ := c.WithSettings(keep)
	*c = *n
	c.file = &file
	if c.live == nil {
		c.live = &atomic.Pointer[Config]{}
	}
	return skipped
}

// File returns the config as config.toml has it, before saved settings.
func (c *Config) File() *Config {
	if c.file != nil {
		return c.file
	}
	return c
}

// Live returns the config with the latest saved settings, which Update
// swaps in while run is up. Read settings that apply without a restart
// through it; meals and messes stay as c has them.
func (c *Config) Live() *Config {
	if c.live != nil {
		if n := c.live.Load(); n != nil {
			return n
		}
	}
	return c
}

// Update makes saved the live settings: config.toml with saved on top. It
// returns the new live config, or an error and changes nothing.
func (c *Config) Update(saved Settings) (*Config, error) {
	n, err := c.File().WithSettings(saved)
	if err != nil {
		return nil, err
	}
	if c.live == nil {
		return nil, fmt.Errorf("this config can't take settings while running; build it with config.Load or config.Default")
	}
	n.file, n.live = c.File(), c.live
	c.live.Store(n)
	return n, nil
}

// strictJSON decodes v into dst and refuses unknown fields and trailing data.
func strictJSON(v json.RawMessage, dst any) error {
	d := json.NewDecoder(bytes.NewReader(v))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return fmt.Errorf("bad value: %w", err)
	}
	if d.More() {
		return fmt.Errorf("bad value: trailing data")
	}
	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// IsolateSettings gives a per-account configuration its own live settings.
func (c *Config) IsolateSettings() { c.file = nil; c.live = &atomic.Pointer[Config]{} }
