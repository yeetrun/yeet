// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	vmLANBridgePrepareStateDir = "host-network/vm-lan-bridge"
	vmLANBridgeStatusFile      = "status.json"
	vmLANBridgeNetplanOverlay  = "/etc/netplan/99-yeet-vm-lan-bridge.yaml"
	vmLANBridgeNetplanMarker   = "# Managed by yeet VM LAN bridge preparation; do not edit."
	vmLANBridgeRollbackDelay   = 2 * time.Minute
)

type vmLANBridgePreparePhase string

const (
	vmLANBridgePhasePlanned    vmLANBridgePreparePhase = "planned"
	vmLANBridgePhaseRunning    vmLANBridgePreparePhase = "running"
	vmLANBridgePhaseApplying   vmLANBridgePreparePhase = "applying"
	vmLANBridgePhaseValidating vmLANBridgePreparePhase = "validating"
	vmLANBridgePhaseReady      vmLANBridgePreparePhase = "ready"
	vmLANBridgePhaseSkipped    vmLANBridgePreparePhase = "skipped"
	vmLANBridgePhaseRolledBack vmLANBridgePreparePhase = "rolled-back"
	vmLANBridgePhaseFailed     vmLANBridgePreparePhase = "failed"
)

type vmLANBridgePrepareStatus struct {
	ID          string                  `json:"id"`
	Phase       vmLANBridgePreparePhase `json:"phase"`
	Bridge      string                  `json:"bridge,omitempty"`
	Parent      string                  `json:"parent,omitempty"`
	SourcePath  string                  `json:"source_path,omitempty"`
	BackupPath  string                  `json:"backup_path,omitempty"`
	OverlayPath string                  `json:"overlay_path,omitempty"`
	Message     string                  `json:"message,omitempty"`
	Error       string                  `json:"error,omitempty"`
	StartedAt   time.Time               `json:"started_at"`
	UpdatedAt   time.Time               `json:"updated_at"`
}

type vmLANBridgePrepareOptions struct {
	Yes bool
}

type VMLANRenderer struct {
	Name      string
	Supported bool
	Reason    string
}

type VMLANBridgePlan struct {
	Ready        bool
	NeedsPrepare bool
	Bridge       string
	Parent       string
	Renderer     VMLANRenderer
	Reason       string
}

type VMLANBridgePrepareStatus struct {
	ID          string    `json:"id"`
	Phase       string    `json:"phase"`
	Bridge      string    `json:"bridge,omitempty"`
	Parent      string    `json:"parent,omitempty"`
	SourcePath  string    `json:"source_path,omitempty"`
	BackupPath  string    `json:"backup_path,omitempty"`
	OverlayPath string    `json:"overlay_path,omitempty"`
	Message     string    `json:"message,omitempty"`
	Error       string    `json:"error,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type vmLANBridgePrepareRunner interface {
	Plan() (vmLANBridgePlan, error)
	ReadNetplan(parent string) ([]byte, error)
	WriteNetplanBackup(path string, content []byte) error
	WriteNetplanOverlay(path string, content []byte) error
	Generate() error
	Apply() error
	Validate(bridge, parent string) error
	ScheduleRollback(id string, after time.Duration) error
	CancelRollback(id string) error
	Rollback(id string) error
}

var vmLANBridgePrepareRunnerFactory = newSystemVMLANBridgePrepareRunner

func prepareVMLANBridge(root string, runner vmLANBridgePrepareRunner, opts vmLANBridgePrepareOptions) (vmLANBridgePrepareStatus, error) {
	op := newVMLANBridgePrepareOperation(root, runner, opts, time.Now().UTC())
	return op.run()
}

func PlanVMLANBridge(root string) (VMLANBridgePlan, error) {
	plan, err := planSystemVMLANBridge()
	return exportVMLANBridgePlan(plan), err
}

func PrepareVMLANBridge(root string, yes bool) (VMLANBridgePrepareStatus, error) {
	if !yes {
		return VMLANBridgePrepareStatus{}, fmt.Errorf("VM LAN bridge preparation --yes is required because this can momentarily change host networking")
	}
	status, err := prepareVMLANBridge(root, vmLANBridgePrepareRunnerFactory(root), vmLANBridgePrepareOptions{Yes: true})
	return exportVMLANBridgePrepareStatus(status), err
}

func ReadVMLANBridgePrepareStatus(root string) (VMLANBridgePrepareStatus, bool, error) {
	statusPath := filepath.Join(root, vmLANBridgePrepareStateDir, vmLANBridgeStatusFile)
	raw, err := os.ReadFile(statusPath)
	if err != nil {
		if os.IsNotExist(err) {
			return VMLANBridgePrepareStatus{}, false, nil
		}
		return VMLANBridgePrepareStatus{}, false, fmt.Errorf("read VM LAN bridge prepare status: %w", err)
	}
	var status VMLANBridgePrepareStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return VMLANBridgePrepareStatus{}, false, fmt.Errorf("parse VM LAN bridge prepare status: %w", err)
	}
	return status, true, nil
}

type vmLANBridgePrepareOperation struct {
	runner     vmLANBridgePrepareRunner
	opts       vmLANBridgePrepareOptions
	status     vmLANBridgePrepareStatus
	statusPath string
}

func newVMLANBridgePrepareOperation(root string, runner vmLANBridgePrepareRunner, opts vmLANBridgePrepareOptions, now time.Time) *vmLANBridgePrepareOperation {
	status := vmLANBridgePrepareStatus{
		ID:        vmLANBridgePrepareID(now),
		Phase:     vmLANBridgePhasePlanned,
		StartedAt: now,
		UpdatedAt: now,
	}
	return &vmLANBridgePrepareOperation{
		runner:     runner,
		opts:       opts,
		status:     status,
		statusPath: filepath.Join(root, vmLANBridgePrepareStateDir, vmLANBridgeStatusFile),
	}
}

func (op *vmLANBridgePrepareOperation) run() (vmLANBridgePrepareStatus, error) {
	if err := writeVMLANBridgePrepareStatus(op.statusPath, op.status); err != nil {
		return op.status, err
	}
	plan, err := op.runner.Plan()
	if err != nil {
		err = op.fail("plan VM LAN bridge", err)
		return op.status, err
	}
	op.status.Bridge = strings.TrimSpace(plan.Bridge)
	op.status.Parent = strings.TrimSpace(plan.Parent)
	return op.runPlan(plan)
}

func (op *vmLANBridgePrepareOperation) runPlan(plan vmLANBridgePlan) (vmLANBridgePrepareStatus, error) {
	if plan.Ready {
		err := op.finish(vmLANBridgePhaseReady, "VM LAN bridge is already ready")
		return op.status, err
	}
	if !plan.NeedsPrepare {
		err := op.finish(vmLANBridgePhaseSkipped, "VM LAN bridge preparation is not needed")
		return op.status, err
	}
	if !op.opts.Yes {
		err := op.finish(vmLANBridgePhaseSkipped, "VM LAN bridge preparation requires confirmation")
		return op.status, err
	}
	return op.runPrepare()
}

func (op *vmLANBridgePrepareOperation) runPrepare() (vmLANBridgePrepareStatus, error) {
	if err := op.setPhase(vmLANBridgePhaseRunning, "preparing VM LAN bridge netplan", nil); err != nil {
		return op.status, err
	}
	currentNetplan, err := op.runner.ReadNetplan(op.status.Parent)
	if err != nil {
		err = op.fail("read netplan", err)
		return op.status, err
	}
	if err := op.recordSourcePath(); err != nil {
		return op.status, err
	}
	if err := op.writeNetplan(currentNetplan); err != nil {
		return op.status, err
	}
	return op.applyAndValidate()
}

func (op *vmLANBridgePrepareOperation) writeNetplan(currentNetplan []byte) error {
	if err := op.recordBackupPath(); err != nil {
		return err
	}
	if err := op.runner.WriteNetplanBackup(op.status.BackupPath, currentNetplan); err != nil {
		return op.fail("write netplan backup", err)
	}
	overlay, err := renderVMLANBridgeNetplan(op.status.Bridge, op.status.Parent, currentNetplan)
	if err != nil {
		return op.fail("render VM LAN bridge netplan", err)
	}
	if err := op.recordOverlayPath(); err != nil {
		return err
	}
	if err := op.runner.WriteNetplanOverlay(op.status.OverlayPath, overlay); err != nil {
		return op.fail("write VM LAN bridge netplan overlay", err)
	}
	if err := op.runner.ScheduleRollback(op.status.ID, vmLANBridgeRollbackDelay); err != nil {
		return op.rollback("schedule VM LAN bridge rollback", err)
	}
	if err := op.runner.Generate(); err != nil {
		return op.rollback("generate netplan", err)
	}
	return nil
}

func (op *vmLANBridgePrepareOperation) recordBackupPath() error {
	op.status.BackupPath = filepath.Join(filepath.Dir(op.statusPath), fmt.Sprintf("netplan-%s.yaml", op.status.ID))
	return op.writeStatus()
}

func (op *vmLANBridgePrepareOperation) recordSourcePath() error {
	source, ok := op.runner.(interface{ NetplanSourcePath() string })
	if !ok {
		return nil
	}
	op.status.SourcePath = source.NetplanSourcePath()
	return op.writeStatus()
}

func (op *vmLANBridgePrepareOperation) recordOverlayPath() error {
	op.status.OverlayPath = vmLANBridgeNetplanOverlay
	return op.writeStatus()
}

func (op *vmLANBridgePrepareOperation) applyAndValidate() (vmLANBridgePrepareStatus, error) {
	if err := op.setPhase(vmLANBridgePhaseApplying, "applying VM LAN bridge netplan", nil); err != nil {
		return op.status, err
	}
	if err := op.runner.Apply(); err != nil {
		err = op.rollback("apply netplan", err)
		return op.status, err
	}
	if err := op.setPhase(vmLANBridgePhaseValidating, "validating VM LAN bridge", nil); err != nil {
		return op.status, err
	}
	if err := op.runner.Validate(op.status.Bridge, op.status.Parent); err != nil {
		err = op.rollback("validate VM LAN bridge", err)
		return op.status, err
	}
	if err := op.runner.CancelRollback(op.status.ID); err != nil {
		err = op.fail("cancel VM LAN bridge rollback", err)
		return op.status, err
	}
	err := op.finish(vmLANBridgePhaseReady, "VM LAN bridge is ready")
	return op.status, err
}

func (op *vmLANBridgePrepareOperation) setPhase(phase vmLANBridgePreparePhase, message string, phaseErr error) error {
	op.status.Phase = phase
	op.status.Message = message
	op.status.Error = ""
	if phaseErr != nil {
		op.status.Error = phaseErr.Error()
	}
	op.status.UpdatedAt = time.Now().UTC()
	return op.writeStatus()
}

func (op *vmLANBridgePrepareOperation) writeStatus() error {
	return writeVMLANBridgePrepareStatus(op.statusPath, op.status)
}

func (op *vmLANBridgePrepareOperation) finish(phase vmLANBridgePreparePhase, message string) error {
	return op.setPhase(phase, message, nil)
}

func (op *vmLANBridgePrepareOperation) fail(action string, err error) error {
	wrapped := fmt.Errorf("%s: %w", action, err)
	if statusErr := op.setPhase(vmLANBridgePhaseFailed, action+" failed", wrapped); statusErr != nil {
		return errors.Join(wrapped, statusErr)
	}
	return wrapped
}

func (op *vmLANBridgePrepareOperation) rollback(action string, err error) error {
	wrapped := fmt.Errorf("%s: %w", action, err)
	rollbackErr := op.runner.Rollback(op.status.ID)
	if rollbackErr != nil {
		combined := errors.Join(wrapped, fmt.Errorf("rollback VM LAN bridge: %w", rollbackErr))
		if statusErr := op.setPhase(vmLANBridgePhaseFailed, "VM LAN bridge rollback failed", combined); statusErr != nil {
			return errors.Join(combined, statusErr)
		}
		return combined
	}
	if statusErr := op.setPhase(vmLANBridgePhaseRolledBack, "VM LAN bridge preparation rolled back", wrapped); statusErr != nil {
		return errors.Join(wrapped, statusErr)
	}
	return wrapped
}

func vmLANBridgePrepareID(now time.Time) string {
	return "vm-lan-bridge-" + now.Format("20060102T150405.000000000Z")
}

func writeVMLANBridgePrepareStatus(path string, status vmLANBridgePrepareStatus) error {
	raw, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("encode VM LAN bridge prepare status: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create VM LAN bridge prepare status dir: %w", err)
	}
	if err := writeTextFileAtomically(path, raw, 0644); err != nil {
		return fmt.Errorf("write VM LAN bridge prepare status: %w", err)
	}
	return nil
}

func exportVMLANBridgePlan(plan vmLANBridgePlan) VMLANBridgePlan {
	return VMLANBridgePlan{
		Ready:        plan.Ready,
		NeedsPrepare: plan.NeedsPrepare,
		Bridge:       plan.Bridge,
		Parent:       plan.Parent,
		Renderer: VMLANRenderer{
			Name:      plan.Renderer.Name,
			Supported: plan.Renderer.Supported,
			Reason:    plan.Renderer.Reason,
		},
		Reason: plan.Reason,
	}
}

func exportVMLANBridgePrepareStatus(status vmLANBridgePrepareStatus) VMLANBridgePrepareStatus {
	return VMLANBridgePrepareStatus{
		ID:          status.ID,
		Phase:       string(status.Phase),
		Bridge:      status.Bridge,
		Parent:      status.Parent,
		SourcePath:  status.SourcePath,
		BackupPath:  status.BackupPath,
		OverlayPath: status.OverlayPath,
		Message:     status.Message,
		Error:       status.Error,
		StartedAt:   status.StartedAt,
		UpdatedAt:   status.UpdatedAt,
	}
}
