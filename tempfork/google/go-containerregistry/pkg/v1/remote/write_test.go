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

package remote

import (
	"bytes"
	"context"
	"crypto"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/name"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/registry"
	v1 "github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/empty"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/mutate"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/partial"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/random"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/stream"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/tarball"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/types"
	"github.com/yeetrun/yeet/tempfork/google/go-containerregistry/pkg/v1/validate"
)

func mustNewTag(t *testing.T, s string) name.Tag {
	tag, err := name.NewTag(s, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(%v) = %v", s, err)
	}
	return tag
}

func TestUrl(t *testing.T) {
	tests := []struct {
		tag  string
		path string
		url  string
	}{{
		tag:  "gcr.io/foo/bar:latest",
		path: "/v2/foo/bar/manifests/latest",
		url:  "https://gcr.io/v2/foo/bar/manifests/latest",
	}, {
		tag:  "localhost:8080/foo/bar:baz",
		path: "/v2/foo/bar/blobs/upload",
		url:  "http://localhost:8080/v2/foo/bar/blobs/upload",
	}}

	for _, test := range tests {
		w := &writer{
			repo: mustNewTag(t, test.tag).Context(),
		}
		if got, want := w.url(test.path), test.url; got.String() != want {
			t.Errorf("url(%v) = %v, want %v", test.path, got.String(), want)
		}
	}
}

func TestNextLocation(t *testing.T) {
	tests := []struct {
		location string
		url      string
	}{{
		location: "https://gcr.io/v2/foo/bar/blobs/uploads/1234567?baz=blah",
		url:      "https://gcr.io/v2/foo/bar/blobs/uploads/1234567?baz=blah",
	}, {
		location: "/v2/foo/bar/blobs/uploads/1234567?baz=blah",
		url:      "https://gcr.io/v2/foo/bar/blobs/uploads/1234567?baz=blah",
	}}

	ref := mustNewTag(t, "gcr.io/foo/bar:latest")
	w := &writer{
		repo: ref.Context(),
	}

	for _, test := range tests {
		resp := &http.Response{
			Header: map[string][]string{
				"Location": {test.location},
			},
			Request: &http.Request{
				URL: &url.URL{
					Scheme: ref.Registry.Scheme(),
					Host:   ref.RegistryStr(),
				},
			},
		}

		got, err := w.nextLocation(resp)
		if err != nil {
			t.Errorf("nextLocation(%v) = %v", resp, err)
		}
		want := test.url
		if got != want {
			t.Errorf("nextLocation(%v) = %v, want %v", resp, got, want)
		}
	}
}

type closer interface {
	Close()
}

func setupImage(t *testing.T) v1.Image {
	rnd, err := random.Image(1024, 1)
	if err != nil {
		t.Fatalf("random.Image() = %v", err)
	}
	return rnd
}

func setupIndex(t *testing.T, children int64) v1.ImageIndex {
	rnd, err := random.Index(1024, 1, children)
	if err != nil {
		t.Fatalf("random.Index() = %v", err)
	}
	return rnd
}

func mustConfigName(t *testing.T, img v1.Image) v1.Hash {
	h, err := img.ConfigName()
	if err != nil {
		t.Fatalf("ConfigName() = %v", err)
	}
	return h
}

func setupWriter(repo string, handler http.HandlerFunc) (*writer, closer, error) {
	server := httptest.NewServer(handler)
	return setupWriterWithServer(server, repo)
}

func setupWriterWithServer(server *httptest.Server, repo string) (*writer, closer, error) {
	u, err := url.Parse(server.URL)
	if err != nil {
		server.Close()
		return nil, nil, err
	}
	tag, err := name.NewTag(fmt.Sprintf("%s/%s:latest", u.Host, repo), name.WeakValidation)
	if err != nil {
		server.Close()
		return nil, nil, err
	}

	return &writer{
		repo:      tag.Context(),
		client:    http.DefaultClient,
		predicate: defaultRetryPredicate,
		backoff:   defaultRetryBackoff,
	}, server, nil
}

func TestCheckExistingBlob(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		existing bool
		wantErr  bool
	}{{
		name:     "success",
		status:   http.StatusOK,
		existing: true,
	}, {
		name:     "not found",
		status:   http.StatusNotFound,
		existing: false,
	}, {
		name:     "error",
		status:   http.StatusInternalServerError,
		existing: false,
		wantErr:  true,
	}}

	img := setupImage(t)
	h := mustConfigName(t, img)
	expectedRepo := "foo/bar"
	expectedPath := fmt.Sprintf("/v2/%s/blobs/%s", expectedRepo, h.String())

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w, closer, err := setupWriter(expectedRepo, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodHead {
					t.Errorf("Method; got %v, want %v", r.Method, http.MethodHead)
				}
				if r.URL.Path != expectedPath {
					t.Errorf("URL; got %v, want %v", r.URL.Path, expectedPath)
				}
				http.Error(w, http.StatusText(test.status), test.status)
			}))
			if err != nil {
				t.Fatalf("setupWriter() = %v", err)
			}
			defer closer.Close()

			existing, err := w.checkExistingBlob(context.Background(), h)
			if test.existing != existing {
				t.Errorf("checkExistingBlob() = %v, want %v", existing, test.existing)
			}
			if err != nil && !test.wantErr {
				t.Errorf("checkExistingBlob() = %v", err)
			} else if err == nil && test.wantErr {
				t.Error("checkExistingBlob() wanted err, got nil")
			}
		})
	}
}

func TestInitiateUploadNoMountsExists(t *testing.T) {
	img := setupImage(t)
	h := mustConfigName(t, img)
	expectedRepo := "foo/bar"
	expectedPath := fmt.Sprintf("/v2/%s/blobs/uploads/", expectedRepo)
	expectedQuery := url.Values{
		"mount": []string{h.String()},
		"from":  []string{"baz/bar"},
	}.Encode()

	w, closer, err := setupWriter(expectedRepo, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method; got %v, want %v", r.Method, http.MethodPost)
		}
		if r.URL.Path != expectedPath {
			t.Errorf("URL; got %v, want %v", r.URL.Path, expectedPath)
		}
		if r.URL.RawQuery != expectedQuery {
			t.Errorf("RawQuery; got %v, want %v", r.URL.RawQuery, expectedQuery)
		}
		http.Error(w, "Mounted", http.StatusCreated)
	}))
	if err != nil {
		t.Fatalf("setupWriter() = %v", err)
	}
	defer closer.Close()

	_, mounted, err := w.initiateUpload(context.Background(), "baz/bar", h.String(), "")
	if err != nil {
		t.Errorf("intiateUpload() = %v", err)
	}
	if !mounted {
		t.Error("initiateUpload() = !mounted, want mounted")
	}
}

func TestInitiateUploadNoMountsInitiated(t *testing.T) {
	img := setupImage(t)
	h := mustConfigName(t, img)
	expectedRepo := "baz/blah"
	expectedPath := fmt.Sprintf("/v2/%s/blobs/uploads/", expectedRepo)
	expectedQuery := url.Values{
		"mount": []string{h.String()},
		"from":  []string{"baz/bar"},
	}.Encode()
	expectedLocation := "https://somewhere.io/upload?foo=bar"

	w, closer, err := setupWriter(expectedRepo, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method; got %v, want %v", r.Method, http.MethodPost)
		}
		if r.URL.Path != expectedPath {
			t.Errorf("URL; got %v, want %v", r.URL.Path, expectedPath)
		}
		if r.URL.RawQuery != expectedQuery {
			t.Errorf("RawQuery; got %v, want %v", r.URL.RawQuery, expectedQuery)
		}
		w.Header().Set("Location", expectedLocation)
		http.Error(w, "Initiated", http.StatusAccepted)
	}))
	if err != nil {
		t.Fatalf("setupWriter() = %v", err)
	}
	defer closer.Close()

	location, mounted, err := w.initiateUpload(context.Background(), "baz/bar", h.String(), "")
	if err != nil {
		t.Errorf("intiateUpload() = %v", err)
	}
	if mounted {
		t.Error("initiateUpload() = mounted, want !mounted")
	}
	if location != expectedLocation {
		t.Errorf("initiateUpload(); got %v, want %v", location, expectedLocation)
	}
}

func TestInitiateUploadNoMountsBadStatus(t *testing.T) {
	img := setupImage(t)
	h := mustConfigName(t, img)
	expectedRepo := "ugh/another"
	expectedPath := fmt.Sprintf("/v2/%s/blobs/uploads/", expectedRepo)
	expectedQuery := url.Values{
		"mount": []string{h.String()},
		"from":  []string{"baz/bar"},
	}.Encode()

	first := true

	w, closer, err := setupWriter(expectedRepo, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method; got %v, want %v", r.Method, http.MethodPost)
		}
		if r.URL.Path != expectedPath {
			t.Errorf("URL; got %v, want %v", r.URL.Path, expectedPath)
		}
		if first {
			if r.URL.RawQuery != expectedQuery {
				t.Errorf("RawQuery; got %v, want %v", r.URL.RawQuery, expectedQuery)
			}
			first = false
		} else {
			if r.URL.RawQuery != "" {
				t.Errorf("RawQuery; got %v, want %v", r.URL.RawQuery, "")
			}
		}

		http.Error(w, "Unknown", http.StatusNoContent)
	}))
	if err != nil {
		t.Fatalf("setupWriter() = %v", err)
	}
	defer closer.Close()

	location, mounted, err := w.initiateUpload(context.Background(), "baz/bar", h.String(), "")
	if err == nil {
		t.Errorf("intiateUpload() = %v, %v; wanted error", location, mounted)
	}
}

func TestInitiateUploadMountsWithMountFromDifferentRegistry(t *testing.T) {
	img := setupImage(t)
	h := mustConfigName(t, img)
	expectedRepo := "yet/again"
	expectedPath := fmt.Sprintf("/v2/%s/blobs/uploads/", expectedRepo)
	expectedQuery := url.Values{
		"mount": []string{h.String()},
		"from":  []string{"baz/bar"},
	}.Encode()

	w, closer, err := setupWriter(expectedRepo, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method; got %v, want %v", r.Method, http.MethodPost)
		}
		if r.URL.Path != expectedPath {
			t.Errorf("URL; got %v, want %v", r.URL.Path, expectedPath)
		}
		if r.URL.RawQuery != expectedQuery {
			t.Errorf("RawQuery; got %v, want %v", r.URL.RawQuery, expectedQuery)
		}
		http.Error(w, "Mounted", http.StatusCreated)
	}))
	if err != nil {
		t.Fatalf("setupWriter() = %v", err)
	}
	defer closer.Close()

	_, mounted, err := w.initiateUpload(context.Background(), "baz/bar", h.String(), "")
	if err != nil {
		t.Errorf("intiateUpload() = %v", err)
	}
	if !mounted {
		t.Error("initiateUpload() = !mounted, want mounted")
	}
}

func TestInitiateUploadMountsWithMountFromTheSameRegistry(t *testing.T) {
	img := setupImage(t)
	h := mustConfigName(t, img)
	expectedMountRepo := "a/different/repo"
	expectedRepo := "yet/again"
	expectedPath := fmt.Sprintf("/v2/%s/blobs/uploads/", expectedRepo)
	expectedQuery := url.Values{
		"mount": []string{h.String()},
		"from":  []string{expectedMountRepo},
	}.Encode()

	serverHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method; got %v, want %v", r.Method, http.MethodPost)
		}
		if r.URL.Path != expectedPath {
			t.Errorf("URL; got %v, want %v", r.URL.Path, expectedPath)
		}
		if r.URL.RawQuery != expectedQuery {
			t.Errorf("RawQuery; got %v, want %v", r.URL.RawQuery, expectedQuery)
		}
		http.Error(w, "Mounted", http.StatusCreated)
	})
	server := httptest.NewServer(serverHandler)

	w, closer, err := setupWriterWithServer(server, expectedRepo)
	if err != nil {
		t.Fatalf("setupWriterWithServer() = %v", err)
	}
	defer closer.Close()

	_, mounted, err := w.initiateUpload(context.Background(), expectedMountRepo, h.String(), "")
	if err != nil {
		t.Errorf("intiateUpload() = %v", err)
	}
	if !mounted {
		t.Error("initiateUpload() = !mounted, want mounted")
	}
}

func TestInitiateUploadMountsWithOrigin(t *testing.T) {
	img := setupImage(t)
	h := mustConfigName(t, img)
	expectedMountRepo := "a/different/repo"
	expectedRepo := "yet/again"
	expectedPath := fmt.Sprintf("/v2/%s/blobs/uploads/", expectedRepo)
	expectedOrigin := "fakeOrigin"
	expectedQuery := url.Values{
		"mount":  []string{h.String()},
		"from":   []string{expectedMountRepo},
		"origin": []string{expectedOrigin},
	}.Encode()

	serverHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method; got %v, want %v", r.Method, http.MethodPost)
		}
		if r.URL.Path != expectedPath {
			t.Errorf("URL; got %v, want %v", r.URL.Path, expectedPath)
		}
		if r.URL.RawQuery != expectedQuery {
			t.Errorf("RawQuery; got %v, want %v", r.URL.RawQuery, expectedQuery)
		}
		http.Error(w, "Mounted", http.StatusCreated)
	})
	server := httptest.NewServer(serverHandler)

	w, closer, err := setupWriterWithServer(server, expectedRepo)
	if err != nil {
		t.Fatalf("setupWriterWithServer() = %v", err)
	}
	defer closer.Close()

	_, mounted, err := w.initiateUpload(context.Background(), expectedMountRepo, h.String(), "fakeOrigin")
	if err != nil {
		t.Errorf("intiateUpload() = %v", err)
	}
	if !mounted {
		t.Error("initiateUpload() = !mounted, want mounted")
	}
}

func TestInitiateUploadMountsWithOriginFallback(t *testing.T) {
	img := setupImage(t)
	h := mustConfigName(t, img)
	expectedMountRepo := "a/different/repo"
	expectedRepo := "yet/again"
	expectedPath := fmt.Sprintf("/v2/%s/blobs/uploads/", expectedRepo)
	expectedOrigin := "fakeOrigin"
	expectedQuery := url.Values{
		"mount":  []string{h.String()},
		"from":   []string{expectedMountRepo},
		"origin": []string{expectedOrigin},
	}.Encode()

	queries := []string{expectedQuery, ""}
	queryCount := 0

	serverHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method; got %v, want %v", r.Method, http.MethodPost)
		}
		if r.URL.Path != expectedPath {
			t.Errorf("URL; got %v, want %v", r.URL.Path, expectedPath)
		}
		if r.URL.RawQuery != queries[queryCount] {
			t.Errorf("RawQuery; got %v, want %v", r.URL.RawQuery, expectedQuery)
		}
		if queryCount == 0 {
			http.Error(w, "nope", http.StatusUnauthorized)
		} else {
			http.Error(w, "Mounted", http.StatusCreated)
		}
		queryCount++
	})
	server := httptest.NewServer(serverHandler)

	w, closer, err := setupWriterWithServer(server, expectedRepo)
	if err != nil {
		t.Fatalf("setupWriterWithServer() = %v", err)
	}
	defer closer.Close()

	_, mounted, err := w.initiateUpload(context.Background(), expectedMountRepo, h.String(), "fakeOrigin")
	if err != nil {
		t.Errorf("intiateUpload() = %v", err)
	}
	if !mounted {
		t.Error("initiateUpload() = !mounted, want mounted")
	}
}

func TestDedupeLayers(t *testing.T) {
	newBlob := func() io.ReadCloser { return io.NopCloser(bytes.NewReader(bytes.Repeat([]byte{'a'}, 10000))) }

	img, err := random.Image(1024, 3)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}

	// Append three identical tarball.Layers, which should be deduped
	// because contents can be hashed before uploading.
	for i := 0; i < 3; i++ {
		tl, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return newBlob(), nil })
		if err != nil {
			t.Fatalf("LayerFromOpener(#%d): %v", i, err)
		}
		img, err = mutate.AppendLayers(img, tl)
		if err != nil {
			t.Fatalf("mutate.AppendLayer(#%d): %v", i, err)
		}
	}

	// Append three identical stream.Layers, whose uploads will *not* be
	// deduped since Write can't tell they're identical ahead of time.
	for i := 0; i < 3; i++ {
		sl := stream.NewLayer(newBlob())
		img, err = mutate.AppendLayers(img, sl)
		if err != nil {
			t.Fatalf("mutate.AppendLayer(#%d): %v", i, err)
		}
	}

	expectedRepo := "write/time"
	headPathPrefix := fmt.Sprintf("/v2/%s/blobs/", expectedRepo)
	initiatePath := fmt.Sprintf("/v2/%s/blobs/uploads/", expectedRepo)
	manifestPath := fmt.Sprintf("/v2/%s/manifests/latest", expectedRepo)
	uploadPath := "/upload"
	commitPath := "/commit"
	var numUploads int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && strings.HasPrefix(r.URL.Path, headPathPrefix) && r.URL.Path != initiatePath {
			http.Error(w, "NotFound", http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case initiatePath:
			if r.Method != http.MethodPost {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPost)
			}
			w.Header().Set("Location", uploadPath)
			http.Error(w, "Accepted", http.StatusAccepted)
		case uploadPath:
			if r.Method != http.MethodPatch {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPatch)
			}
			atomic.AddInt32(&numUploads, 1)
			w.Header().Set("Location", commitPath)
			http.Error(w, "Created", http.StatusCreated)
		case commitPath:
			http.Error(w, "Created", http.StatusCreated)
		case manifestPath:
			if r.Method == http.MethodHead {
				w.Header().Set("Content-Type", string(types.DockerManifestSchema1Signed))
				w.Header().Set("Docker-Content-Digest", fakeDigest)
				w.Write([]byte("doesn't matter"))
				return
			}
			if r.Method != http.MethodPut {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPut)
			}
			http.Error(w, "Created", http.StatusCreated)
		default:
			t.Fatalf("Unexpected path: %v", r.URL.Path)
		}
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse(%v) = %v", server.URL, err)
	}
	tag, err := name.NewTag(fmt.Sprintf("%s/%s:latest", u.Host, expectedRepo), name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag() = %v", err)
	}

	if err := Write(tag, img); err != nil {
		t.Errorf("Write: %v", err)
	}

	// 3 random layers, 1 tarball layer (deduped), 3 stream layers (not deduped), 1 image config blob
	wantUploads := int32(3 + 1 + 3 + 1)
	if numUploads != wantUploads {
		t.Fatalf("Write uploaded %d blobs, want %d", numUploads, wantUploads)
	}
}

func TestStreamBlob(t *testing.T) {
	img := setupImage(t)
	expectedPath := "/vWhatever/I/decide"
	expectedCommitLocation := "https://commit.io/v12/blob"

	w, closer, err := setupWriter("what/ever", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("Method; got %v, want %v", r.Method, http.MethodPatch)
		}
		if r.URL.Path != expectedPath {
			t.Errorf("URL; got %v, want %v", r.URL.Path, expectedPath)
		}
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(Body) = %v", err)
		}
		want, err := img.RawConfigFile()
		if err != nil {
			t.Errorf("RawConfigFile() = %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("bytes.Equal(); got %v, want %v", got, want)
		}
		w.Header().Set("Location", expectedCommitLocation)
		http.Error(w, "Created", http.StatusCreated)
	}))
	if err != nil {
		t.Fatalf("setupWriter() = %v", err)
	}
	defer closer.Close()

	streamLocation := w.url(expectedPath)

	l, err := partial.ConfigLayer(img)
	if err != nil {
		t.Fatalf("ConfigLayer: %v", err)
	}

	commitLocation, err := w.streamBlob(context.Background(), l, streamLocation.String())
	if err != nil {
		t.Errorf("streamBlob() = %v", err)
	}
	if commitLocation != expectedCommitLocation {
		t.Errorf("streamBlob(); got %v, want %v", commitLocation, expectedCommitLocation)
	}
}

func TestStreamLayer(t *testing.T) {
	var n, wantSize int64 = 10000, 49
	newBlob := func() io.ReadCloser { return io.NopCloser(bytes.NewReader(bytes.Repeat([]byte{'a'}, int(n)))) }
	wantDigest := "sha256:3d7c465be28d9e1ed810c42aeb0e747b44441424f566722ba635dc93c947f30e"

	expectedPath := "/vWhatever/I/decide"
	expectedCommitLocation := "https://commit.io/v12/blob"
	w, closer, err := setupWriter("what/ever", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("Method; got %v, want %v", r.Method, http.MethodPatch)
		}
		if r.URL.Path != expectedPath {
			t.Errorf("URL; got %v, want %v", r.URL.Path, expectedPath)
		}

		h := crypto.SHA256.New()
		s, err := io.Copy(h, r.Body)
		if err != nil {
			t.Errorf("Reading body: %v", err)
		}
		if s != wantSize {
			t.Errorf("Received %d bytes, want %d", s, wantSize)
		}
		gotDigest := "sha256:" + hex.EncodeToString(h.Sum(nil))
		if gotDigest != wantDigest {
			t.Errorf("Received bytes with digest %q, want %q", gotDigest, wantDigest)
		}

		w.Header().Set("Location", expectedCommitLocation)
		http.Error(w, "Created", http.StatusCreated)
	}))
	if err != nil {
		t.Fatalf("setupWriter() = %v", err)
	}
	defer closer.Close()

	streamLocation := w.url(expectedPath)
	sl := stream.NewLayer(newBlob())

	commitLocation, err := w.streamBlob(context.Background(), sl, streamLocation.String())
	if err != nil {
		t.Errorf("streamBlob: %v", err)
	}
	if commitLocation != expectedCommitLocation {
		t.Errorf("streamBlob(); got %v, want %v", commitLocation, expectedCommitLocation)
	}
}

func TestCommitBlob(t *testing.T) {
	img := setupImage(t)
	h := mustConfigName(t, img)
	expectedPath := "/no/commitment/issues"
	expectedQuery := url.Values{
		"digest": []string{h.String()},
	}.Encode()

	w, closer, err := setupWriter("what/ever", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("Method; got %v, want %v", r.Method, http.MethodPut)
		}
		if r.URL.Path != expectedPath {
			t.Errorf("URL; got %v, want %v", r.URL.Path, expectedPath)
		}
		if r.URL.RawQuery != expectedQuery {
			t.Errorf("RawQuery; got %v, want %v", r.URL.RawQuery, expectedQuery)
		}
		http.Error(w, "Created", http.StatusCreated)
	}))
	if err != nil {
		t.Fatalf("setupWriter() = %v", err)
	}
	defer closer.Close()

	commitLocation := w.url(expectedPath)

	if err := w.commitBlob(context.Background(), commitLocation.String(), h.String()); err != nil {
		t.Errorf("commitBlob() = %v", err)
	}
}

func TestUploadOne(t *testing.T) {
	img := setupImage(t)
	h := mustConfigName(t, img)
	expectedRepo := "baz/blah"
	headPath := fmt.Sprintf("/v2/%s/blobs/%s", expectedRepo, h.String())
	initiatePath := fmt.Sprintf("/v2/%s/blobs/uploads/", expectedRepo)
	streamPath := "/path/to/upload"
	commitPath := "/path/to/commit"
	ctx := context.Background()

	uploaded := false
	w, closer, err := setupWriter(expectedRepo, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case headPath:
			if r.Method != http.MethodHead {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodHead)
			}
			if uploaded {
				return
			}
			http.Error(w, "NotFound", http.StatusNotFound)
		case initiatePath:
			if r.Method != http.MethodPost {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPost)
			}
			w.Header().Set("Location", streamPath)
			http.Error(w, "Initiated", http.StatusAccepted)
		case streamPath:
			if r.Method != http.MethodPatch {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPatch)
			}
			got, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("ReadAll(Body) = %v", err)
			}
			want, err := img.RawConfigFile()
			if err != nil {
				t.Errorf("RawConfigFile() = %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("bytes.Equal(); got %v, want %v", got, want)
			}
			w.Header().Set("Location", commitPath)
			http.Error(w, "Initiated", http.StatusAccepted)
		case commitPath:
			if r.Method != http.MethodPut {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPut)
			}
			uploaded = true
			http.Error(w, "Created", http.StatusCreated)
		default:
			t.Fatalf("Unexpected path: %v", r.URL.Path)
		}
	}))
	if err != nil {
		t.Fatalf("setupWriter() = %v", err)
	}
	defer closer.Close()

	l, err := partial.ConfigLayer(img)
	if err != nil {
		t.Fatalf("ConfigLayer: %v", err)
	}
	ml := &MountableLayer{
		Layer:     l,
		Reference: w.repo.Digest(h.String()),
	}
	if err := w.uploadOne(ctx, ml); err != nil {
		t.Errorf("uploadOne() = %v", err)
	}
	// Hit the existing blob path.
	if err := w.uploadOne(ctx, l); err != nil {
		t.Errorf("uploadOne() = %v", err)
	}
}

func TestUploadOneStreamedLayer(t *testing.T) {
	expectedRepo := "baz/blah"
	initiatePath := fmt.Sprintf("/v2/%s/blobs/uploads/", expectedRepo)
	streamPath := "/path/to/upload"
	commitPath := "/path/to/commit"
	ctx := context.Background()

	w, closer, err := setupWriter(expectedRepo, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case initiatePath:
			if r.Method != http.MethodPost {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPost)
			}
			w.Header().Set("Location", streamPath)
			http.Error(w, "Initiated", http.StatusAccepted)
		case streamPath:
			if r.Method != http.MethodPatch {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPatch)
			}
			// TODO(jasonhall): What should we check here?
			w.Header().Set("Location", commitPath)
			http.Error(w, "Initiated", http.StatusAccepted)
		case commitPath:
			if r.Method != http.MethodPut {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPut)
			}
			http.Error(w, "Created", http.StatusCreated)
		default:
			t.Fatalf("Unexpected path: %v", r.URL.Path)
		}
	}))
	if err != nil {
		t.Fatalf("setupWriter() = %v", err)
	}
	defer closer.Close()

	var n, wantSize int64 = 10000, 49
	newBlob := func() io.ReadCloser { return io.NopCloser(bytes.NewReader(bytes.Repeat([]byte{'a'}, int(n)))) }
	wantDigest := "sha256:3d7c465be28d9e1ed810c42aeb0e747b44441424f566722ba635dc93c947f30e"
	wantDiffID := "sha256:27dd1f61b867b6a0f6e9d8a41c43231de52107e53ae424de8f847b821db4b711"
	l := stream.NewLayer(newBlob())
	if err := w.uploadOne(ctx, l); err != nil {
		t.Fatalf("uploadOne: %v", err)
	}

	if dig, err := l.Digest(); err != nil {
		t.Errorf("Digest: %v", err)
	} else if dig.String() != wantDigest {
		t.Errorf("Digest got %q, want %q", dig, wantDigest)
	}
	if diffID, err := l.DiffID(); err != nil {
		t.Errorf("DiffID: %v", err)
	} else if diffID.String() != wantDiffID {
		t.Errorf("DiffID got %q, want %q", diffID, wantDiffID)
	}
	if size, err := l.Size(); err != nil {
		t.Errorf("Size: %v", err)
	} else if size != wantSize {
		t.Errorf("Size got %d, want %d", size, wantSize)
	}
}

func TestCommitImage(t *testing.T) {
	img := setupImage(t)
	ctx := context.Background()

	expectedRepo := "foo/bar"
	expectedPath := fmt.Sprintf("/v2/%s/manifests/latest", expectedRepo)

	w, closer, err := setupWriter(expectedRepo, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("Method; got %v, want %v", r.Method, http.MethodPut)
		}
		if r.URL.Path != expectedPath {
			t.Errorf("URL; got %v, want %v", r.URL.Path, expectedPath)
		}
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(Body) = %v", err)
		}
		want, err := img.RawManifest()
		if err != nil {
			t.Errorf("RawManifest() = %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("bytes.Equal(); got %v, want %v", got, want)
		}
		mt, err := img.MediaType()
		if err != nil {
			t.Errorf("MediaType() = %v", err)
		}
		if got, want := r.Header.Get("Content-Type"), string(mt); got != want {
			t.Errorf("Header; got %v, want %v", got, want)
		}
		http.Error(w, "Created", http.StatusCreated)
	}))
	if err != nil {
		t.Fatalf("setupWriter() = %v", err)
	}
	defer closer.Close()

	if err := w.commitManifest(ctx, img, w.repo.Tag("latest")); err != nil {
		t.Error("commitManifest() = ", err)
	}
}

func TestWrite(t *testing.T) {
	img := setupImage(t)
	expectedRepo := "write/time"
	headPathPrefix := fmt.Sprintf("/v2/%s/blobs/", expectedRepo)
	initiatePath := fmt.Sprintf("/v2/%s/blobs/uploads/", expectedRepo)
	manifestPath := fmt.Sprintf("/v2/%s/manifests/latest", expectedRepo)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && strings.HasPrefix(r.URL.Path, headPathPrefix) && r.URL.Path != initiatePath {
			http.Error(w, "NotFound", http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case initiatePath:
			if r.Method != http.MethodPost {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPost)
			}
			http.Error(w, "Mounted", http.StatusCreated)
		case manifestPath:
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method != http.MethodPut {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPut)
			}
			http.Error(w, "Created", http.StatusCreated)
		default:
			t.Fatalf("Unexpected path: %v", r.URL.Path)
		}
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse(%v) = %v", server.URL, err)
	}
	tag, err := name.NewTag(fmt.Sprintf("%s/%s:latest", u.Host, expectedRepo), name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag() = %v", err)
	}

	if err := Write(tag, img); err != nil {
		t.Errorf("Write() = %v", err)
	}
}

func TestWriteWithErrors(t *testing.T) {
	img := setupImage(t)
	expectedRepo := "write/time"
	headPathPrefix := fmt.Sprintf("/v2/%s/blobs/", expectedRepo)
	initiatePath := fmt.Sprintf("/v2/%s/blobs/uploads/", expectedRepo)
	manifestPath := fmt.Sprintf("/v2/%s/manifests/latest", expectedRepo)

	errorBody := `{"errors":[{"code":"NAME_INVALID","message":"some explanation of how things were messed up."}],"StatusCode":400}`
	expectedErrMsg, err := regexp.Compile(`POST .+ NAME_INVALID: some explanation of how things were messed up.`)
	if err != nil {
		t.Error(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && strings.HasPrefix(r.URL.Path, headPathPrefix) && r.URL.Path != initiatePath {
			http.Error(w, "NotFound", http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case manifestPath:
			w.WriteHeader(http.StatusNotFound)
		case initiatePath:
			if r.Method != http.MethodPost {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPost)
			}

			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(errorBody))
		default:
			t.Fatalf("Unexpected path: %v", r.URL.Path)
		}
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse(%v) = %v", server.URL, err)
	}
	tag, err := name.NewTag(fmt.Sprintf("%s/%s:latest", u.Host, expectedRepo), name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag() = %v", err)
	}

	c := make(chan v1.Update, 100)

	var terr *transport.Error
	if err := Write(tag, img, WithProgress(c)); err == nil {
		t.Error("Write() = nil; wanted error")
	} else if !errors.As(err, &terr) {
		t.Errorf("Write() = %T; wanted *transport.Error", err)
	} else if !expectedErrMsg.Match([]byte(terr.Error())) {
		diff := cmp.Diff(expectedErrMsg, terr.Error())
		t.Errorf("Write(); (-want +got) = %s", diff)
	}

	var last v1.Update
	for update := range c {
		last = update
	}
	if last.Error == nil {
		t.Error("Progress chan didn't report error")
	}
}

func TestDockerhubScopes(t *testing.T) {
	src, err := name.ParseReference("busybox")
	if err != nil {
		t.Fatal(err)
	}
	rl, err := random.Layer(1024, types.DockerLayer)
	if err != nil {
		t.Fatal(err)
	}
	ml := &MountableLayer{
		Layer:     rl,
		Reference: src,
	}
	want := src.Scope(transport.PullScope)

	for _, s := range []string{
		"jonjohnson/busybox",
		"docker.io/jonjohnson/busybox",
		"index.docker.io/jonjohnson/busybox",
	} {
		dst, err := name.ParseReference(s)
		if err != nil {
			t.Fatal(err)
		}

		scopes := scopesForUploadingImage(dst.Context(), []v1.Layer{ml})

		if len(scopes) != 2 {
			t.Errorf("Should have two scopes (src and dst), got %d", len(scopes))
		} else if diff := cmp.Diff(want, scopes[1]); diff != "" {
			t.Errorf("TestDockerhubScopes %q: (-want +got) = %v", s, diff)
		}
	}
}

func TestScopesForUploadingImage(t *testing.T) {
	referenceToUpload, err := name.NewTag("example.com/sample/sample:latest", name.WeakValidation)
	if err != nil {
		t.Fatalf("name.NewTag() = %v", err)
	}

	sameReference, err := name.NewTag("example.com/sample/sample:previous", name.WeakValidation)
	if err != nil {
		t.Fatalf("name.NewTag() = %v", err)
	}

	anotherRepo1, err := name.NewTag("example.com/sample/another_repo1:latest", name.WeakValidation)
	if err != nil {
		t.Fatalf("name.NewTag() = %v", err)
	}

	anotherRepo2, err := name.NewTag("example.com/sample/another_repo2:latest", name.WeakValidation)
	if err != nil {
		t.Fatalf("name.NewTag() = %v", err)
	}

	repoOnOtherRegistry, err := name.NewTag("other-domain.com/sample/any_repo:latest", name.WeakValidation)
	if err != nil {
		t.Fatalf("name.NewTag() = %v", err)
	}

	img := setupImage(t)
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("img.Layers() = %v", err)
	}
	wokeLayer := layers[0]

	testCases := []struct {
		name      string
		reference name.Reference
		layers    []v1.Layer
		expected  []string
	}{
		{
			name:      "empty layers",
			reference: referenceToUpload,
			layers:    []v1.Layer{},
			expected: []string{
				referenceToUpload.Scope(transport.PushScope),
			},
		},
		{
			name:      "mountable layers with same reference",
			reference: referenceToUpload,
			layers: []v1.Layer{
				&MountableLayer{
					Layer:     wokeLayer,
					Reference: sameReference,
				},
			},
			expected: []string{
				referenceToUpload.Scope(transport.PushScope),
			},
		},
		{
			name:      "mountable layers with single reference with no-duplicate",
			reference: referenceToUpload,
			layers: []v1.Layer{
				&MountableLayer{
					Layer:     wokeLayer,
					Reference: anotherRepo1,
				},
			},
			expected: []string{
				referenceToUpload.Scope(transport.PushScope),
				anotherRepo1.Scope(transport.PullScope),
			},
		},
		{
			name:      "mountable layers with single reference with duplicate",
			reference: referenceToUpload,
			layers: []v1.Layer{
				&MountableLayer{
					Layer:     wokeLayer,
					Reference: anotherRepo1,
				},
				&MountableLayer{
					Layer:     wokeLayer,
					Reference: anotherRepo1,
				},
			},
			expected: []string{
				referenceToUpload.Scope(transport.PushScope),
				anotherRepo1.Scope(transport.PullScope),
			},
		},
		{
			name:      "mountable layers with multiple references with no-duplicates",
			reference: referenceToUpload,
			layers: []v1.Layer{
				&MountableLayer{
					Layer:     wokeLayer,
					Reference: anotherRepo1,
				},
				&MountableLayer{
					Layer:     wokeLayer,
					Reference: anotherRepo2,
				},
			},
			expected: []string{
				referenceToUpload.Scope(transport.PushScope),
				anotherRepo1.Scope(transport.PullScope),
				anotherRepo2.Scope(transport.PullScope),
			},
		},
		{
			name:      "mountable layers with multiple references with duplicates",
			reference: referenceToUpload,
			layers: []v1.Layer{
				&MountableLayer{
					Layer:     wokeLayer,
					Reference: anotherRepo1,
				},
				&MountableLayer{
					Layer:     wokeLayer,
					Reference: anotherRepo2,
				},
				&MountableLayer{
					Layer:     wokeLayer,
					Reference: anotherRepo1,
				},
				&MountableLayer{
					Layer:     wokeLayer,
					Reference: anotherRepo2,
				},
			},
			expected: []string{
				referenceToUpload.Scope(transport.PushScope),
				anotherRepo1.Scope(transport.PullScope),
				anotherRepo2.Scope(transport.PullScope),
			},
		},
		{
			name:      "cross repository mountable layer",
			reference: referenceToUpload,
			layers: []v1.Layer{
				&MountableLayer{
					Layer:     wokeLayer,
					Reference: repoOnOtherRegistry,
				},
			},
			expected: []string{
				referenceToUpload.Scope(transport.PushScope),
			},
		},
	}

	for _, tc := range testCases {
		actual := scopesForUploadingImage(tc.reference.Context(), tc.layers)

		if want, got := tc.expected[0], actual[0]; want != got {
			t.Errorf("TestScopesForUploadingImage() %s: Wrong first scope; want %v, got %v", tc.name, want, got)
		}

		less := func(a, b string) bool {
			return strings.Compare(a, b) <= -1
		}
		if diff := cmp.Diff(tc.expected[1:], actual[1:], cmpopts.SortSlices(less)); diff != "" {
			t.Errorf("TestScopesForUploadingImage() %s: Wrong scopes (-want +got) = %v", tc.name, diff)
		}
	}
}

func TestWriteIndex(t *testing.T) {
	idx := setupIndex(t, 2)
	expectedRepo := "write/time"
	headPathPrefix := fmt.Sprintf("/v2/%s/blobs/", expectedRepo)
	initiatePath := fmt.Sprintf("/v2/%s/blobs/uploads/", expectedRepo)
	manifestPath := fmt.Sprintf("/v2/%s/manifests/latest", expectedRepo)
	childDigest := mustIndexManifest(t, idx).Manifests[0].Digest
	childPath := fmt.Sprintf("/v2/%s/manifests/%s", expectedRepo, childDigest)
	existingChildDigest := mustIndexManifest(t, idx).Manifests[1].Digest
	existingChildPath := fmt.Sprintf("/v2/%s/manifests/%s", expectedRepo, existingChildDigest)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && strings.HasPrefix(r.URL.Path, headPathPrefix) && r.URL.Path != initiatePath {
			http.Error(w, "NotFound", http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case initiatePath:
			if r.Method != http.MethodPost {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPost)
			}
			http.Error(w, "Mounted", http.StatusCreated)
		case manifestPath:
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method != http.MethodPut {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPut)
			}
			http.Error(w, "Created", http.StatusCreated)
		case existingChildPath:
			if r.Method == http.MethodHead {
				w.Header().Set("Content-Type", string(types.DockerManifestSchema1))
				w.Header().Set("Docker-Content-Digest", existingChildDigest.String())
				w.Header().Set("Content-Length", "123")
				return
			}
			t.Errorf("Unexpected method; got %v, want %v", r.Method, http.MethodHead)
		case childPath:
			if r.Method == http.MethodHead {
				http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
				return
			}
			if r.Method != http.MethodPut {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPut)
			}
			http.Error(w, "Created", http.StatusCreated)
		default:
			t.Fatalf("Unexpected path: %v", r.URL.Path)
		}
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse(%v) = %v", server.URL, err)
	}
	tag, err := name.NewTag(fmt.Sprintf("%s/%s:latest", u.Host, expectedRepo), name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag() = %v", err)
	}

	if err := WriteIndex(tag, idx); err != nil {
		t.Errorf("WriteIndex() = %v", err)
	}
}

// If we actually attempt to read the contents, this will fail the test.
type fakeForeignLayer struct {
	t *testing.T
}

func (l *fakeForeignLayer) MediaType() (types.MediaType, error) {
	return types.DockerForeignLayer, nil
}

func (l *fakeForeignLayer) Size() (int64, error) {
	return 0, nil
}

func (l *fakeForeignLayer) Digest() (v1.Hash, error) {
	return v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)}, nil
}

func (l *fakeForeignLayer) DiffID() (v1.Hash, error) {
	return v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)}, nil
}

func (l *fakeForeignLayer) Compressed() (io.ReadCloser, error) {
	l.t.Helper()
	l.t.Errorf("foreign layer not skipped: Compressed")
	return nil, nil
}

func (l *fakeForeignLayer) Uncompressed() (io.ReadCloser, error) {
	l.t.Helper()
	l.t.Errorf("foreign layer not skipped: Uncompressed")
	return nil, nil
}

func TestSkipForeignLayersByDefault(t *testing.T) {
	// Set up an image with a foreign layer.
	base := setupImage(t)
	img, err := mutate.AppendLayers(base, &fakeForeignLayer{t: t})
	if err != nil {
		t.Fatal(err)
	}

	// Set up a fake registry.
	s := httptest.NewServer(registry.New())
	defer s.Close()
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	dst := fmt.Sprintf("%s/test/foreign/upload", u.Host)
	ref, err := name.ParseReference(dst)
	if err != nil {
		t.Fatal(err)
	}

	if err := Write(ref, img); err != nil {
		t.Errorf("failed to Write: %v", err)
	}
}

func TestWriteForeignLayerIfOptionSet(t *testing.T) {
	// Set up an image with a foreign layer.
	base := setupImage(t)
	foreignLayer, err := random.Layer(1024, types.DockerForeignLayer)
	if err != nil {
		t.Fatal("random.Layer:", err)
	}
	img, err := mutate.AppendLayers(base, foreignLayer)
	if err != nil {
		t.Fatal(err)
	}

	expectedRepo := "write/time"
	headPathPrefix := fmt.Sprintf("/v2/%s/blobs/", expectedRepo)
	initiatePath := fmt.Sprintf("/v2/%s/blobs/uploads/", expectedRepo)
	manifestPath := fmt.Sprintf("/v2/%s/manifests/latest", expectedRepo)
	uploadPath := "/upload"
	commitPath := "/commit"
	var numUploads int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && strings.HasPrefix(r.URL.Path, headPathPrefix) && r.URL.Path != initiatePath {
			http.Error(w, "NotFound", http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case initiatePath:
			if r.Method != http.MethodPost {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPost)
			}
			w.Header().Set("Location", uploadPath)
			http.Error(w, "Accepted", http.StatusAccepted)
		case uploadPath:
			if r.Method != http.MethodPatch {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPatch)
			}
			atomic.AddInt32(&numUploads, 1)
			w.Header().Set("Location", commitPath)
			http.Error(w, "Created", http.StatusCreated)
		case commitPath:
			http.Error(w, "Created", http.StatusCreated)
		case manifestPath:
			if r.Method == http.MethodHead {
				w.Header().Set("Content-Type", string(types.DockerManifestSchema1Signed))
				w.Header().Set("Docker-Content-Digest", fakeDigest)
				w.Header().Set("Content-Length", "123")
				return
			}
			if r.Method != http.MethodPut && r.Method != http.MethodHead {
				t.Errorf("Method; got %v, want %v", r.Method, http.MethodPut)
			}
			http.Error(w, "Created", http.StatusCreated)
		default:
			t.Fatalf("Unexpected path: %v", r.URL.Path)
		}
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse(%v) = %v", server.URL, err)
	}
	tag, err := name.NewTag(fmt.Sprintf("%s/%s:latest", u.Host, expectedRepo), name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag() = %v", err)
	}

	if err := Write(tag, img, WithNondistributable); err != nil {
		t.Errorf("Write: %v", err)
	}

	// 1 random layer, 1 foreign layer, 1 image config blob
	wantUploads := int32(1 + 1 + 1)
	if numUploads != wantUploads {
		t.Fatalf("Write uploaded %d blobs, want %d", numUploads, wantUploads)
	}
}

func TestTag(t *testing.T) {
	idx := setupIndex(t, 3)
	// Set up a fake registry.
	s := httptest.NewServer(registry.New())
	defer s.Close()
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf("%s/test/tag:src", u.Host)
	srcRef, err := name.NewTag(src)
	if err != nil {
		t.Fatal(err)
	}

	if err := WriteIndex(srcRef, idx); err != nil {
		t.Fatal(err)
	}

	dst := fmt.Sprintf("%s/test/tag:dst", u.Host)
	dstRef, err := name.NewTag(dst)
	if err != nil {
		t.Fatal(err)
	}

	if err := Tag(dstRef, idx); err != nil {
		t.Fatal(err)
	}

	got, err := Index(dstRef)
	if err != nil {
		t.Fatal(err)
	}

	if err := validate.Index(got); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}

func TestTagDescriptor(t *testing.T) {
	idx := setupIndex(t, 3)
	// Set up a fake registry.
	s := httptest.NewServer(registry.New())
	defer s.Close()
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf("%s/test/tag:src", u.Host)
	srcRef, err := name.NewTag(src)
	if err != nil {
		t.Fatal(err)
	}

	if err := WriteIndex(srcRef, idx); err != nil {
		t.Fatal(err)
	}

	desc, err := Get(srcRef)
	if err != nil {
		t.Fatal(err)
	}

	dst := fmt.Sprintf("%s/test/tag:dst", u.Host)
	dstRef, err := name.NewTag(dst)
	if err != nil {
		t.Fatal(err)
	}

	if err := Tag(dstRef, desc); err != nil {
		t.Fatal(err)
	}
}

func TestNestedIndex(t *testing.T) {
	// Set up a fake registry.
	s := httptest.NewServer(registry.New())
	defer s.Close()
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf("%s/test/tag:src", u.Host)
	srcRef, err := name.NewTag(src)
	if err != nil {
		t.Fatal(err)
	}

	child, err := random.Index(1024, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	parent := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{
		Add: child,
		Descriptor: v1.Descriptor{
			URLs: []string{"example.com/url"},
		},
	})

	l, err := random.Layer(100, types.DockerLayer)
	if err != nil {
		t.Fatal(err)
	}

	parent = mutate.AppendManifests(parent, mutate.IndexAddendum{
		Add: l,
	})

	if err := WriteIndex(srcRef, parent); err != nil {
		t.Fatal(err)
	}
	pulled, err := Index(srcRef)
	if err != nil {
		t.Fatal(err)
	}

	if err := validate.Index(pulled); err != nil {
		t.Fatalf("validate.Index: %v", err)
	}

	digest, err := child.Digest()
	if err != nil {
		t.Fatal(err)
	}

	pulledChild, err := pulled.ImageIndex(digest)
	if err != nil {
		t.Fatal(err)
	}

	desc, err := partial.Descriptor(pulledChild)
	if err != nil {
		t.Fatal(err)
	}

	if len(desc.URLs) != 1 {
		t.Fatalf("expected url for pulledChild")
	}

	if want, got := "example.com/url", desc.URLs[0]; want != got {
		t.Errorf("pulledChild.urls[0] = %s != %s", got, want)
	}
}

func BenchmarkWrite(b *testing.B) {
	// unfortunately the registry _and_ the img have caching behaviour, so we need a new registry
	// and image every iteration of benchmarking.
	for i := 0; i < b.N; i++ {
		// set up the registry
		s := httptest.NewServer(registry.New())
		defer s.Close()

		// load the image
		img, err := random.Image(50*1024*1024, 10)
		if err != nil {
			b.Fatalf("random.Image(...): %v", err)
		}

		b.ResetTimer()

		tagStr := strings.TrimPrefix(s.URL+"/test/image:tag", "http://")
		tag, err := name.NewTag(tagStr)
		if err != nil {
			b.Fatalf("parsing tag (%s): %v", tagStr, err)
		}

		err = Write(tag, img)
		if err != nil {
			b.Fatalf("pushing tag one: %v", err)
		}
	}
}
