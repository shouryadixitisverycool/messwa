// Package config loads messwa's TOML configuration.
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	StateDir string `toml:"state_dir"`
	Timezone string `toml:"timezone"`

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

	loc *time.Location
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
	return validateSeasons(c.Seasons)
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
