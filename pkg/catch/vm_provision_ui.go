// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"strings"
	"time"
)

const (
	vmRunStepResolve   = "Resolve VM plan"
	vmRunStepDisk      = "Prepare disk"
	vmRunStepMetadata  = "Inject guest metadata"
	vmRunStepConfig    = "Write Firecracker config"
	vmRunStepNetwork   = "Configure network"
	vmRunStepStage     = "Stage VM service"
	vmRunStepInstall   = "Install VM service"
	vmRunStepCommit    = "Commit VM service"
	vmRunStepStart     = "Start VM"
	vmRunStepReadiness = "Wait for guest readiness"
)

type vmProvisionUI struct {
	ui           *runUI
	startedAt    time.Time
	serviceLabel string
}

type vmProvisionSuccess struct {
	ServiceLabel string
	Plan         vmProvisionPlan
	Payload      string
	Elapsed      time.Duration
	Ready        vmGuestReadyReport
	Started      bool
}

func newVMProvisionUI(e *ttyExecer) *vmProvisionUI {
	if e == nil || e.rw == nil {
		return &vmProvisionUI{startedAt: time.Now()}
	}
	ui := e.newProgressUI("run")
	return &vmProvisionUI{
		ui:           ui,
		startedAt:    time.Now(),
		serviceLabel: ui.service,
	}
}

func (v *vmProvisionUI) Start() {
	if v == nil || v.ui == nil {
		return
	}
	v.ui.Start()
}

func (v *vmProvisionUI) Stop() {
	if v == nil || v.ui == nil {
		return
	}
	v.ui.Stop()
}

func (v *vmProvisionUI) StartStep(name string) {
	if v == nil || v.ui == nil {
		return
	}
	v.ui.StartTimedStep(name)
}

func (v *vmProvisionUI) DoneStep(detail string) {
	if v == nil || v.ui == nil {
		return
	}
	v.ui.DoneStep(detail)
}

func (v *vmProvisionUI) FailStep(detail string) {
	if v == nil || v.ui == nil {
		return
	}
	v.ui.FailStep(detail)
}

func (v *vmProvisionUI) UpdateDetail(detail string) {
	if v == nil || v.ui == nil {
		return
	}
	v.ui.UpdateDetail(detail)
}

func (v *vmProvisionUI) ImageProgress() ProgressUI {
	if v == nil || v.ui == nil {
		return nil
	}
	return &vmProvisionImageProgressUI{parent: v}
}

func (v *vmProvisionUI) PrintSuccess(plan vmProvisionPlan, payload string, ready vmGuestReadyReport, started bool) {
	if v == nil || v.ui == nil {
		return
	}
	v.ui.PrintBlock(vmProvisionSuccessBlockLines(vmProvisionSuccess{
		ServiceLabel: v.serviceLabel,
		Plan:         plan,
		Payload:      payload,
		Elapsed:      time.Since(v.startedAt),
		Ready:        ready,
		Started:      started,
	}))
}

func (v *vmProvisionUI) PrintReadinessFailure(service string) {
	if v == nil || v.ui == nil {
		return
	}
	v.ui.PrintBlock(vmProvisionReadinessFailureBlockLines(service))
}

type vmProvisionImageProgressUI struct {
	parent      *vmProvisionUI
	restoreStep string
}

func (u *vmProvisionImageProgressUI) Start() {}
func (u *vmProvisionImageProgressUI) Stop()  {}
func (u *vmProvisionImageProgressUI) Suspend() {
	if u.parent != nil && u.parent.ui != nil {
		u.parent.ui.Suspend()
	}
}
func (u *vmProvisionImageProgressUI) StartStep(name string) {
	if u.parent != nil && u.parent.ui != nil {
		u.restoreStep = u.parent.currentStep()
		u.parent.StartStep(name)
	}
}
func (u *vmProvisionImageProgressUI) UpdateDetail(detail string) {
	if u.parent != nil && u.parent.ui != nil {
		u.parent.ui.UpdateDetail(detail)
	}
}
func (u *vmProvisionImageProgressUI) DoneStep(detail string) {
	if u.parent != nil {
		u.parent.DoneStep(detail)
		u.parent.restoreStep(u.restoreStep)
	}
}
func (u *vmProvisionImageProgressUI) FailStep(detail string) {
	if u.parent != nil {
		u.parent.FailStep(detail)
		u.parent.restoreStep(u.restoreStep)
	}
}
func (u *vmProvisionImageProgressUI) Printer(format string, args ...any) {}

func (u *vmProvisionImageProgressUI) PauseForPrompt() func() {
	if u.parent == nil || u.parent.ui == nil {
		return func() {}
	}
	u.parent.ui.PauseForPrompt()
	return u.parent.ui.FinishPrompt
}

func (v *vmProvisionUI) currentStep() string {
	if v == nil || v.ui == nil {
		return ""
	}
	v.ui.mu.Lock()
	defer v.ui.mu.Unlock()
	return v.ui.current
}

func (v *vmProvisionUI) restoreStep(name string) {
	if name == "" || v == nil || v.ui == nil {
		return
	}
	v.StartStep(name)
}

func vmProvisionSuccessBlockLines(s vmProvisionSuccess) []string {
	service := s.Plan.Service
	if service == "" {
		service = strings.TrimSpace(strings.Split(s.ServiceLabel, "@")[0])
	}
	status := "ready"
	if !s.Started {
		status = "created"
	}
	lines := []string{
		fmt.Sprintf("✔ VM %s in %s", status, formatRunUIElapsed(s.Elapsed)),
		"",
		s.ServiceLabel,
		"Image    " + vmProvisionImageDisplayName(s.Plan, s.Payload),
		"Shape    " + vmProvisionShapeLine(s.Plan.Shape),
		"Network  " + formatVMProvisionNetwork(s.Plan.Network),
	}
	if s.Ready.IP.IsValid() {
		lines = append(lines, "IP       "+s.Ready.IP.String())
	}
	if !s.Started {
		return append(lines,
			"",
			"Start    yeet start "+service,
			"Console  yeet vm console "+service,
		)
	}
	return append(lines,
		"",
		"SSH      yeet ssh "+service,
		"Console  yeet vm console "+service,
	)
}

func vmProvisionReadinessFailureBlockLines(service string) []string {
	return []string{
		"",
		"VM service was created, but readiness did not complete.",
		"",
		"Console  yeet vm console " + service,
		"Logs     yeet logs " + service,
	}
}

func vmProvisionPlanDetail(plan vmProvisionPlan) string {
	return vmProvisionImageDisplayName(plan, "")
}

func vmProvisionImageDisplayName(plan vmProvisionPlan, payload string) string {
	name := strings.TrimSpace(plan.Image.Manifest.Name)
	if name != "" {
		return name
	}
	if payload != "" {
		return payload
	}
	if plan.Image.Manifest.Version != "" {
		return plan.Image.Manifest.Version
	}
	return "VM image"
}

func vmProvisionShapeLine(shape vmShape) string {
	return fmt.Sprintf("%d vCPU, %s memory, %s disk",
		shape.CPUs,
		formatVMProvisionBytes(shape.MemoryBytes),
		formatVMProvisionBytes(shape.DiskBytes),
	)
}
