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

package layout

import (
	"path/filepath"
	"testing"

	v1 "github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/partial"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/types"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/validate"
)

var (
	indexDigest = v1.Hash{
		Algorithm: "sha256",
		Hex:       "05f95b26ed10668b7183c1e2da98610e91372fa9f510046d4ce5812addad86b5",
	}
	manifestDigest = v1.Hash{
		Algorithm: "sha256",
		Hex:       "eebff607b1628d67459b0596643fc07de70d702eccf030f0bc7bb6fc2b278650",
	}
	configDigest = v1.Hash{
		Algorithm: "sha256",
		Hex:       "6e0b05049ed9c17d02e1a55e80d6599dbfcce7f4f4b022e3c673e685789c470e",
	}
	bogusDigest = v1.Hash{
		Algorithm: "sha256",
		Hex:       "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	}
	customManifestDigest = v1.Hash{
		Algorithm: "sha256",
		Hex:       "b544f71ecd82372bc9a3c0dbef378abfd2734fe437df81ff6e242a0d720d8e3e",
	}
	bogusPath                         = filepath.Join("testdata", "does_not_exist")
	testPath                          = filepath.Join("testdata", "test_index")
	testPathOneImage                  = filepath.Join("testdata", "test_index_one_image")
	testPathMediaType                 = filepath.Join("testdata", "test_index_media_type")
	customMediaType   types.MediaType = "application/tar+gzip"
)

func TestImage(t *testing.T) {
	lp, err := FromPath(testPath)
	if err != nil {
		t.Fatalf("FromPath() = %v", err)
	}
	img, err := lp.Image(manifestDigest)
	if err != nil {
		t.Fatalf("Image() = %v", err)
	}

	if err := validate.Image(img); err != nil {
		t.Errorf("validate.Image() = %v", err)
	}

	mt, err := img.MediaType()
	if err != nil {
		t.Errorf("MediaType() = %v", err)
	} else if got, want := mt, types.OCIManifestSchema1; got != want {
		t.Errorf("MediaType(); want: %v got: %v", want, got)
	}

	cfg, err := img.LayerByDigest(configDigest)
	if err != nil {
		t.Fatalf("LayerByDigest(%s) = %v", configDigest, err)
	}

	cfgName, err := img.ConfigName()
	if err != nil {
		t.Fatalf("ConfigName() = %v", err)
	}

	cfgDigest, err := cfg.Digest()
	if err != nil {
		t.Fatalf("cfg.Digest() = %v", err)
	}

	if got, want := cfgDigest, cfgName; got != want {
		t.Errorf("ConfigName(); want: %v got: %v", want, got)
	}

	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("img.Layers() = %v", err)
	}

	mediaType, err := layers[0].MediaType()
	if err != nil {
		t.Fatalf("img.Layers() = %v", err)
	}

	// Fixture is a DockerLayer
	if got, want := mediaType, types.DockerLayer; got != want {
		t.Fatalf("MediaType(); want: %q got: %q", want, got)
	}

	if ok, err := partial.Exists(layers[0]); err != nil {
		t.Fatal(err)
	} else if got, want := ok, true; got != want {
		t.Errorf("Exists() = %t != %t", got, want)
	}
}

func TestImageWithEmptyHash(t *testing.T) {
	lp, err := FromPath(testPathOneImage)
	if err != nil {
		t.Fatalf("FromPath() = %v", err)
	}
	img, err := lp.Image(v1.Hash{})
	if err != nil {
		t.Fatalf("Image() = %v", err)
	}

	if err := validate.Image(img); err != nil {
		t.Errorf("validate.Image() = %v", err)
	}
}

func TestImageErrors(t *testing.T) {
	lp, err := FromPath(testPath)
	if err != nil {
		t.Fatalf("FromPath() = %v", err)
	}
	img, err := lp.Image(manifestDigest)
	if err != nil {
		t.Fatalf("Image() = %v", err)
	}

	if _, err := img.LayerByDigest(bogusDigest); err == nil {
		t.Errorf("LayerByDigest(%s) = nil, expected err", bogusDigest)
	}

	if _, err := lp.Image(bogusDigest); err == nil {
		t.Errorf("Image(%s) = nil, expected err", bogusDigest)
	}

	if _, err := lp.Image(bogusDigest); err == nil {
		t.Errorf("Image(%s, %s) = nil, expected err", bogusPath, bogusDigest)
	}
}

func TestImageCustomMediaType(t *testing.T) {
	lp, err := FromPath(testPathMediaType)
	if err != nil {
		t.Fatalf("FromPath() = %v", err)
	}
	img, err := lp.Image(customManifestDigest)
	if err != nil {
		t.Fatalf("Image() = %v", err)
	}
	mt, err := img.MediaType()
	if err != nil {
		t.Errorf("MediaType() = %v", err)
	} else if got, want := mt, types.OCIManifestSchema1; got != want {
		t.Errorf("MediaType(); want: %v got: %v", want, got)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("img.Layers() = %v", err)
	}
	mediaType, err := layers[0].MediaType()
	if err != nil {
		t.Fatalf("img.Layers() = %v", err)
	}
	if got, want := mediaType, customMediaType; got != want {
		t.Fatalf("MediaType(); want: %q got: %q", want, got)
	}
}
