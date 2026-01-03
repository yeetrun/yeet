// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/shayne/yargs"
)

var (
	prefsFile       = filepath.Join(os.Getenv("HOME"), ".yeet", "prefs.json")
	serviceOverride string
	hostOverride    string
	hostOverrideSet bool
)

const (
	defaultCatchHost = "catch"
	defaultRPCPort   = 41548
)

func init() {
	if err := loadedPrefs.load(); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("failed to load preferences: %v", err)
		}
	}
	if host := os.Getenv("CATCH_HOST"); host != "" {
		loadedPrefs.DefaultHost = host
	}
	if loadedPrefs.DefaultHost == "" {
		loadedPrefs.DefaultHost = defaultCatchHost
	}
}

var loadedPrefs prefs

type prefs struct {
	changed     bool   `json:"-"`
	DefaultHost string `json:"defaultHost"`
}

func SetHost(host string) {
	if host == "" {
		return
	}
	if host != loadedPrefs.DefaultHost {
		loadedPrefs.DefaultHost = host
		loadedPrefs.changed = true
	}
}

func SetHostOverride(host string) {
	if host == "" {
		return
	}
	hostOverride = host
	hostOverrideSet = true
	SetHost(host)
}

func HostOverride() (string, bool) {
	if !hostOverrideSet {
		return "", false
	}
	return hostOverride, true
}

func resetHostOverride() {
	hostOverride = ""
	hostOverrideSet = false
}

func Host() string {
	return loadedPrefs.DefaultHost
}

func SetServiceOverride(service string) {
	svc, host, ok := splitServiceHost(service)
	if ok && host != "" {
		SetHostOverride(host)
	}
	serviceOverride = svc
}

func (p *prefs) save() error {
	if err := os.MkdirAll(filepath.Dir(prefsFile), 0o755); err != nil {
		return err
	}
	j, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(prefsFile, j, 0o600)
}

func (p *prefs) load() error {
	fp := filepath.Join(os.Getenv("HOME"), ".yeet", "prefs.json")
	j, err := os.ReadFile(fp)
	if err != nil {
		return err
	}
	return json.Unmarshal(j, p)
}

func asJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

type prefsFlagsParsed struct {
	Save bool `flag:"save"`
}

func HandlePrefs(_ context.Context, args []string) error {
	if len(args) > 0 && args[0] == "prefs" {
		args = args[1:]
	}
	result, err := yargs.ParseFlags[prefsFlagsParsed](args)
	if err != nil {
		return err
	}
	fmt.Println(asJSON(loadedPrefs))
	if result.Flags.Save {
		if !loadedPrefs.changed {
			fmt.Fprintln(os.Stderr, "No changes to save")
			return nil
		}
		if err := loadedPrefs.save(); err != nil {
			return fmt.Errorf("failed to save preferences: %w", err)
		}
		fmt.Fprintln(os.Stderr, "Prefs saved")
	} else if loadedPrefs.changed {
		fmt.Fprintln(os.Stderr, "Use --save to save the prefs")
	}
	return nil
}
