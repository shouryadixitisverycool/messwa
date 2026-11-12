package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"messwa/internal/portal"
)

// proxyMeal sells someone else's meal through a sell task: a friend's QR
// saved as an image, with the mess and diet stated by hand. While
// <state_dir>/proxy-meal.json names a date and meal, tasks for that meal
// take the mess from the file and send its image instead of reading the
// portal. Other meals still use the portal. The file is read on every
// call, so deleting it turns this off without a restart.
type proxyMeal struct {
	Date string `json:"date"`
	Meal string `json:"meal"`
	Mess string `json:"mess"` // portal style, e.g. "kadamba-nonveg"
	QR   string `json:"qr"`   // image file, relative to state_dir; empty when the user sends it
	// Extras to mention in the offer, e.g. ["2 omelette"].
	Extras []string `json:"extras,omitempty"`
}

type proxyPortal struct {
	stateDir string
	real     func(context.Context) (mealPortal, error)
}

func (p proxyPortal) load(date, meal string) (*proxyMeal, []byte, error) {
	raw, err := os.ReadFile(filepath.Join(p.stateDir, "proxy-meal.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	// One meal, or a list of them under different slot names.
	var list []proxyMeal
	if err := json.Unmarshal(raw, &list); err != nil {
		var one proxyMeal
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, nil, err
		}
		list = []proxyMeal{one}
	}
	for _, m := range list {
		if m.Date != date || m.Meal != meal {
			continue
		}
		if m.QR == "" {
			// No image: the user sends the QR, so the task must hand off
			// before payment. A QR send fails on the empty image.
			return &m, nil, nil
		}
		img, err := os.ReadFile(filepath.Join(p.stateDir, m.QR))
		if err != nil {
			return nil, nil, err
		}
		return &m, img, nil
	}
	return nil, nil, nil
}

func (m proxyMeal) registration() portal.Registration {
	return portal.Registration{Date: m.Date, Meal: baseMeal(m.Meal), Mess: m.Mess, Category: "regular"}
}

func (p proxyPortal) RequireRegistration(ctx context.Context, date, meal string) (portal.Registration, error) {
	m, _, err := p.load(date, meal)
	if err != nil || m != nil {
		if m == nil {
			return portal.Registration{}, err
		}
		return m.registration(), nil
	}
	r, err := p.real(ctx)
	if err != nil {
		return portal.Registration{}, err
	}
	return r.RequireRegistration(ctx, date, baseMeal(meal))
}

func (p proxyPortal) QRForMeal(ctx context.Context, date, meal, accountHash string) (portal.CurrentQR, error) {
	m, img, err := p.load(date, meal)
	if err != nil {
		return portal.CurrentQR{}, err
	}
	if m != nil {
		sum := sha256.Sum256(img)
		return portal.CurrentQR{PNG: img, AccountHash: "proxy:" + hex.EncodeToString(sum[:8]), Registration: m.registration()}, nil
	}
	r, err := p.real(ctx)
	if err != nil {
		return portal.CurrentQR{}, err
	}
	return r.QRForMeal(ctx, date, baseMeal(meal), accountHash)
}

// ExtraRegistrations lists the proxy meal's extras from the file, and for
// any other meal reads the portal.
func (p proxyPortal) ExtraRegistrations(ctx context.Context, date, meal string) ([]portal.ExtraRegistration, error) {
	m, _, err := p.load(date, meal)
	if err != nil {
		return nil, err
	}
	if m != nil {
		var rows []portal.ExtraRegistration
		for _, name := range m.Extras {
			rows = append(rows, portal.ExtraRegistration{Date: date, Meal: baseMeal(meal), Mess: m.Mess, Name: name})
		}
		return rows, nil
	}
	r, err := p.real(ctx)
	if err != nil {
		return nil, err
	}
	ep, ok := r.(extrasPortal)
	if !ok {
		return nil, nil
	}
	return ep.ExtraRegistrations(ctx, date, baseMeal(meal))
}
