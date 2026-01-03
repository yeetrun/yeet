// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"tailscale.com/client/tailscale"
)

func overlaps(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

func HandleListHosts(ctx context.Context, tags []string) error {
	var lc tailscale.LocalClient
	st, err := lc.Status(ctx)
	if err != nil {
		return err
	}
	_, selfDomain, _ := strings.Cut(st.Self.DNSName, ".")
	if len(tags) == 0 {
		tags = []string{"tag:catch"}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	defer w.Flush()

	fmt.Fprintln(w, "HOST\tVERSION\tTAGS")

	for _, peer := range st.Peer {
		if peer.Tags == nil || !overlaps(peer.Tags.AsSlice(), tags) {
			continue
		}
		host, domain, _ := strings.Cut(peer.DNSName, ".")
		if domain != selfDomain {
			continue
		}
		rpc := newRPCClient(host)
		infoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		var info serverInfo
		if err := rpc.Call(infoCtx, "catch.Info", nil, &info); err != nil {
			log.Printf("failed to get version for %s: %v", host, err)
			fmt.Fprintf(w, "%s\t%s\t%s\n", host, "unknown", strings.Join(peer.Tags.AsSlice(), ","))
			cancel()
			continue
		}
		cancel()
		fmt.Fprintf(w, "%s\t%s\t%s\n", host, info.Version, strings.Join(peer.Tags.AsSlice(), ","))
	}
	return nil
}
