// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/miekg/dns"
	"github.com/yeetrun/yeet/pkg/db"
)

func RunDNSServer(ctx context.Context, cfg *Config) error {
	if cfg == nil || cfg.DB == nil {
		return fmt.Errorf("DNS server requires a DB")
	}
	server := &dns.Server{
		Addr:         yeetDNSListenAddr,
		Net:          "udp",
		Handler:      newYeetDNSHandler(dnsStoreForConfig(cfg), nil),
		ReadTimeout:  yeetDNSReadTimeout,
		WriteTimeout: yeetDNSWriteTimeout,
	}
	errc := make(chan error, 1)
	go func() {
		log.Printf("starting yeet DNS on udp/%s", yeetDNSListenAddr)
		errc <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return server.ShutdownContext(shutdownCtx)
	case err := <-errc:
		return err
	}
}

func dnsStoreForConfig(cfg *Config) dnsDataStore {
	if cfg == nil {
		return nil
	}
	if cfg.RootDir == "" {
		return cfg.DB
	}
	servicesRoot := cfg.ServicesRoot
	if servicesRoot == "" {
		servicesRoot = filepath.Join(cfg.RootDir, "services")
	}
	return freshDNSStore{
		dbPath:       filepath.Join(cfg.RootDir, "db.json"),
		servicesRoot: servicesRoot,
	}
}

type freshDNSStore struct {
	dbPath       string
	servicesRoot string
}

func (s freshDNSStore) Get() (db.DataView, error) {
	return db.NewStore(s.dbPath, s.servicesRoot).Get()
}
