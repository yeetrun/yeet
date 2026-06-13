// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
)

type snapshotEvent string

const (
	snapshotEventRun                  snapshotEvent = "run"
	snapshotEventDockerUpdate         snapshotEvent = "docker-update"
	snapshotEventServiceRootMigration snapshotEvent = "service-root-migration"
	snapshotEventVMManual             snapshotEvent = "vm-manual"
	defaultSnapshotMaxAge                           = 7 * 24 * time.Hour
	defaultSnapshotKeepLast                         = 5
)

var (
	snapshotMaxAgeDaysRE = regexp.MustCompile(`^(-?[0-9]+)d$`)
	snapshotNameCleaner  = regexp.MustCompile(`[^A-Za-z0-9_.:-]+`)
)

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
	Comment    string
	Checkpoint string
}

type listedSnapshot struct {
	Name      string
	Created   time.Time
	CreatedBy string
	Service   string
}

type snapshotOperation struct {
	Service   *db.Service
	Event     snapshotEvent
	Writer    io.Writer
	Operation func() error
}

func (s *Server) withServiceSnapshot(ctx context.Context, op snapshotOperation) error {
	if op.Operation == nil {
		return nil
	}
	if skipsServiceSnapshot(s, op) {
		return op.Operation()
	}

	policy, err := s.serviceSnapshotPolicy(op.Service)
	if err != nil {
		return err
	}
	if !policy.Enabled || !policy.Allows(op.Event) {
		return op.Operation()
	}

	now := time.Now()
	snapshotName, err := s.createSnapshotForOperation(ctx, op, now)
	if err != nil {
		if policy.Required {
			return err
		}
		writeSnapshotWarning(op.Writer, "warning: failed to create ZFS snapshot for %q: %v\n", op.Service.Name, err)
		return op.Operation()
	}

	opErr := op.Operation()
	return s.finishSnapshotOperation(ctx, op, policy, now, snapshotName, opErr)
}

func skipsServiceSnapshot(s *Server, op snapshotOperation) bool {
	return s == nil ||
		s.cfg.DB == nil ||
		op.Service == nil ||
		strings.TrimSpace(op.Service.ServiceRootZFS) == "" ||
		isInitialRunSnapshot(op.Service, op.Event)
}

func (s *Server) serviceSnapshotPolicy(service *db.Service) (effectivePolicy, error) {
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return effectivePolicy{}, err
	}
	serverPolicy := snapshotPolicyPtrFromView(dv.SnapshotDefaults())
	return effectiveSnapshotPolicy(serverPolicy, service.SnapshotPolicy)
}

func (s *Server) createSnapshotForOperation(ctx context.Context, op snapshotOperation, now time.Time) (string, error) {
	return createServiceSnapshot(ctx, s.zfsRunner, snapshotCreateRequest{
		Service:    op.Service.Name,
		Dataset:    op.Service.ServiceRootZFS,
		Event:      op.Event,
		Generation: op.Service.Generation,
		Now:        now,
	})
}

func (s *Server) finishSnapshotOperation(ctx context.Context, op snapshotOperation, policy effectivePolicy, now time.Time, snapshotName string, opErr error) error {
	if err := s.pruneServiceSnapshots(ctx, op.Service, policy, now, snapshotName); err != nil {
		writeSnapshotWarning(op.Writer, "warning: failed to prune ZFS snapshots for %q: %v\n", op.Service.Name, err)
	}
	if opErr != nil {
		writeSnapshotWarning(op.Writer, "recovery snapshot: %s\n", snapshotName)
		return opErr
	}
	return nil
}

func isInitialRunSnapshot(service *db.Service, event snapshotEvent) bool {
	return event == snapshotEventRun && service.Generation == 0 && service.LatestGeneration == 0
}

func writeSnapshotWarning(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	_, _ = fmt.Fprintf(w, format, args...)
}

func (s *Server) pruneServiceSnapshots(ctx context.Context, service *db.Service, policy effectivePolicy, now time.Time, current string) error {
	if service == nil || strings.TrimSpace(service.ServiceRootZFS) == "" {
		return nil
	}
	_, err := s.pruneServiceSnapshotsForDataset(ctx, service.ServiceRootZFS, service, policy, now, current)
	return err
}

func (s *Server) pruneServiceSnapshotsForDataset(ctx context.Context, dataset string, service *db.Service, policy effectivePolicy, now time.Time, current string) ([]string, error) {
	if service == nil || strings.TrimSpace(dataset) == "" {
		return nil, nil
	}
	snaps, err := listServiceSnapshots(ctx, s.zfsRunner, dataset)
	if err != nil {
		return nil, err
	}
	names := snapshotsToPrune(snaps, service.Name, policy, now, current)
	destroyed := make([]string, 0, len(names))
	for _, name := range names {
		if err := destroySnapshot(ctx, s.zfsRunner, name); err != nil {
			return destroyed, err
		}
		destroyed = append(destroyed, name)
	}
	return destroyed, nil
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

func parseSnapshotEvents(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	events := make([]string, 0, len(parts))
	for _, part := range parts {
		event := strings.TrimSpace(part)
		if event == "" {
			return nil, fmt.Errorf("snapshot events must not contain empty values")
		}
		events = append(events, event)
	}
	if _, err := effectiveSnapshotEvents(events); err != nil {
		return nil, err
	}
	return events, nil
}

func snapshotPolicyPtrFromView(view db.SnapshotPolicyView) *db.SnapshotPolicy {
	if !view.Valid() {
		return nil
	}
	return view.AsStruct()
}

func snapshotPolicyRPC(policy *db.SnapshotPolicy) *catchrpc.SnapshotPolicy {
	if policy == nil {
		return nil
	}
	out := &catchrpc.SnapshotPolicy{
		MaxAge: policy.MaxAge,
		Events: append([]string(nil), policy.Events...),
	}
	if policy.Enabled != nil {
		out.Enabled = boolPointer(*policy.Enabled)
	}
	if policy.KeepLast != nil {
		out.KeepLast = intPointer(*policy.KeepLast)
	}
	if policy.Required != nil {
		out.Required = boolPointer(*policy.Required)
	}
	return out
}

func effectiveSnapshotPolicyRPCWithPreferred(policy effectivePolicy, preferredMaxAge string) catchrpc.EffectiveSnapshotPolicy {
	return catchrpc.EffectiveSnapshotPolicy{
		Enabled:  policy.Enabled,
		KeepLast: policy.KeepLast,
		MaxAge:   formatEffectiveSnapshotMaxAge(policy.MaxAge, preferredMaxAge),
		Events:   effectiveSnapshotEventStrings(policy.Events),
		Required: policy.Required,
	}
}

func preferredEffectiveSnapshotMaxAge(server, service *db.SnapshotPolicy) string {
	if service != nil && service.MaxAge != "" {
		return service.MaxAge
	}
	if server != nil && server.MaxAge != "" {
		return server.MaxAge
	}
	return "7d"
}

func formatEffectiveSnapshotMaxAge(maxAge time.Duration, preferred string) string {
	if preferred != "" {
		if parsed, err := parseSnapshotMaxAge(preferred); err == nil && parsed == maxAge {
			return preferred
		}
	}
	if maxAge == defaultSnapshotMaxAge {
		return "7d"
	}
	if maxAge%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", maxAge/(24*time.Hour))
	}
	return maxAge.String()
}

func effectiveSnapshotEventStrings(events map[snapshotEvent]struct{}) []string {
	ordered := []snapshotEvent{
		snapshotEventRun,
		snapshotEventDockerUpdate,
		snapshotEventServiceRootMigration,
	}
	out := make([]string, 0, len(events))
	for _, event := range ordered {
		if _, ok := events[event]; ok {
			out = append(out, string(event))
		}
	}
	return out
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
		if days > int(math.MaxInt64/(24*time.Hour)) {
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
	return createServiceSnapshotWithSuffix(ctx, runner, req, generateRandomSnapshotSuffix)
}

func createServiceSnapshotWithSuffix(ctx context.Context, runner zfsCommandRunner, req snapshotCreateRequest, suffixFn func() (string, error)) (string, error) {
	if runner == nil {
		runner = runZFSCommand
	}
	shortName := snapshotShortName(req)
	snapshotName := req.Dataset + "@" + shortName
	stderr, err := runZFSSnapshot(ctx, runner, req, snapshotName)
	if err == nil {
		return snapshotName, nil
	}
	if !isZFSSnapshotNameCollision(stderr) {
		return "", formatZFSCommandError("zfs snapshot "+snapshotName, stderr, err)
	}

	suffix, suffixErr := suffixFn()
	if suffixErr != nil {
		return "", fmt.Errorf("generate snapshot suffix after name collision: %w", suffixErr)
	}
	snapshotName = req.Dataset + "@" + shortName + "-" + suffix
	stderr, err = runZFSSnapshot(ctx, runner, req, snapshotName)
	if err != nil {
		return "", formatZFSCommandError("zfs snapshot "+snapshotName, stderr, err)
	}
	return snapshotName, nil
}

func runZFSSnapshot(ctx context.Context, runner zfsCommandRunner, req snapshotCreateRequest, snapshotName string) (string, error) {
	args := []string{
		"snapshot",
		"-o", "com.yeetrun:created-by=catch",
		"-o", "com.yeetrun:service=" + req.Service,
		"-o", "com.yeetrun:event=" + string(req.Event),
		"-o", "com.yeetrun:generation=" + strconv.Itoa(req.Generation),
		"-o", "com.yeetrun:policy-version=1",
	}
	if comment := strings.TrimSpace(req.Comment); comment != "" {
		args = append(args, "-o", "com.yeetrun:comment="+comment)
	}
	if checkpoint := strings.TrimSpace(req.Checkpoint); checkpoint != "" {
		args = append(args, "-o", "com.yeetrun:checkpoint="+checkpoint)
	}
	args = append(args, snapshotName)
	_, stderr, err := runner(ctx, args...)
	return stderr, err
}

func snapshotShortName(req snapshotCreateRequest) string {
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	event := snapshotNameCleaner.ReplaceAllString(string(req.Event), "_")
	return fmt.Sprintf("yeet-%s-%s-g%d", now.UTC().Format("20060102T150405Z"), event, req.Generation)
}

func isZFSSnapshotNameCollision(stderr string) bool {
	stderr = strings.ToLower(stderr)
	if !strings.Contains(stderr, "already exists") {
		return false
	}
	return strings.Contains(stderr, "snapshot") || strings.Contains(stderr, "@yeet-")
}

func generateRandomSnapshotSuffix() (string, error) {
	var raw [3]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
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

func snapshotsToPrune(snaps []listedSnapshot, service string, policy effectivePolicy, now time.Time, current string) []string {
	owned := catchOwnedYeetSnapshotsForService(snaps, service)
	sort.SliceStable(owned, func(i, j int) bool {
		if !owned[i].Created.Equal(owned[j].Created) {
			return owned[i].Created.After(owned[j].Created)
		}
		return owned[i].Name < owned[j].Name
	})

	prune := make(map[string]struct{})
	for i, snap := range owned {
		if snap.Name == current {
			continue
		}
		if current == "" && i == 0 {
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

func catchOwnedYeetSnapshotsForService(snaps []listedSnapshot, service string) []listedSnapshot {
	owned := make([]listedSnapshot, 0, len(snaps))
	for _, snap := range snaps {
		if snap.CreatedBy == "catch" && snap.Service == service && strings.Contains(snap.Name, "@yeet-") {
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
