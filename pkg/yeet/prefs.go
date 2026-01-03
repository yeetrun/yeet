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
	"strconv"

	"github.com/shayne/yargs"
)

var (
	prefsFile       = filepath.Join(os.Getenv("HOME"), ".yeet", "prefs.json")
	serviceOverride string
	hostOverride    string
	hostOverrideSet bool
)

const (
	defaultHost    = "catch"
	defaultRPCPort = 41548
)

func init() {
	if err := loadedPrefs.load(); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("failed to load preferences: %v", err)
		}
	}
	if host := os.Getenv("CATCH_HOST"); host != "" {
		loadedPrefs.Host = host
	}
	if port := os.Getenv("CATCH_RPC_PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			loadedPrefs.RPCPort = p
		}
	}
	if loadedPrefs.Host == "" {
		loadedPrefs.Host = defaultHost
	}
	if loadedPrefs.RPCPort == 0 {
		loadedPrefs.RPCPort = defaultRPCPort
	}
}

var loadedPrefs prefs

type prefs struct {
	changed bool   `json:"-"`
	Host    string `json:"host"`
	RPCPort int    `json:"rpcPort"`
}

func SetHost(host string) {
	if host == "" {
		return
	}
	if host != loadedPrefs.Host {
		loadedPrefs.Host = host
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
	return loadedPrefs.Host
}

func SetRPCPort(port int) {
	if port == 0 {
		return
	}
	if port != loadedPrefs.RPCPort {
		loadedPrefs.RPCPort = port
		loadedPrefs.changed = true
	}
}

func RPCPort() int {
	return loadedPrefs.RPCPort
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
