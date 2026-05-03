// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
)

func overlaps(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

var (
	listHostsStatusFn = func(ctx context.Context) (*ipnstate.Status, error) {
		var lc local.Client
		return lc.Status(ctx)
	}
	listHostsCatchInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		var info serverInfo
		if err := newRPCClient(host).Call(ctx, "catch.Info", nil, &info); err != nil {
			return serverInfo{}, err
		}
		return info, nil
	}
)

func HandleListHosts(ctx context.Context, tags []string) error {
	return handleListHosts(ctx, tags, os.Stdout)
}

func handleListHosts(ctx context.Context, tags []string, out io.Writer) error {
	st, err := listHostsStatusFn(ctx)
	if err != nil {
		return err
	}
	_, selfDomain, _ := strings.Cut(st.Self.DNSName, ".")
	if len(tags) == 0 {
		tags = []string{"tag:catch"}
	}

	rows := []listHostRow{}
	for _, peer := range st.Peer {
		if peer.Tags == nil || !overlaps(peer.Tags.AsSlice(), tags) {
			continue
		}
		host, domain, _ := strings.Cut(peer.DNSName, ".")
		if domain != selfDomain {
			continue
		}
		infoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		info, err := listHostsCatchInfoFn(infoCtx, host)
		cancel()
		if err != nil {
			log.Printf("failed to get version for %s: %v", host, err)
			rows = append(rows, listHostRow{Host: host, Version: "unknown", Tags: peer.Tags.AsSlice()})
			continue
		}
		rows = append(rows, listHostRow{Host: host, Version: info.Version, Tags: peer.Tags.AsSlice()})
	}
	return renderListHosts(out, rows)
}

type listHostRow struct {
	Host    string
	Version string
	Tags    []string
}

func renderListHosts(out io.Writer, rows []listHostRow) error {
	w := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(w, "HOST\tVERSION\tTAGS"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", row.Host, row.Version, strings.Join(row.Tags, ",")); err != nil {
			return err
		}
	}
	return w.Flush()
}
