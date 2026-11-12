package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestSettings(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, v0, err := s.Settings(ctx)
	if err != nil || len(got) != 0 || v0 != 0 {
		t.Fatalf("empty store: %v %d %v", got, v0, err)
	}
	if err := s.SaveSettings(ctx, map[string]json.RawMessage{"sell.show_price": json.RawMessage(`false`), "sell.post_gap": json.RawMessage(`30`)}, 1); err != nil {
		t.Fatal(err)
	}
	got, v1, _ := s.Settings(ctx)
	if string(got["sell.show_price"]) != "false" || string(got["sell.post_gap"]) != "30" || v1 <= v0 {
		t.Fatalf("after save: %v version %d", got, v1)
	}
	// Saving replaces the whole set: post_gap goes back to config.toml.
	if err := s.SaveSettings(ctx, map[string]json.RawMessage{"sell.show_price": json.RawMessage(`true`)}, 0); err != nil {
		t.Fatal(err)
	}
	got, v2, _ := s.Settings(ctx)
	if len(got) != 1 || string(got["sell.show_price"]) != "true" || v2 <= v1 {
		t.Fatalf("after replace: %v version %d", got, v2)
	}
	if v, _ := s.SettingsVersion(ctx); v != v2 {
		t.Fatalf("SettingsVersion = %d, want %d", v, v2)
	}
}
