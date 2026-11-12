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

func TestAcceptPlainDMsSetting(t *testing.T) {
	if Default().Sell.AcceptPlainDMs {
		t.Fatal("plain DMs are accepted by default")
	}
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte("[sell]\naccept_plain_dms = true\n"), 0o600)
	c, err := Load(p)
	if err != nil || !c.Sell.AcceptPlainDMs {
		t.Fatalf("accept_plain_dms = true loaded as %v, %v", c.Sell.AcceptPlainDMs, err)
	}
}

func TestAcceptPaymentImagesSetting(t *testing.T) {
	if Default().Sell.AcceptPaymentImages {
		t.Fatal("payment images are accepted by default")
	}
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte("[sell]\naccept_payment_images = true\n"), 0o600)
	c, err := Load(p)
	if err != nil || !c.Sell.AcceptPaymentImages {
		t.Fatalf("accept_payment_images = true loaded as %v, %v", c.Sell.AcceptPaymentImages, err)
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

func TestBuyersSetting(t *testing.T) {
	c := Default()
	if c.Sell.Buyers.UnsavedOnly() {
		t.Fatal("default config turns saved contacts away")
	}
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte("[sell]\nbuyers = \"unsaved_only\"\n"), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Sell.Buyers.UnsavedOnly() {
		t.Fatalf("buyers = %q", c.Sell.Buyers)
	}
	os.WriteFile(p, []byte("[sell]\nbuyers = \"anyone\"\n"), 0o600)
	if c, err = Load(p); err != nil || c.Sell.Buyers.UnsavedOnly() {
		t.Fatalf("anyone = %q, %v", c.Sell.Buyers, err)
	}
	os.WriteFile(p, []byte("[sell]\nbuyers = \"unsaved\"\n"), 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("unknown buyers value loaded")
	}
}
