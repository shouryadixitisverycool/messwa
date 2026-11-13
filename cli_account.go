package main

import (
	"context"
	"fmt"
	"messwa/internal/accounts"
	"messwa/internal/config"
	"messwa/internal/store"
)

// scopeCLI resolves the account before opening any device or private data.
func scopeCLI(ctx context.Context, cfg *config.Config, name string) (*config.Config, error) {
	st, err := store.Open(cfg.DataDB())
	if err != nil {
		return nil, err
	}
	defer st.Close()
	acc, err := accounts.FromEnv(st)
	if err != nil {
		return nil, err
	}
	var u *accounts.User
	if name == "" {
		u, err = acc.Owner(ctx)
	} else {
		u, err = acc.ByName(ctx, name)
	}
	if err != nil {
		return nil, err
	}
	if u == nil || u.Disabled || u.ID <= 0 {
		return nil, fmt.Errorf("choose an enabled messwa user with --user NAME")
	}
	c, err := accountConfig(ctx, cfg, st, u.ID)
	if err != nil {
		return nil, err
	}
	c.AccountRoot = cfg.StateDir
	private, err := store.Open(c.DataDB())
	if err != nil {
		return nil, err
	}
	defer private.Close()
	saved, _, err := private.Settings(ctx)
	if err != nil {
		return nil, err
	}
	c.ApplySaved(config.Settings(saved))
	if err = private.AssignLegacyTasks(ctx, u.ID); err != nil {
		return nil, err
	}
	return c, nil
}

func openCommandStore(cfg *config.Config) (*store.Store, error) {
	root := cfg.AccountRoot
	if root == "" {
		root = cfg.StateDir
	}
	return store.OpenAccount(cfg.DataDB(), root+"/messwa.db")
}
