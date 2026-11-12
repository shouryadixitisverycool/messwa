package main

import (
	"context"
	"errors"
	"messwa/internal/portal"
	"os"
	"path/filepath"
	"testing"
)

func TestProxyPortal(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "q.png"), []byte("img"), 0o600)
	os.WriteFile(filepath.Join(dir, "proxy-meal.json"), []byte(`{"date":"2026-11-12","meal":"dinner","mess":"kadamba-nonveg","qr":"q.png","extras":["2 omelette"]}`), 0o600)
	realCalled := false
	p := proxyPortal{stateDir: dir, real: func(context.Context) (mealPortal, error) { realCalled = true; return nil, errors.New("no portal") }}
	qr, err := p.QRForMeal(context.Background(), "2026-11-12", "dinner", "")
	if err != nil || string(qr.PNG) != "img" {
		t.Fatalf("qr %q err %v", qr.PNG, err)
	}
	if mess, diet := qr.Registration.MessDiet(); mess != "kadamba" || diet != "nonveg" {
		t.Fatalf("mess %s diet %s", mess, diet)
	}
	if realCalled {
		t.Fatal("proxy meal read the portal")
	}
	if _, err := p.RequireRegistration(context.Background(), "2026-11-12", "lunch"); err == nil || !realCalled {
		t.Fatal("another meal should go to the portal")
	}
	rows, err := p.ExtraRegistrations(context.Background(), "2026-11-12", "dinner")
	if got := offerExtras(rows, "kadamba"); err != nil || len(got) != 1 || got[0] != "2 omelette" {
		t.Fatalf("extras %v err %v", got, err)
	}
}

type slotPortal struct{ meals []string }

func (f *slotPortal) RequireRegistration(_ context.Context, _, meal string) (portal.Registration, error) {
	f.meals = append(f.meals, meal)
	return portal.Registration{Meal: meal, Mess: "bakul-veg"}, nil
}

func (f *slotPortal) QRForMeal(_ context.Context, _, meal, _ string) (portal.CurrentQR, error) {
	f.meals = append(f.meals, meal)
	return portal.CurrentQR{Registration: portal.Registration{Meal: meal, Mess: "bakul-veg"}}, nil
}

func TestProxyPortalSlots(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.png"), []byte("a"), 0o600)
	os.WriteFile(filepath.Join(dir, "proxy-meal.json"), []byte(`[
		{"date":"2026-11-12","meal":"dinner","mess":"kadamba-nonveg","qr":"a.png"},
		{"date":"2026-11-12","meal":"dinner#3","mess":"kadamba-nonveg"}]`), 0o600)
	real := &slotPortal{}
	p := proxyPortal{stateDir: dir, real: func(context.Context) (mealPortal, error) { return real, nil }}
	ctx := context.Background()
	if qr, err := p.QRForMeal(ctx, "2026-11-12", "dinner", ""); err != nil || string(qr.PNG) != "a" {
		t.Fatalf("first proxy: %q %v", qr.PNG, err)
	}
	qr, err := p.QRForMeal(ctx, "2026-11-12", "dinner#3", "")
	if err != nil || qr.PNG != nil || qr.Registration.Meal != "dinner" {
		t.Fatalf("imageless proxy: %+v %v", qr, err)
	}
	if label := portalMealLabel("kadamba", "nonveg", "dinner#3"); label != "kadamba non-veg dinner" {
		t.Fatalf("label %q", label)
	}
	if _, err := p.QRForMeal(ctx, "2026-11-12", "dinner#2", ""); err != nil {
		t.Fatal(err)
	}
	if len(real.meals) != 1 || real.meals[0] != "dinner" {
		t.Fatalf("portal asked for %v, want the base meal", real.meals)
	}
	if _, _, _, _, err := deliveryImageBytes(qr.PNG); err == nil {
		t.Fatal("an imageless proxy must not produce a sendable QR")
	}
}
