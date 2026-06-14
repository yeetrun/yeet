// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/miekg/dns"
)

func RunDNSServer(ctx context.Context, cfg *Config) error {
	if cfg == nil || cfg.DB == nil {
		return fmt.Errorf("DNS server requires a DB")
	}
	server := &dns.Server{
		Addr:         yeetDNSListenAddr,
		Net:          "udp",
		Handler:      newYeetDNSHandler(cfg.DB, nil),
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
