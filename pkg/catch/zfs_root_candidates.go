// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/yeetrun/yeet/pkg/catchrpc"
)

const maxZFSRootCandidates = 12

type zfsRootCandidateRow struct {
	Name       string
	Type       string
	Mountpoint string
	Available  int64
	Used       int64
	Refer      int64
	Origin     string
	Canmount   string
	Readonly   string
}

type zfsRootCandidateNode struct {
	Row      zfsRootCandidateRow
	Parent   *zfsRootCandidateNode
	Children []*zfsRootCandidateNode
}

type zfsRootCandidateTree struct {
	Nodes   map[string]*zfsRootCandidateNode
	Ordered []*zfsRootCandidateNode
}

func (s *Server) zfsServiceRootCandidates(ctx context.Context, req catchrpc.ZFSServiceRootCandidatesRequest) (catchrpc.ZFSServiceRootCandidatesResponse, error) {
	if s == nil {
		return zfsServiceRootCandidates(ctx, nil, req)
	}
	return zfsServiceRootCandidates(ctx, s.zfsRunner, req)
}

func zfsServiceRootCandidates(ctx context.Context, runner zfsCommandRunner, req catchrpc.ZFSServiceRootCandidatesRequest) (catchrpc.ZFSServiceRootCandidatesResponse, error) {
	if runner == nil {
		runner = runZFSCommand
	}

	stdout, stderr, err := runner(ctx, "list", "-H", "-p", "-o", "name,type,mountpoint,available,used,refer,origin,canmount,readonly", "-t", "filesystem,volume")
	if err != nil {
		if isZFSMissingCommand(stderr, err) {
			return catchrpc.ZFSServiceRootCandidatesResponse{State: catchrpc.ZFSRootDiscoveryZFSMissing}, nil
		}
		return catchrpc.ZFSServiceRootCandidatesResponse{
			State:    catchrpc.ZFSRootDiscoveryError,
			Warnings: []string{formatZFSCommandError("zfs list filesystems", stderr, err).Error()},
		}, nil
	}

	rows, err := parseZFSRootCandidateRows(stdout)
	if err != nil {
		return catchrpc.ZFSServiceRootCandidatesResponse{
			State:    catchrpc.ZFSRootDiscoveryError,
			Warnings: []string{err.Error()},
		}, nil
	}

	candidates := zfsRankedRootCandidates(buildZFSRootCandidateTree(rows), req)
	if len(candidates) == 0 {
		return catchrpc.ZFSServiceRootCandidatesResponse{State: catchrpc.ZFSRootDiscoveryNoFilesystems}, nil
	}
	if len(candidates) > maxZFSRootCandidates {
		candidates = candidates[:maxZFSRootCandidates]
	}
	return catchrpc.ZFSServiceRootCandidatesResponse{
		State:      catchrpc.ZFSRootDiscoveryAvailable,
		Candidates: candidates,
	}, nil
}

func parseZFSRootCandidateRows(raw string) ([]zfsRootCandidateRow, error) {
	var rows []zfsRootCandidateRow
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 9 {
			return nil, fmt.Errorf("invalid zfs list row %q", line)
		}
		available, err := parseZFSRootCandidateBytes(fields[3], "available", line)
		if err != nil {
			return nil, err
		}
		used, err := parseZFSRootCandidateBytes(fields[4], "used", line)
		if err != nil {
			return nil, err
		}
		refer, err := parseZFSRootCandidateBytes(fields[5], "refer", line)
		if err != nil {
			return nil, err
		}
		rows = append(rows, zfsRootCandidateRow{
			Name:       strings.TrimSpace(fields[0]),
			Type:       strings.TrimSpace(fields[1]),
			Mountpoint: strings.TrimSpace(fields[2]),
			Available:  available,
			Used:       used,
			Refer:      refer,
			Origin:     strings.TrimSpace(fields[6]),
			Canmount:   strings.TrimSpace(fields[7]),
			Readonly:   strings.TrimSpace(fields[8]),
		})
	}
	return rows, nil
}

func parseZFSRootCandidateBytes(raw, field, line string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "-" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid zfs list %s %q in row %q: %w", field, raw, line, err)
	}
	return value, nil
}

func buildZFSRootCandidateTree(rows []zfsRootCandidateRow) zfsRootCandidateTree {
	tree := zfsRootCandidateTree{
		Nodes:   make(map[string]*zfsRootCandidateNode, len(rows)),
		Ordered: make([]*zfsRootCandidateNode, 0, len(rows)),
	}
	for _, row := range rows {
		node := &zfsRootCandidateNode{Row: row}
		tree.Nodes[row.Name] = node
		tree.Ordered = append(tree.Ordered, node)
	}
	for _, node := range tree.Ordered {
		parentName := zfsDatasetParent(node.Row.Name)
		if parentName == "" {
			continue
		}
		parent := tree.Nodes[parentName]
		if parent == nil {
			continue
		}
		node.Parent = parent
		parent.Children = append(parent.Children, node)
	}
	return tree
}

func zfsRankedRootCandidates(tree zfsRootCandidateTree, req catchrpc.ZFSServiceRootCandidatesRequest) []catchrpc.ZFSServiceRootCandidate {
	candidates := make([]catchrpc.ZFSServiceRootCandidate, 0, len(tree.Ordered))
	workload := strings.ToLower(strings.TrimSpace(req.Workload))
	for _, node := range tree.Ordered {
		if !usableZFSRootCandidate(node.Row) {
			continue
		}
		childCount := zfsFilesystemChildCount(node)
		vmChildCount := zfsVMChildCount(node)
		serviceChildCount := zfsServiceChildCount(node)
		rank := zfsRootCandidateRank(node.Row.Name, workload, childCount, vmChildCount, serviceChildCount)
		candidates = append(candidates, catchrpc.ZFSServiceRootCandidate{
			Dataset:           node.Row.Name,
			Mountpoint:        node.Row.Mountpoint,
			FreeBytes:         node.Row.Available,
			ChildCount:        childCount,
			VMChildCount:      vmChildCount,
			ServiceChildCount: serviceChildCount,
			SuggestedDataset:  suggestedZFSDataset(node.Row.Name, req.Service),
			Label:             node.Row.Name,
			Rank:              rank,
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Rank != candidates[j].Rank {
			return candidates[i].Rank > candidates[j].Rank
		}
		if candidates[i].FreeBytes != candidates[j].FreeBytes {
			return candidates[i].FreeBytes > candidates[j].FreeBytes
		}
		if candidates[i].ChildCount != candidates[j].ChildCount {
			return candidates[i].ChildCount > candidates[j].ChildCount
		}
		return candidates[i].Dataset < candidates[j].Dataset
	})
	return candidates
}

func zfsRootCandidateRank(name, workload string, childCount, vmChildCount, serviceChildCount int) int {
	if workload == "vm" {
		return zfsVMRootCandidateRank(name, childCount, vmChildCount)
	}
	return zfsComposeRootCandidateRank(name, childCount, vmChildCount, serviceChildCount)
}

func zfsVMRootCandidateRank(name string, childCount, vmChildCount int) int {
	rank := vmChildCount*100 + childCount
	if zfsDatasetNameEndsWith(name, "vms") {
		rank += 80
	}
	if zfsDatasetContainsPath(name, "yeet/vms") {
		rank += 60
	}
	if zfsDatasetNameEndsWith(name, "yeet") {
		rank += 10
	}
	return rank
}

func zfsComposeRootCandidateRank(name string, childCount, vmChildCount, serviceChildCount int) int {
	rank := serviceChildCount*100 + childCount
	switch {
	case zfsDatasetNameEndsWith(name, "yeet"):
		rank += 80
	case zfsDatasetNameEndsWith(name, "apps"):
		rank += 70
	case zfsDatasetNameEndsWith(name, "services"):
		rank += 70
	}
	if zfsDatasetNameEndsWith(name, "vms") {
		rank -= 150
	}
	if zfsDatasetContainsPath(name, "yeet/vms") {
		rank -= 100
	}
	if vmChildCount > 0 {
		rank -= vmChildCount * 20
	}
	return rank
}

func suggestedZFSDataset(root, service string) string {
	root = strings.TrimRight(strings.TrimSpace(root), "/")
	service = strings.TrimSpace(service)
	if service == "" {
		return root + "/"
	}
	return root + "/" + service
}

func usableZFSRootCandidate(row zfsRootCandidateRow) bool {
	if row.Type != "filesystem" {
		return false
	}
	if !normalZFSMountpoint(row.Mountpoint) {
		return false
	}
	if strings.EqualFold(row.Canmount, "off") {
		return false
	}
	if strings.EqualFold(row.Readonly, "on") {
		return false
	}
	return !isInternalVMImagesDataset(row.Name)
}

func normalZFSMountpoint(mountpoint string) bool {
	mountpoint = strings.TrimSpace(mountpoint)
	if mountpoint == "" || mountpoint == "-" || strings.EqualFold(mountpoint, "legacy") || strings.EqualFold(mountpoint, "none") {
		return false
	}
	return filepath.IsAbs(mountpoint)
}

func zfsFilesystemChildCount(node *zfsRootCandidateNode) int {
	count := 0
	for _, child := range node.Children {
		if child.Row.Type == "filesystem" {
			count++
		}
	}
	return count
}

func zfsVMChildCount(node *zfsRootCandidateNode) int {
	count := 0
	for _, child := range node.Children {
		if child.Row.Type == "filesystem" && zfsNodeHasDirectRootVolume(child) {
			count++
		}
	}
	return count
}

func zfsServiceChildCount(node *zfsRootCandidateNode) int {
	count := 0
	for _, child := range node.Children {
		if !usableZFSRootCandidate(child.Row) {
			continue
		}
		if zfsDatasetNameEndsWith(child.Row.Name, "vms") {
			continue
		}
		if zfsNodeHasDirectRootVolume(child) {
			continue
		}
		count++
	}
	return count
}

func zfsNodeHasDirectRootVolume(node *zfsRootCandidateNode) bool {
	rootVolume := node.Row.Name + "/root"
	for _, child := range node.Children {
		if child.Row.Type == "volume" && child.Row.Name == rootVolume {
			return true
		}
	}
	return false
}

func zfsDatasetParent(name string) string {
	name = strings.TrimSpace(name)
	idx := strings.LastIndex(name, "/")
	if idx <= 0 {
		return ""
	}
	return name[:idx]
}

func zfsDatasetNameEndsWith(name, segment string) bool {
	segment = strings.Trim(segment, "/")
	return name == segment || strings.HasSuffix(name, "/"+segment)
}

func zfsDatasetContainsPath(name, path string) bool {
	path = strings.Trim(path, "/")
	return name == path ||
		strings.HasPrefix(name, path+"/") ||
		strings.HasSuffix(name, "/"+path) ||
		strings.Contains(name, "/"+path+"/")
}

func isInternalVMImagesDataset(name string) bool {
	return name == "vm-images" ||
		strings.HasSuffix(name, "/vm-images") ||
		strings.Contains(name, "/vm-images/")
}

func isZFSMissingCommand(stderr string, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(stderr + " " + err.Error()))
	return strings.Contains(text, "executable file not found") ||
		strings.Contains(text, "command not found") ||
		strings.Contains(text, "no such file or directory")
}
