// Package config loads messwa's TOML configuration.
package config

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	AccountRoot string `toml:"-"`
	StateDir    string `toml:"state_dir"`
	Timezone    string `toml:"timezone"`

	// Groups are the mess buy/sell group JIDs (xxxx@g.us) to merge.
	Groups []string `toml:"groups"`

	Meals  []Meal `toml:"meals"`
	Messes []Mess `toml:"messes"`
	// Seasons split the year into terms and breaks for the insights page.
	Seasons []Season `toml:"seasons"`
	Dedupe  Dedupe   `toml:"dedupe"`
	Sold    Sold     `toml:"sold"`
	DMs     DMs      `toml:"dms"`
	MCP     MCP      `toml:"mcp"`
	Dash    Dash     `toml:"dashboard"`
	Sales   Sales    `toml:"sales"`
	Sell    Sell     `toml:"sell"`

	loc *time.Location
	// file is the config as config.toml has it, before saved settings.
	file *Config
	// live holds the config with the latest saved settings; see Live.
	live *atomic.Pointer[Config]
}

type Meal struct {
	Name     string   `toml:"name"`
	Starts   string   `toml:"starts"` // "HH:MM"
	Ends     string   `toml:"ends"`
	Keywords []string `toml:"keywords"`
}

type Mess struct {
	Name    string   `toml:"name"`
	Aliases []string `toml:"aliases"`
	// VegOnly messes serve no non-veg, so their listings are veg even
	// when the post doesn't say.
	VegOnly bool `toml:"veg_only"`
}

type Dedupe struct {
	// How far back to look for an earlier post by the same person for the
	// same meal slot. The slot match does the real work; people post
	// tomorrow's breakfast the evening before, so this must span that.
	Window duration `toml:"window"`
}

type Sold struct {
	// Reactions meaning sold. Skin tones and variation selectors are
	// ignored, so "👍" covers "👍🏻" and "☑" covers "☑️".
	Emojis []string `toml:"emojis"`
	// Reactions from the seller meaning withdrawn ("closed").
	ClosedEmojis []string `toml:"closed_emojis"`
	// Who can mark a listing sold by reacting: the seller always can.
	// Admins of the group can too when AdminsCount is true. Off by default:
	// in the IIITH groups admins never tick other people's posts.
	AdminsCount bool `toml:"admins_count"`
	// Words that mark a listing sold when the seller edits the post to
	// include them or replies to it with them.
	Words []string `toml:"words"`
}

type DMs struct {
	// How long DM content from unsaved numbers is kept.
	Retention duration `toml:"retention"`
}

type MCP struct {
	Listen string `toml:"listen"`
	// Name of the env var holding a shared bearer token that acts as the
	// first admin. Per-user tokens work either way. With it unset, requests
	// without a token are allowed when Listen is loopback.
	TokenEnv string `toml:"token_env"`
	// SellTasks adds the start_sell_task, sell_task_status and
	// cancel_sell_task tools, for admins only. Off by default: they let an
	// agent post in a group and message buyers from the linked account.
	SellTasks bool `toml:"sell_tasks"`
}

type Dash struct {
	// Where the listings dashboard listens. Empty turns it off. It shows
	// sellers' phone numbers, so it must be loopback or a private address
	// like a LAN or Tailscale IP; run refuses anything else.
	Listen string `toml:"listen"`
	// BeeperDB is Beeper Desktop or Server's index.db. When set, the
	// dashboard links each copy of a post to that message in Beeper. It is
	// only ever opened read-only.
	BeeperDB string `toml:"beeper_db"`
}

type Sales struct {
	// What the mess charges you per month, in rupees. The sales page uses
	// the portal's bill and falls back to this when the portal can't be
	// read. 0 hides that part.
	MonthlyFee int `toml:"monthly_fee"`
}

// VegOnly reports whether a mess serves only veg.
func (c *Config) VegOnly(mess string) bool {
	for _, m := range c.Messes {
		if m.Name == mess {
			return m.VegOnly
		}
	}
	return false
}

type duration struct{ time.Duration }

func (d *duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

func Load(path string) (*Config, error) {
	c := Default()
	if _, err := toml.DecodeFile(path, c); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return c, c.validate()
}

func Default() *Config {
	return &Config{
		live:     &atomic.Pointer[Config]{},
		StateDir: "state",
		Timezone: "Asia/Kolkata",
		Meals: []Meal{
			{Name: "breakfast", Starts: "07:30", Ends: "09:30", Keywords: []string{"bf", "b/f", "bfast", "bkft", "brkfst", "nashta"}},
			{Name: "lunch", Starts: "12:30", Ends: "14:30", Keywords: []string{"lunxh"}},
			{Name: "snacks", Starts: "16:45", Ends: "18:15", Keywords: []string{"snack", "evening snacks"}},
			{Name: "dinner", Starts: "19:30", Ends: "21:30", Keywords: []string{"dinr"}},
		},
		Messes: []Mess{
			{Name: "kadamba", Aliases: []string{"kadamb", "kdm", "kdmb", "kdmba", "kadam"}},
			{Name: "yuktahar", Aliases: []string{"yukthar", "yukt", "ykth", "yukhtar"}, VegOnly: true},
			{Name: "palash", Aliases: []string{"palas", "plsh"}, VegOnly: true},
			{Name: "bakul", Aliases: []string{"bkl"}},
		},
		Seasons: defaultSeasons(),
		Dedupe:  Dedupe{Window: duration{24 * time.Hour}},
		Sold: Sold{
			Emojis:       []string{"✅", "✔", "☑", "👍"},
			ClosedEmojis: []string{"❌", "✖", "🚫"},
			AdminsCount:  false,
			Words:        []string{"sold", "done", "gone"},
		},
		DMs: DMs{Retention: duration{72 * time.Hour}},
		MCP: MCP{Listen: "127.0.0.1:8766", TokenEnv: "MESSWA_MCP_TOKEN"},
		Sell: Sell{
			PostGap:   duration{15 * time.Second},
			ShowPrice: true,
			// Someone who posted that they want this meal is the likeliest
			// buyer there is.
			MessageBuyPosts: true,
			Repost:          true,
			RepostAfter:     duration{10 * time.Minute},
			RepostMessages:  10,
			RepostListings:  3,
			MaxReposts:      6,
			// WhatsApp replies come in minutes, not seconds. In the
			// 12 Nov dinner test 6 of 9 buyers timed out at 2m and the
			// meal went to one who wrote just after.
			NegotiationSilence: duration{10 * time.Minute},
		},
	}
}

func (c *Config) validate() error {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return fmt.Errorf("timezone: %w", err)
	}
	c.loc = loc
	for _, m := range c.Meals {
		if _, err := ParseClock(m.Starts); err != nil {
			return fmt.Errorf("meal %s starts: %w", m.Name, err)
		}
		if _, err := ParseClock(m.Ends); err != nil {
			return fmt.Errorf("meal %s ends: %w", m.Name, err)
		}
	}
	if err := c.validateSell(); err != nil {
		return err
	}
	return validateSeasons(c.Seasons)
}

func (c *Config) validateSell() error {
	s := c.Sell
	if len(s.Groups) > 0 && s.GroupCount != 0 {
		return fmt.Errorf("sell: set groups or group_count, not both")
	}
	if s.GroupCount < 0 || s.GroupCount > len(c.Groups) {
		return fmt.Errorf("sell.group_count must be between 0 and the %d configured groups", len(c.Groups))
	}
	for _, g := range s.Groups {
		if strings.TrimSpace(g) == "" {
			return fmt.Errorf("sell.groups has an empty entry")
		}
	}
	if s.PostGap.Duration < MinPostGap {
		return fmt.Errorf("sell.post_gap must be at least %s", MinPostGap)
	}
	if d := s.NegotiationSilence.Duration; d < MinNegotiationSilence || d > MaxNegotiationSilence {
		return fmt.Errorf("sell.negotiation_silence must be between %s and %s", MinNegotiationSilence, MaxNegotiationSilence)
	}
	return ValidateRepost(s.RepostAfter.Duration, s.RepostMessages, s.RepostListings, s.MaxReposts, "sell.")
}

// ValidateRepost checks the repost thresholds. prefix names them in the
// error: "sell." for config.toml, "-" for flags.
func ValidateRepost(after time.Duration, messages, listings, maxReposts int, prefix string) error {
	name := func(n string) string {
		if prefix == "-" {
			return "-" + strings.ReplaceAll(n, "_", "-")
		}
		return prefix + n
	}
	switch {
	case after != 0 && after < MinRepostGap:
		return fmt.Errorf("%s must be 0 (off) or at least %s", name("repost_after"), MinRepostGap)
	case messages < 0:
		return fmt.Errorf("%s must be 0 (off) or more", name("repost_messages"))
	case listings < 0:
		return fmt.Errorf("%s must be 0 (off) or more", name("repost_listings"))
	case maxReposts < 0 || maxReposts > MaxRepostsLimit:
		return fmt.Errorf("%s must be between 0 and %d", name("max_reposts"), MaxRepostsLimit)
	}
	return nil
}

func (c *Config) Location() *time.Location {
	if c.loc == nil {
		c.loc, _ = time.LoadLocation(c.Timezone)
	}
	return c.loc
}

// ParseClock parses "HH:MM" into minutes after midnight.
func ParseClock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, err
	}
	return t.Hour()*60 + t.Minute(), nil
}

func (c *Config) SessionDB() string { return c.StateDir + "/session.db" }
func (c *Config) DataDB() string    { return c.StateDir + "/messwa.db" }

func EnsureDir(dir string) error { return os.MkdirAll(dir, 0o700) }
