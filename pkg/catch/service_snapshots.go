// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

type snapshotEvent string

const (
	snapshotEventRun                  snapshotEvent = "run"
	snapshotEventDockerUpdate         snapshotEvent = "docker-update"
	snapshotEventServiceRootMigration snapshotEvent = "service-root-migration"
	defaultSnapshotMaxAge                           = 7 * 24 * time.Hour
	defaultSnapshotKeepLast                         = 5
)

var snapshotMaxAgeDaysRE = regexp.MustCompile(`^([0-9]+)d$`)

type effectivePolicy struct {
	Enabled  bool
	KeepLast int
	MaxAge   time.Duration
	Events   map[snapshotEvent]struct{}
	Required bool
}

func (p effectivePolicy) Allows(event snapshotEvent) bool {
	_, ok := p.Events[event]
	return ok
}

type snapshotCreateRequest struct {
	Service    string
	Dataset    string
	Event      snapshotEvent
	Generation int
	Now        time.Time
}

type listedSnapshot struct {
	Name      string
	Created   time.Time
	CreatedBy string
	Service   string
}

func effectiveSnapshotPolicy(server, service *db.SnapshotPolicy) (effectivePolicy, error) {
	raw := db.SnapshotPolicy{
		Enabled:  boolPointer(true),
		KeepLast: intPointer(defaultSnapshotKeepLast),
		MaxAge:   "",
		Events: []string{
			string(snapshotEventRun),
			string(snapshotEventDockerUpdate),
			string(snapshotEventServiceRootMigration),
		},
		Required: boolPointer(true),
	}
	applySnapshotPolicyOverride(&raw, server)
	applySnapshotPolicyOverride(&raw, service)

	maxAge, err := parseSnapshotMaxAge(raw.MaxAge)
	if err != nil {
		return effectivePolicy{}, err
	}

	enabled := raw.Enabled != nil && *raw.Enabled
	keepLast := defaultSnapshotKeepLast
	if raw.KeepLast != nil {
		keepLast = *raw.KeepLast
	}
	if enabled && keepLast < 1 {
		return effectivePolicy{}, fmt.Errorf("snapshot keep-last must be at least 1 when snapshots are enabled")
	}

	events, err := effectiveSnapshotEvents(raw.Events)
	if err != nil {
		return effectivePolicy{}, err
	}

	required := raw.Required != nil && *raw.Required
	return effectivePolicy{
		Enabled:  enabled,
		KeepLast: keepLast,
		MaxAge:   maxAge,
		Events:   events,
		Required: required,
	}, nil
}

func applySnapshotPolicyOverride(dst *db.SnapshotPolicy, src *db.SnapshotPolicy) {
	if src == nil {
		return
	}
	if src.Enabled != nil {
		dst.Enabled = src.Enabled
	}
	if src.KeepLast != nil {
		dst.KeepLast = src.KeepLast
	}
	if src.MaxAge != "" {
		dst.MaxAge = src.MaxAge
	}
	if src.Events != nil {
		dst.Events = src.Events
	}
	if src.Required != nil {
		dst.Required = src.Required
	}
}

func effectiveSnapshotEvents(raw []string) (map[snapshotEvent]struct{}, error) {
	events := make(map[snapshotEvent]struct{}, len(raw))
	for _, event := range raw {
		switch snapshotEvent(event) {
		case snapshotEventRun, snapshotEventDockerUpdate, snapshotEventServiceRootMigration:
			events[snapshotEvent(event)] = struct{}{}
		default:
			return nil, fmt.Errorf("invalid snapshot event %q", event)
		}
	}
	return events, nil
}

func parseSnapshotMaxAge(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultSnapshotMaxAge, nil
	}
	if match := snapshotMaxAgeDaysRE.FindStringSubmatch(raw); match != nil {
		days, err := strconv.Atoi(match[1])
		if err != nil {
			return 0, fmt.Errorf("invalid snapshot max age %q", raw)
		}
		d := time.Duration(days) * 24 * time.Hour
		if d <= 0 {
			return 0, fmt.Errorf("snapshot max age must be positive")
		}
		return d, nil
	}

	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid snapshot max age %q", raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("snapshot max age must be positive")
	}
	return d, nil
}

func createServiceSnapshot(ctx context.Context, runner zfsCommandRunner, req snapshotCreateRequest) (string, error) {
	if runner == nil {
		runner = runZFSCommand
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	snapshotName := fmt.Sprintf("%s@yeet-%s-%s-g%d", req.Dataset, now.UTC().Format("20060102T150405Z"), req.Event, req.Generation)
	args := []string{
		"snapshot",
		"-o", "com.yeetrun:created-by=catch",
		"-o", "com.yeetrun:service=" + req.Service,
		"-o", "com.yeetrun:event=" + string(req.Event),
		"-o", "com.yeetrun:generation=" + strconv.Itoa(req.Generation),
		"-o", "com.yeetrun:policy-version=1",
		snapshotName,
	}
	_, stderr, err := runner(ctx, args...)
	if err != nil {
		return "", formatZFSCommandError("zfs snapshot "+snapshotName, stderr, err)
	}
	return snapshotName, nil
}

func listServiceSnapshots(ctx context.Context, runner zfsCommandRunner, dataset string) ([]listedSnapshot, error) {
	if runner == nil {
		runner = runZFSCommand
	}
	stdout, stderr, err := runner(ctx, "list", "-H", "-p", "-t", "snapshot", "-o", "name,creation,com.yeetrun:created-by,com.yeetrun:service", "-s", "creation", dataset)
	if err != nil {
		return nil, formatZFSCommandError("zfs list snapshots "+dataset, stderr, err)
	}
	return parseListedSnapshots(stdout)
}

func parseListedSnapshots(raw string) ([]listedSnapshot, error) {
	var snapshots []listedSnapshot
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			return nil, fmt.Errorf("invalid zfs snapshot row %q", line)
		}
		createdUnix, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid zfs snapshot creation %q: %w", fields[1], err)
		}
		snapshots = append(snapshots, listedSnapshot{
			Name:      fields[0],
			Created:   time.Unix(createdUnix, 0).UTC(),
			CreatedBy: zfsPropertyValue(fields[2]),
			Service:   zfsPropertyValue(fields[3]),
		})
	}
	return snapshots, nil
}

func snapshotsToPrune(snaps []listedSnapshot, policy effectivePolicy, now time.Time, current string) []string {
	owned := catchOwnedYeetSnapshots(snaps)
	sort.SliceStable(owned, func(i, j int) bool {
		return owned[i].Created.After(owned[j].Created)
	})

	prune := make(map[string]struct{})
	for i, snap := range owned {
		if snap.Name == current {
			continue
		}
		if shouldPruneSnapshot(snap, policy, now, i) {
			prune[snap.Name] = struct{}{}
		}
	}

	var names []string
	for _, snap := range snaps {
		if _, ok := prune[snap.Name]; ok {
			names = append(names, snap.Name)
		}
	}
	return names
}

func catchOwnedYeetSnapshots(snaps []listedSnapshot) []listedSnapshot {
	owned := make([]listedSnapshot, 0, len(snaps))
	for _, snap := range snaps {
		if snap.CreatedBy == "catch" && strings.Contains(snap.Name, "@yeet-") {
			owned = append(owned, snap)
		}
	}
	return owned
}

func shouldPruneSnapshot(snap listedSnapshot, policy effectivePolicy, now time.Time, newestIndex int) bool {
	return snapshotExpired(snap, policy, now) || snapshotOutsideRetention(policy, newestIndex)
}

func snapshotExpired(snap listedSnapshot, policy effectivePolicy, now time.Time) bool {
	return policy.MaxAge > 0 && now.Sub(snap.Created) > policy.MaxAge
}

func snapshotOutsideRetention(policy effectivePolicy, newestIndex int) bool {
	return policy.KeepLast > 0 && newestIndex >= policy.KeepLast
}

func destroySnapshot(ctx context.Context, runner zfsCommandRunner, name string) error {
	if runner == nil {
		runner = runZFSCommand
	}
	_, stderr, err := runner(ctx, "destroy", name)
	if err != nil {
		return formatZFSCommandError("zfs destroy "+name, stderr, err)
	}
	return nil
}

func zfsPropertyValue(raw string) string {
	if raw == "-" {
		return ""
	}
	return raw
}

func boolPointer(v bool) *bool {
	return &v
}

func intPointer(v int) *int {
	return &v
}
