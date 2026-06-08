// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import "strings"

const (
	vmImagePayloadPrefix = "vm://"
	vmUbuntu2604Payload  = "vm://ubuntu/26.04"
	vmNixOS2605Payload   = "vm://nixos/26.05"
)

const (
	defaultVMImageVersion       = "ubuntu-26.04-amd64-v13"
	defaultVMImageManifestURL   = "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json"
	nixos2605VMImageManifestURL = "https://github.com/yeetrun/yeet-vm-images/releases/download/nixos-26.05-amd64-latest/manifest.json"
)

type officialVMImage struct {
	Payload       string
	DisplayName   string
	ManifestURL   string
	VersionPrefix string
	DefaultUser   string
}

var officialVMImages = []officialVMImage{
	{
		Payload:       vmUbuntu2604Payload,
		DisplayName:   "Ubuntu 26.04",
		ManifestURL:   defaultVMImageManifestURL,
		VersionPrefix: "ubuntu-26.04-amd64-",
		DefaultUser:   "ubuntu",
	},
	{
		Payload:       vmNixOS2605Payload,
		DisplayName:   "NixOS 26.05",
		ManifestURL:   nixos2605VMImageManifestURL,
		VersionPrefix: "nixos-26.05-amd64-",
		DefaultUser:   "nixos",
	},
}

func officialVMImageByPayload(payload string) (officialVMImage, bool) {
	payload = strings.TrimSpace(payload)
	for _, image := range officialVMImages {
		if image.Payload == payload {
			return image, true
		}
	}
	return officialVMImage{}, false
}

func officialVMImageByVersion(version string) (officialVMImage, bool) {
	version = strings.TrimSpace(version)
	for _, image := range officialVMImages {
		if image.matchesVersion(version) {
			return image, true
		}
	}
	return officialVMImage{}, false
}

func (i officialVMImage) matchesVersion(version string) bool {
	version = strings.TrimSpace(version)
	return strings.HasPrefix(version, i.VersionPrefix) && isNumericVersionSuffix(strings.TrimPrefix(version, i.VersionPrefix))
}

func reservedVMImageLocalPrefix(name string) bool {
	name = strings.TrimSpace(name)
	for _, image := range officialVMImages {
		officialName := strings.TrimPrefix(image.Payload, vmImagePayloadPrefix)
		prefix := strings.SplitN(officialName, "/", 2)[0] + "/"
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func officialVMImagePayloadsForError() string {
	out := make([]string, 0, len(officialVMImages))
	for _, image := range officialVMImages {
		out = append(out, image.Payload)
	}
	return strings.Join(out, ", ")
}
