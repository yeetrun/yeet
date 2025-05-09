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

package tarball

import (
	"io"
	"testing"

	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/name"
	v1 "github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/validate"
)

func TestManifestAndConfig(t *testing.T) {
	img, err := ImageFromPath("testdata/test_image_1.tar", nil)
	if err != nil {
		t.Fatalf("Error loading image: %v", err)
	}
	manifest, err := img.Manifest()
	if err != nil {
		t.Fatalf("Error loading manifest: %v", err)
	}
	if len(manifest.Layers) != 1 {
		t.Fatalf("layers should be 1, got %d", len(manifest.Layers))
	}

	config, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("Error loading config file: %v", err)
	}
	if len(config.History) != 1 {
		t.Fatalf("history length should be 1, got %d", len(config.History))
	}

	if err := validate.Image(img); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}

func TestNullManifest(t *testing.T) {
	img, err := ImageFromPath("testdata/null_manifest.tar", nil)
	if err == nil {
		t.Fatalf("Error expected loading null image: %v", img)
	}
}

func TestNoManifest(t *testing.T) {
	img, err := ImageFromPath("testdata/no_manifest.tar", nil)
	if err == nil {
		t.Fatalf("Error expected loading image: %v", img)
	}
}

func TestBundleSingle(t *testing.T) {
	img, err := ImageFromPath("testdata/test_bundle.tar", nil)
	if err == nil {
		t.Fatalf("Error expected loading image: %v", img)
	}
}

func TestDocker25(t *testing.T) {
	img, err := ImageFromPath("testdata/hello-world-v25.tar", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := validate.Image(img); err != nil {
		t.Fatal(err)
	}
}

func TestBundleMultiple(t *testing.T) {
	for _, imgName := range []string{
		"test_image_1",
		"test_image_2",
		"test_image_1:latest",
		"test_image_2:latest",
		"index.docker.io/library/test_image_1:latest",
	} {
		t.Run(imgName, func(t *testing.T) {
			tag, err := name.NewTag(imgName, name.WeakValidation)
			if err != nil {
				t.Fatalf("Error creating tag: %v", err)
			}
			img, err := ImageFromPath("testdata/test_bundle.tar", &tag)
			if err != nil {
				t.Fatalf("Error loading image: %v", err)
			}
			if _, err := img.Manifest(); err != nil {
				t.Fatalf("Unexpected error loading manifest: %v", err)
			}

			if err := validate.Image(img); err != nil {
				t.Errorf("Validate() = %v", err)
			}
		})
	}
}

func TestLayerLink(t *testing.T) {
	tag, err := name.NewTag("bazel/v1/tarball:test_image_3", name.WeakValidation)
	if err != nil {
		t.Fatalf("Error creating tag: %v", err)
	}
	img, err := ImageFromPath("testdata/test_link.tar", &tag)
	if err != nil {
		t.Fatalf("Error loading image: %v", img)
	}
	hash := v1.Hash{
		Algorithm: "sha256",
		Hex:       "8897395fd26dc44ad0e2a834335b33198cb41ac4d98dfddf58eced3853fa7b17",
	}
	layer, err := img.LayerByDiffID(hash)
	if err != nil {
		t.Fatalf("Error getting layer by diff ID: %v, %v", hash, err)
	}
	rc, err := layer.Uncompressed()
	if err != nil {
		t.Fatal(err)
	}
	bs, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) == 0 {
		t.Errorf("layer.Uncompressed() returned a link file")
	}
}

func TestLoadManifest(t *testing.T) {
	manifest, err := LoadManifest(pathOpener("testdata/test_load_manifest.tar"))
	if err != nil {
		t.Fatalf("Error load manifest: %v", err)
	}
	if len(manifest) == 0 {
		t.Fatalf("get nothing")
	}
}
