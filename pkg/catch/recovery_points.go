// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/cmdutil"
	"github.com/yeetrun/yeet/pkg/db"
)

const (
	recoveryStorageServiceRoot = "service-root-dataset"
	recoveryStorageVMZVOL      = "vm-zvol"
	recoveryModeServiceRoot    = "service-root"
	recoveryModeDisk           = "disk"
)

type recoveryPoint struct {
	Service     string    `json:"service"`
	ServiceType string    `json:"serviceType"`
	StorageKind string    `json:"storageKind"`
	Dataset     string    `json:"dataset"`
	Name        string    `json:"name"`
	ShortName   string    `json:"shortName"`
	Created     time.Time `json:"created"`
	CreatedBy   string    `json:"createdBy"`
	Event       string    `json:"event"`
	Generation  *int      `json:"generation,omitempty"`
	Comment     string    `json:"comment,omitempty"`
	Mode        string    `json:"mode"`
	Protected   bool      `json:"protected"`
	Actions     []string  `json:"actions"`
	Retention   string    `json:"retention"`
}

type recoveryTarget struct {
	Service     *db.Service
	ServiceType string
	StorageKind string
	Dataset     string
}

func recoveryTargetForService(service *db.Service) (recoveryTarget, bool, error) {
	if service == nil {
		return recoveryTarget{}, false, nil
	}
	if service.ServiceType == db.ServiceTypeVM && service.VM != nil {
		dataset, err := vmSnapshotDataset(service.VM.Disk)
		if err != nil {
			return recoveryTarget{}, false, err
		}
		return recoveryTarget{
			Service:     service,
			ServiceType: string(db.ServiceTypeVM),
			StorageKind: recoveryStorageVMZVOL,
			Dataset:     dataset,
		}, true, nil
	}
	if strings.TrimSpace(service.ServiceRootZFS) != "" {
		return recoveryTarget{
			Service:     service,
			ServiceType: string(service.ServiceType),
			StorageKind: recoveryStorageServiceRoot,
			Dataset:     service.ServiceRootZFS,
		}, true, nil
	}
	return recoveryTarget{}, false, nil
}

func (s *Server) listRecoveryPoints(ctx context.Context, serviceName string) ([]recoveryPoint, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, err
	}
	serviceName = strings.TrimSpace(serviceName)
	services, err := recoveryPointServices(dv, serviceName)
	if err != nil {
		return nil, err
	}
	strictTarget := serviceName != ""
	var points []recoveryPoint
	for _, service := range services {
		servicePoints, err := s.listRecoveryPointsForService(ctx, service, strictTarget)
		if err != nil {
			return nil, err
		}
		points = append(points, servicePoints...)
	}
	sort.SliceStable(points, func(i, j int) bool {
		if points[i].Created.Equal(points[j].Created) {
			return points[i].Name < points[j].Name
		}
		return points[i].Created.After(points[j].Created)
	})
	return points, nil
}

func recoveryPointServices(dv *db.DataView, serviceName string) ([]*db.Service, error) {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName != "" {
		sv, ok := dv.Services().GetOk(serviceName)
		if !ok {
			return nil, fmt.Errorf("service %q not found", serviceName)
		}
		return []*db.Service{sv.AsStruct()}, nil
	}

	var services []*db.Service
	for _, sv := range dv.Services().All() {
		services = append(services, sv.AsStruct())
	}
	sort.SliceStable(services, func(i, j int) bool {
		return services[i].Name < services[j].Name
	})
	return services, nil
}

func (s *Server) createRecoveryPoint(ctx context.Context, serviceName string, flags cli.SnapshotsCreateFlags, w io.Writer) error {
	service, err := s.recoveryService(serviceName)
	if err != nil {
		return err
	}
	if service.ServiceType == db.ServiceTypeVM {
		return s.createVMSnapshot(ctx, service.Name, flags, w)
	}
	return s.createServiceRootRecoveryPoint(ctx, service, flags, w)
}

func (s *Server) createServiceRootRecoveryPoint(ctx context.Context, service *db.Service, flags cli.SnapshotsCreateFlags, w io.Writer) error {
	target, ok, err := recoveryTargetForService(service)
	if err != nil {
		return fmt.Errorf("service %q is not a supported recovery target: %w", service.Name, err)
	}
	if !ok || target.StorageKind != recoveryStorageServiceRoot {
		return fmt.Errorf("service %q does not have a ZFS-backed service root", service.Name)
	}
	policy, err := s.serviceSnapshotPolicy(service)
	if err != nil {
		return err
	}
	if !policy.Enabled {
		return fmt.Errorf("snapshots are disabled for %q; enable snapshots for the service or inherit enabled defaults", service.Name)
	}
	now := time.Now()
	name, err := createServiceSnapshot(ctx, s.zfsRunner, snapshotCreateRequest{
		Service:    service.Name,
		Dataset:    target.Dataset,
		Event:      snapshotEventManual,
		Generation: intPointer(service.Generation),
		Now:        now,
		Comment:    flags.Comment,
		Checkpoint: recoveryModeServiceRoot,
	})
	if err != nil {
		return err
	}
	if _, err := s.pruneServiceSnapshotsForDataset(ctx, target.Dataset, service, policy, now, name); err != nil {
		writeSnapshotWarning(w, "warning: failed to prune snapshots for %q: %v\n", service.Name, err)
	}
	writef(w, "Recovery point: %s\n", name)
	return nil
}

func (s *Server) setRecoveryPointProtected(ctx context.Context, serviceName, selector string, protected bool, w io.Writer) error {
	point, _, err := s.resolveRecoveryPoint(ctx, serviceName, selector)
	if err != nil {
		return err
	}
	if err := setSnapshotProperty(ctx, s.zfsRunner, point.Name, "com.yeetrun:protected", fmt.Sprintf("%t", protected)); err != nil {
		return err
	}
	if protected {
		writef(w, "Protected recovery point: %s\n", point.Name)
	} else {
		writef(w, "Unprotected recovery point: %s\n", point.Name)
	}
	return nil
}

func (s *Server) removeRecoveryPoint(ctx context.Context, serviceName, selector string, yes bool, rw io.ReadWriter) error {
	point, _, err := s.resolveRecoveryPoint(ctx, serviceName, selector)
	if err != nil {
		return err
	}
	if point.Protected {
		return fmt.Errorf("recovery point %s is protected; unprotect it before removing", point.ShortName)
	}
	if !yes {
		ok, err := cmdutil.Confirm(rw, rw, fmt.Sprintf("Remove recovery point %s?", point.ShortName))
		if err != nil {
			return fmt.Errorf("failed to confirm recovery point removal: %w", err)
		}
		if !ok {
			writef(rw, "Skipped recovery point: %s\n", point.Name)
			return nil
		}
	}
	if err := destroySnapshot(ctx, s.zfsRunner, point.Name); err != nil {
		return err
	}
	writef(rw, "Removed recovery point: %s\n", point.Name)
	return nil
}

func (s *Server) resolveRecoveryPoint(ctx context.Context, serviceName, selector string) (recoveryPoint, *db.Service, error) {
	service, err := s.recoveryService(serviceName)
	if err != nil {
		return recoveryPoint{}, nil, err
	}
	points, err := s.listRecoveryPointsForService(ctx, service, true)
	if err != nil {
		return recoveryPoint{}, nil, err
	}
	point, err := resolveRecoveryPointSelector(points, selector)
	if err != nil {
		return recoveryPoint{}, nil, err
	}
	return point, service, nil
}

func (s *Server) recoveryService(serviceName string) (*db.Service, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, err
	}
	serviceName = strings.TrimSpace(serviceName)
	sv, ok := dv.Services().GetOk(serviceName)
	if !ok {
		return nil, fmt.Errorf("service %q not found", serviceName)
	}
	return sv.AsStruct(), nil
}

func setSnapshotProperty(ctx context.Context, runner zfsCommandRunner, name, property, value string) error {
	if runner == nil {
		runner = runZFSCommand
	}
	setting := property + "=" + value
	_, stderr, err := runner(ctx, "set", setting, name)
	if err != nil {
		return formatZFSCommandError("zfs set "+setting+" "+name, stderr, err)
	}
	return nil
}

func (s *Server) listRecoveryPointsForService(ctx context.Context, service *db.Service, strictTarget bool) ([]recoveryPoint, error) {
	target, ok, err := recoveryTargetForService(service)
	if err != nil {
		if strictTarget {
			return nil, fmt.Errorf("service %q is not a supported recovery target: %w", service.Name, err)
		}
		return nil, nil
	}
	if !ok {
		return nil, nil
	}
	snaps, err := listServiceSnapshots(ctx, s.zfsRunner, target.Dataset)
	if err != nil {
		return nil, err
	}
	points := make([]recoveryPoint, 0, len(snaps))
	for _, snap := range snaps {
		if !isRecoverySnapshotForTarget(snap, target) {
			continue
		}
		points = append(points, recoveryPointFromSnapshot(target, snap))
	}
	return points, nil
}

func isRecoverySnapshotForTarget(snap listedSnapshot, target recoveryTarget) bool {
	return snap.CreatedBy == "catch" &&
		snap.Service == target.Service.Name &&
		strings.HasPrefix(snap.Name, target.Dataset+"@yeet-")
}

func recoveryPointFromSnapshot(target recoveryTarget, snap listedSnapshot) recoveryPoint {
	mode := zfsPropertyValue(snap.Checkpoint)
	if mode == "" {
		if target.StorageKind == recoveryStorageVMZVOL {
			mode = recoveryModeDisk
		} else {
			mode = recoveryModeServiceRoot
		}
	}
	point := recoveryPoint{
		Service:     target.Service.Name,
		ServiceType: target.ServiceType,
		StorageKind: target.StorageKind,
		Dataset:     target.Dataset,
		Name:        snap.Name,
		ShortName:   vmSnapshotShortName(snap.Name),
		Created:     snap.Created,
		CreatedBy:   snap.CreatedBy,
		Event:       snap.Event,
		Generation:  recoveryPointGeneration(target, snap),
		Comment:     snap.Comment,
		Mode:        mode,
		Protected:   snap.Protected,
		Retention:   recoveryRetentionLabel(snap.Protected),
	}
	point.Actions = recoveryPointActions(point)
	return point
}

func recoveryPointGeneration(target recoveryTarget, snap listedSnapshot) *int {
	if target.StorageKind == recoveryStorageVMZVOL {
		return nil
	}
	if snap.Generation == nil {
		return nil
	}
	gen := *snap.Generation
	return &gen
}

func recoveryRetentionLabel(protected bool) string {
	if protected {
		return "protected"
	}
	return "managed"
}

func recoveryPointActions(point recoveryPoint) []string {
	actions := []string{"inspect"}
	switch point.StorageKind {
	case recoveryStorageServiceRoot, recoveryStorageVMZVOL:
		actions = append(actions, "clone", "restore")
	}
	if point.Protected {
		return append(actions, "unprotect")
	}
	return append(actions, "protect", "rm")
}

func resolveRecoveryPointSelector(points []recoveryPoint, selector string) (recoveryPoint, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return recoveryPoint{}, fmt.Errorf("snapshot selector is required")
	}
	var matches []recoveryPoint
	for _, point := range points {
		switch {
		case point.Name == selector:
			return point, nil
		case point.ShortName == selector:
			return point, nil
		case strings.HasPrefix(point.ShortName, selector):
			matches = append(matches, point)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, point := range matches {
			names = append(names, point.ShortName)
		}
		return recoveryPoint{}, fmt.Errorf("ambiguous snapshot %q; matches: %s", selector, strings.Join(names, ", "))
	}
	return recoveryPoint{}, fmt.Errorf("snapshot %q not found", selector)
}
