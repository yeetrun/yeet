// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"log"

	"github.com/shayne/yeet/pkg/db"
	"github.com/shayne/yeet/pkg/svc"
)

type ServiceDataType string
type ComponentStatus string

const (
	ServiceDataTypeService ServiceDataType = "service"
	ServiceDataTypeCron    ServiceDataType = "cron"
	ServiceDataTypeDocker  ServiceDataType = "docker"
	ServiceDataTypeUnknown ServiceDataType = "unknown"

	ComponentStatusStarting ComponentStatus = "starting"
	ComponentStatusRunning  ComponentStatus = "running"
	ComponentStatusStopping ComponentStatus = "stopping"
	ComponentStatusStopped  ComponentStatus = "stopped"
	ComponentStatusUnknown  ComponentStatus = "unknown"
)

type ServiceStatusData struct {
	ServiceName     string                `json:"serviceName"`
	ServiceType     ServiceDataType       `json:"serviceType"`
	ComponentStatus []ComponentStatusData `json:"components"`
}

type ComponentStatusData struct {
	Name   string          `json:"name"`
	Status ComponentStatus `json:"status"`
}

func ComponentStatusFromServiceStatus(st svc.Status) ComponentStatus {
	switch st {
	case svc.StatusRunning:
		return ComponentStatusRunning
	case svc.StatusStopped:
		return ComponentStatusStopped
	case svc.StatusUnknown:
		return ComponentStatusUnknown
	default:
		log.Printf("unknown service status: %v", st)
		return ComponentStatusUnknown
	}
}

func ServiceDataTypeFromServiceType(st db.ServiceType) ServiceDataType {
	switch st {
	case db.ServiceTypeSystemd:
		return ServiceDataTypeService
	case db.ServiceTypeDockerCompose:
		return ServiceDataTypeDocker
	default:
		return ServiceDataTypeUnknown
	}
}

func ServiceDataTypeForService(sv db.ServiceView) ServiceDataType {
	if !sv.Valid() {
		return ServiceDataTypeUnknown
	}
	if sv.ServiceType() == db.ServiceTypeSystemd {
		if hasTimerArtifact(sv) {
			return ServiceDataTypeCron
		}
		return ServiceDataTypeService
	}
	return ServiceDataTypeFromServiceType(sv.ServiceType())
}

func hasTimerArtifact(sv db.ServiceView) bool {
	if !sv.Valid() {
		return false
	}
	_, ok := sv.Artifacts().GetOk(db.ArtifactSystemdTimerFile)
	return ok
}

func ServiceDataTypeFromUnitType(unitType string) ServiceDataType {
	switch unitType {
	case "service":
		return ServiceDataTypeService
	case "cron":
		return ServiceDataTypeCron
	case "docker":
		return ServiceDataTypeDocker
	default:
		log.Printf("unknown unit type: %q", unitType)
		return ServiceDataTypeUnknown
	}
}
