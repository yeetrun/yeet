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
	"testing"

	v1 "github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/types"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/validate"
)

func TestIndex(t *testing.T) {
	idx, err := ImageIndexFromPath(testPath)
	if err != nil {
		t.Fatalf("ImageIndexFromPath() = %v", err)
	}

	if err := validate.Index(idx); err != nil {
		t.Errorf("validate.Index() = %v", err)
	}

	mt, err := idx.MediaType()
	if err != nil {
		t.Fatalf("MediaType() = %v", err)
	}

	if got, want := mt, types.OCIImageIndex; got != want {
		t.Errorf("MediaType(); want: %v got: %v", want, got)
	}

	indexHash, _ := v1.NewHash("sha256:2b29a2b8dea3af91ea7d0154be1da0c92d55ddd098540930fc8d3db7de377fdb")
	ii, err := idx.ImageIndex(indexHash)
	if err != nil {
		t.Fatalf("ImageIndex() = %v", err)
	}

	mt, err = ii.MediaType()
	if err != nil {
		t.Fatalf("MediaType() = %v", err)
	}

	if got, want := mt, types.DockerManifestList; got != want {
		t.Errorf("MediaType(); want: %v got: %v", want, got)
	}
}

func TestIndexErrors(t *testing.T) {
	idx, err := ImageIndexFromPath(testPath)
	if err != nil {
		t.Fatalf("ImageIndexFromPath() = %v", err)
	}

	if _, err := idx.Image(bogusDigest); err == nil {
		t.Errorf("idx.Image(%s) = nil, expected err", bogusDigest)
	}

	if _, err := idx.Image(indexDigest); err == nil {
		t.Errorf("idx.Image(%s) = nil, expected err", bogusDigest)
	}

	if _, err := idx.ImageIndex(bogusDigest); err == nil {
		t.Errorf("idx.ImageIndex(%s) = nil, expected err", bogusDigest)
	}

	if _, err := idx.ImageIndex(manifestDigest); err == nil {
		t.Errorf("idx.ImageIndex(%s) = nil, expected err", bogusDigest)
	}
}
