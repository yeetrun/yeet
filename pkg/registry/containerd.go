// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/snapshots"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"tailscale.com/syncs"
)

// ContainerdCacheStorage implements Storage using containerd's content and metadata stores.
// It works with Docker daemons configured with the containerd snapshotter.
// All blobs (layers, configs, manifests) are stored in containerd's content store,
// and images are registered in containerd's metadata store so they appear in
// `docker images` and `ctr images list`.
//
// This storage backend requires a running containerd daemon and registers all pushed
// images in the "moby" namespace (Docker's default namespace).
type ContainerdCacheStorage struct {
	containerdClient *containerd.Client // Containerd client for metadata operations
	bgCtx            context.Context
	cancelBg         context.CancelFunc

	contentStore containerdContentStore
	uploads      syncs.Map[string, *containerdUpload]
}

type containerdUpload struct {
	writer  content.Writer
	release func(context.Context) error
}

type containerdContentStore interface {
	Info(ctx context.Context, dg digest.Digest) (content.Info, error)
	ReaderAt(ctx context.Context, desc ocispec.Descriptor) (content.ReaderAt, error)
	Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error)
	Delete(ctx context.Context, dg digest.Digest) error
	Abort(ctx context.Context, ref string) error
}

var _ Storage = (*ContainerdCacheStorage)(nil)

// NewContainerdCacheStorage creates a new Docker cache-based storage.
// containerdSocket is the path to containerd's socket (e.g., /run/containerd/containerd.sock).
//
// This requires a running containerd daemon. Images pushed to the registry will be
// automatically registered in containerd's metadata and will appear in `docker images`
// and `ctr -n moby images list`.
func NewContainerdCacheStorage(containerdSocket string) (*ContainerdCacheStorage, error) {
	// Create containerd client for metadata operations
	client, err := containerd.New(containerdSocket, containerd.WithDefaultNamespace("moby"))
	if err != nil {
		return nil, fmt.Errorf("create containerd client: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &ContainerdCacheStorage{
		containerdClient: client,
		bgCtx:            ctx,
		cancelBg:         cancel,
	}, nil
}

// Close closes the containerd client connection.
func (s *ContainerdCacheStorage) Close() error {
	s.cancelBg()
	if s.containerdClient != nil {
		return s.containerdClient.Close()
	}
	return nil
}

func (s *ContainerdCacheStorage) getContentStore() containerdContentStore {
	if s.contentStore != nil {
		return s.contentStore
	}
	if s.containerdClient == nil {
		return nil
	}
	return s.containerdClient.ContentStore()
}

type readAtCloserAsReader struct {
	io.ReaderAt
	io.Closer
	offset int64
}

func (r *readAtCloserAsReader) Read(p []byte) (int, error) {
	n, err := r.ReaderAt.ReadAt(p, r.offset)
	r.offset += int64(n)
	return n, err
}

// GetBlob retrieves a blob by digest from Docker's image cache as a stream.
func (s *ContainerdCacheStorage) GetBlob(ctx context.Context, dg string) (io.ReadCloser, error) {
	cs := s.getContentStore()
	if cs == nil {
		return nil, errors.New("content store unavailable")
	}
	r, err := cs.ReaderAt(ctx, ocispec.Descriptor{Digest: digest.Digest(dg)})
	if err != nil {
		return nil, fmt.Errorf("get blob from containerd: %w", err)
	}
	return &readAtCloserAsReader{
		ReaderAt: r,
		Closer:   r,
	}, nil
}

// BlobExists checks if a blob exists in Docker's cache.
func (s *ContainerdCacheStorage) BlobExists(ctx context.Context, dg string) bool {
	cs := s.getContentStore()
	if cs == nil {
		return false
	}
	_, err := cs.Info(ctx, digest.Digest(dg))
	if err != nil {
		return false
	}
	ra, err := cs.ReaderAt(ctx, ocispec.Descriptor{Digest: digest.Digest(dg)})
	if err != nil {
		return false
	}
	_ = ra.Close()
	return true
}

// BlobSize returns the size of a blob by digest.
func (s *ContainerdCacheStorage) BlobSize(ctx context.Context, dg string) (int64, error) {
	cs := s.getContentStore()
	if cs == nil {
		return 0, errors.New("content store unavailable")
	}
	info, err := cs.Info(ctx, digest.Digest(dg))
	if err != nil {
		if errdefs.IsNotFound(err) {
			return 0, ErrBlobNotFound
		}
		return 0, fmt.Errorf("get blob info from containerd: %w", err)
	}
	return info.Size, nil
}

// DeleteBlob removes a blob from Docker's image cache.
func (s *ContainerdCacheStorage) DeleteBlob(ctx context.Context, dg string) error {
	cs := s.getContentStore()
	if cs == nil {
		return errors.New("content store unavailable")
	}
	return cs.Delete(ctx, digest.Digest(dg))
}

// GetManifest retrieves a manifest from containerd's metadata store.
func (s *ContainerdCacheStorage) GetManifest(ctx context.Context, repo, reference string) (*ManifestMetadata, error) {
	if strings.HasPrefix(reference, "sha256:") {
		cs := s.getContentStore()
		if cs == nil {
			return nil, errors.New("content store unavailable")
		}
		dg := digest.Digest(reference)
		info, err := cs.Info(ctx, dg)
		if err != nil {
			if errors.Is(err, errdefs.ErrNotFound) {
				return nil, ErrManifestNotFound
			}
			return nil, fmt.Errorf("get manifest info from containerd: %w", err)
		}
		blob, err := content.ReadBlob(ctx, cs, ocispec.Descriptor{Digest: dg})
		if err != nil {
			if errdefs.IsNotFound(err) {
				return nil, ErrManifestNotFound
			}
			return nil, fmt.Errorf("read manifest blob '%s' from containerd: %w", dg, err)
		}
		return &ManifestMetadata{
			MediaType: info.Labels["containerd.io/content/type"],
			Digest:    dg.String(),
			Size:      info.Size,
			Data:      io.NopCloser(bytes.NewReader(blob)),
		}, nil
	}
	imageName := repo + ":" + reference
	img, err := s.containerdClient.ImageService().Get(ctx, imageName)
	if err != nil {
		if errors.Is(err, errdefs.ErrNotFound) {
			return nil, ErrManifestNotFound
		}
		return nil, fmt.Errorf("get image from containerd: %w", err)
	}
	cs := s.getContentStore()
	if cs == nil {
		return nil, errors.New("content store unavailable")
	}
	blob, err := content.ReadBlob(ctx, cs, img.Target)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil, ErrManifestNotFound
		}
		return nil, fmt.Errorf("read blob '%s' from containerd content store: %w", img.Target.Digest, err)
	}

	return &ManifestMetadata{
		MediaType: img.Target.MediaType,
		Digest:    img.Target.Digest.String(),
		Size:      int64(img.Target.Size),
		Data:      io.NopCloser(bytes.NewReader(blob)),
	}, nil
}

// ParseRepositoryName parses a repository name and returns the normalized domain and path.
// It handles Docker Hub conventions and various repository name formats.
//
// Examples:
//   - "nginx" -> "docker.io", "library/nginx"
//   - "user/app" -> "docker.io", "user/app"
//   - "registry.example.com/user/app" -> "registry.example.com", "user/app"
//   - "registry.example.com/app" -> "registry.example.com", "app"
func ParseRepositoryName(repo string) (domain, path string) {
	if repo == "" {
		return "docker.io", ""
	}

	domain, path, _ = strings.Cut(repo, "/")
	if path == "" {
		domain, path = "docker.io", domain
	}

	// Check if domain contains a dot or colon (for ports like localhost:5000)
	// If not, treat as docker.io subproject
	if !strings.Contains(domain, ".") && !strings.Contains(domain, ":") {
		domain, path = "docker.io", repo // The full repo name becomes the path
	}

	// For docker.io, add library prefix if path doesn't contain a slash
	if !strings.Contains(path, "/") && domain == "docker.io" { // "nginx" -> "library/nginx"
		path = "library/" + path
	}

	return domain, path
}

// PutManifest stores a manifest in containerd's content store and registers the image.
func (s *ContainerdCacheStorage) PutManifest(ctx context.Context, repo, reference string, data []byte, mediaType string) (_ string, err error) {
	labels := make(map[string]string)
	domain, path := ParseRepositoryName(repo)
	repo = domain + "/" + path

	labels["containerd.io/distribution.source."+domain] = path
	labels["containerd.io/content/type"] = mediaType
	// Parse manifest to get child references
	var platform *ocispec.Platform
	switch mediaType {
	case ocispec.MediaTypeImageManifest, "application/vnd.docker.distribution.manifest.v2+json":
		var manifest ocispec.Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return "", fmt.Errorf("unmarshal manifest: %w", err)
		}
		labels["containerd.io/gc.ref.content.config"] = manifest.Config.Digest.String()
		for i, layer := range manifest.Layers {
			labels[fmt.Sprintf("containerd.io/gc.ref.content.l.%d", i)] = layer.Digest.String()
		}
		platform = manifest.Config.Platform

	case ocispec.MediaTypeImageIndex, "application/vnd.docker.distribution.manifest.list.v2+json":
		var index ocispec.Index
		if err := json.Unmarshal(data, &index); err != nil {
			return "", fmt.Errorf("unmarshal index: %w", err)
		}
		for i, manifest := range index.Manifests {
			labels[fmt.Sprintf("containerd.io/gc.ref.content.m.%d", i)] = manifest.Digest.String()
		}
	default:
		return "", fmt.Errorf("unsupported media type: %s", mediaType)
	}
	_ = platform
	upload, err := s.NewUpload(ctx)
	if err != nil {
		return "", fmt.Errorf("new upload: %w", err)
	}
	defer func() {
		if err != nil {
			s.AbortUpload(ctx, upload.UUID)
		}
	}()

	h := sha256.New()
	h.Write(data)
	wantDigest := digest.NewDigest(digest.SHA256, h)

	if _, err := s.CopyChunk(ctx, upload.UUID, bytes.NewReader(data)); err != nil {
		return "", fmt.Errorf("copy chunk: %w", err)
	}

	if _, err := s.CompleteUpload(ctx, upload.UUID, wantDigest.String()); err != nil {
		return "", fmt.Errorf("complete upload: %w", err)
	}

	img := images.Image{
		Name: repo + ":" + reference,
		Target: ocispec.Descriptor{
			MediaType: mediaType,
			Digest:    wantDigest,
			Size:      int64(len(data)),
		},
	}

	if _, err := s.containerdClient.ImageService().Create(ctx, img); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return "", fmt.Errorf("create image: %w", err)
		}
		if _, err := s.containerdClient.ImageService().Update(ctx, img); err != nil {
			return "", fmt.Errorf("update image: %w", err)
		}
	}
	ci := containerd.NewImage(s.containerdClient, img)
	if ok, err := ci.IsUnpacked(ctx, defaultSnapshotter); err != nil {
		return "", fmt.Errorf("is unpacked: %w", err)
	} else if !ok {
		if err := ci.Unpack(ctx, defaultSnapshotter, containerd.WithSnapshotterPlatformCheck(), func(ctx context.Context, uc *containerd.UnpackConfig) error {
			uc.SnapshotOpts = append(uc.SnapshotOpts, snapshots.WithLabels(map[string]string{
				"containerd.io/distribution.source." + domain: path,
			}))
			return nil
		}); err != nil {
			return "", fmt.Errorf("unpack: %w", err)
		}
	}

	cs := s.getContentStore()
	if cs == nil {
		return "", errors.New("content store unavailable")
	}
	if _, err := cs.Update(ctx, content.Info{
		Digest: wantDigest,
		Labels: labels,
	}); err != nil {
		return "", fmt.Errorf("update content store: %w", err)
	}

	return wantDigest.String(), nil
}

const defaultSnapshotter = "overlayfs"

// ManifestExists checks if a manifest exists in containerd's metadata.
func (s *ContainerdCacheStorage) ManifestExists(ctx context.Context, repo, reference string) bool {
	if strings.HasPrefix(reference, "sha256:") {
		_, err := s.containerdClient.ContentStore().Info(ctx, digest.Digest(reference))
		return err == nil
	}
	imageName := repo + ":" + reference
	_, err := s.containerdClient.ImageService().Get(ctx, imageName)
	return err == nil
}

// DeleteManifest removes a manifest from containerd.
func (s *ContainerdCacheStorage) DeleteManifest(ctx context.Context, repo, reference string) error {
	if strings.HasPrefix(reference, "sha256:") {
		if err := s.containerdClient.ContentStore().Delete(ctx, digest.Digest(reference)); err != nil {
			if errors.Is(err, errdefs.ErrNotFound) {
				return nil
			}
			return fmt.Errorf("delete manifest content from containerd: %w", err)
		}
		return nil
	}
	imageName := repo + ":" + reference

	err := s.containerdClient.ImageService().Delete(ctx, imageName)
	if err != nil {
		if errors.Is(err, errdefs.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("delete image from containerd: %w", err)
	}
	return nil
}

// NewUpload creates a new upload session.
func (s *ContainerdCacheStorage) NewUpload(_ context.Context) (*UploadSession, error) {
	// IMPORTANT: Ignore incoming context and instead use background context to
	// make sure we can actually upload.
	ctx, release, err := s.containerdClient.WithLease(s.bgCtx)
	if err != nil {
		return nil, fmt.Errorf("create lease: %w", err)
	}
	uuid := uuid.New().String()
	w, err := content.OpenWriter(ctx, s.containerdClient.ContentStore(), content.WithRef("upload-"+uuid))
	if err != nil {
		return nil, fmt.Errorf("open writer: %w", err)
	}
	s.uploads.Store(uuid, &containerdUpload{
		writer:  w,
		release: release,
	})
	return &UploadSession{UUID: uuid}, nil
}

// GetUpload retrieves an upload session.
func (s *ContainerdCacheStorage) GetUpload(_ context.Context, uuid string) (*UploadSession, error) {
	fu, ok := s.uploads.Load(uuid)
	if !ok {
		return nil, errors.New("upload not found")
	}
	info, err := fu.writer.Status()
	if err != nil {
		return nil, fmt.Errorf("get status: %w", err)
	}
	return &UploadSession{UUID: uuid, Written: info.Offset}, nil
}

// CopyChunk copies a chunk to an upload session.
func (s *ContainerdCacheStorage) CopyChunk(ctx context.Context, uuid string, r io.Reader) (*UploadSession, error) {
	fu, ok := s.uploads.Load(uuid)
	if !ok {
		return nil, errors.New("upload not found")
	}
	_, err := io.Copy(fu.writer, r)
	if err != nil {
		return nil, fmt.Errorf("copy chunk: %w", err)
	}
	info, err := fu.writer.Status()
	if err != nil {
		return nil, fmt.Errorf("writer status: %w", err)
	}
	return &UploadSession{UUID: uuid, Written: info.Offset}, nil
}

// CompleteUpload saves an upload session.
func (s *ContainerdCacheStorage) CompleteUpload(ctx context.Context, uuid, expectedDigest string) (dg string, err error) {
	fu, ok := s.uploads.LoadAndDelete(uuid)
	if !ok {
		return "", errors.New("upload not found")
	}

	releaseCtx := s.bgCtx
	if releaseCtx == nil {
		releaseCtx = context.Background()
	}
	defer func() {
		if fu.release == nil {
			return
		}
		if rerr := fu.release(releaseCtx); rerr != nil {
			if err == nil {
				err = fmt.Errorf("release lease: %w", rerr)
				return
			}
			err = errors.Join(err, fmt.Errorf("release lease: %w", rerr))
		}
	}()

	if err = fu.writer.Commit(ctx, 0, digest.Digest(expectedDigest)); err != nil {
		if errdefs.IsAlreadyExists(err) {
			dg = expectedDigest
			if uerr := s.markContentRoot(digest.Digest(dg)); uerr != nil {
				return "", uerr
			}
			return dg, nil
		}
		return "", fmt.Errorf("commit upload: %w", err)
	}
	dg = fu.writer.Digest().String()
	if uerr := s.markContentRoot(digest.Digest(dg)); uerr != nil {
		return "", uerr
	}
	return dg, nil
}

func (s *ContainerdCacheStorage) markContentRoot(dg digest.Digest) error {
	cs := s.getContentStore()
	if cs == nil {
		return errors.New("content store unavailable")
	}
	ctx := s.bgCtx
	if ctx == nil {
		ctx = context.Background()
	}
	labels := map[string]string{
		"containerd.io/gc.root": time.Now().UTC().Format(time.RFC3339),
	}
	if _, err := cs.Update(ctx, content.Info{Digest: dg, Labels: labels}, "labels.containerd.io/gc.root"); err != nil {
		return fmt.Errorf("mark content root %s: %w", dg, err)
	}
	return nil
}

// AbortUpload removes an upload session.
func (s *ContainerdCacheStorage) AbortUpload(ctx context.Context, uuid string) error {
	fu, ok := s.uploads.LoadAndDelete(uuid)
	if !ok {
		return nil
	}
	if err := fu.writer.Close(); err != nil {
		log.Printf("error closing upload file: %v", err)
	}
	if err := fu.release(ctx); err != nil {
		return fmt.Errorf("release lease: %w", err)
	}
	cs := s.getContentStore()
	if cs == nil {
		return errors.New("content store unavailable")
	}
	if err := cs.Abort(ctx, "upload-"+uuid); err != nil {
		return fmt.Errorf("abort upload: %w", err)
	}
	return nil
}
