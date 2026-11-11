package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHandoffSetting(t *testing.T) {
	c := Default()
	if c.Sell.Handoff.HandsOffOnReply() {
		t.Fatal("default config hands off")
	}
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte("[sell]\nhandoff = \"on_reply\"\n"), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Sell.Handoff.HandsOffOnReply() {
		t.Fatalf("handoff = %q", c.Sell.Handoff)
	}
	os.WriteFile(p, []byte("[sell]\nhandoff = \"sometimes\"\n"), 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("unknown handoff value loaded")
	}
}

func TestHandoffOverride(t *testing.T) {
	for _, tc := range []struct {
		configured     Handoff
		override, want string
		bad            bool
	}{
		{"", "", "never", false},
		{HandoffOnReply, "", "on_reply", false},
		{HandoffOnReply, "never", "never", false},
		{HandoffNever, "on_reply", "on_reply", false},
		{HandoffNever, "on-reply", "", true},
	} {
		got, err := tc.configured.Resolve(tc.override)
		if (err != nil) != tc.bad || string(got) != tc.want {
			t.Errorf("%q over %q = %q, %v", tc.override, tc.configured, got, err)
		}
	}
}

func TestTaskHandoff(t *testing.T) {
	for in, want := range map[string]Handoff{"": HandoffNever, "never": HandoffNever, "on_reply": HandoffOnReply, "on_agreement": HandoffOnAgreement} {
		if got, err := ParseTaskHandoff(in); err != nil || got != want {
			t.Errorf("ParseTaskHandoff(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseTaskHandoff("sometimes"); err == nil {
		t.Fatal("unknown task handoff accepted")
	}
	// Only sell tasks negotiate, so [sell] and deliver-qr don't take it.
	if _, err := ParseHandoff("on_agreement"); err == nil {
		t.Fatal("deliver-qr handoff accepted on_agreement")
	}
}
