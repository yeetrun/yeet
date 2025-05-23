// Copyright 2018 Google LLC All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package empty

import (
	"testing"

	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/validate"
)

func TestImage(t *testing.T) {
	if err := validate.Image(Image); err != nil {
		t.Fatalf("validate.Image(empty.Image) = %v", err)
	}
}

func TestManifestAndConfig(t *testing.T) {
	manifest, err := Image.Manifest()
	if err != nil {
		t.Fatalf("Error loading manifest: %v", err)
	}
	if got, want := len(manifest.Layers), 0; got != want {
		t.Fatalf("num layers; got %v, want %v", got, want)
	}

	config, err := Image.ConfigFile()
	if err != nil {
		t.Fatalf("Error loading config file: %v", err)
	}
	if got, want := len(config.RootFS.DiffIDs), 0; got != want {
		t.Fatalf("num diff ids; got %v, want %v", got, want)
	}
	if got, want := config.RootFS.Type, "layers"; got != want {
		t.Fatalf("rootfs type; got %v, want %v", got, want)
	}
}
