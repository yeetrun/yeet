// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"tailscale.com/syncs"
)

var (
	// ErrBlobNotFound indicates the blob was not found in storage
	ErrBlobNotFound = errors.New("blob not found")
	// ErrManifestNotFound indicates the manifest was not found
	ErrManifestNotFound = errors.New("manifest not found")
	// ErrDigestMismatch indicates the digest does not match the content
	ErrDigestMismatch = errors.New("digest mismatch")
)

type ManifestMetadata struct {
	MediaType string
	Digest    string
	Size      int64
	Data      io.ReadCloser
}

// Storage provides content-addressable storage for registry blobs and manifests.
type Storage interface {
	// GetBlob retrieves a blob by digest as a stream
	GetBlob(ctx context.Context, digest string) (io.ReadCloser, error)
	// BlobSize returns the size of a blob by digest.
	BlobSize(ctx context.Context, digest string) (int64, error)
	// BlobExists checks if a blob exists
	BlobExists(ctx context.Context, digest string) bool
	// DeleteBlob removes a blob
	DeleteBlob(ctx context.Context, digest string) error

	// GetManifest retrieves a manifest for a repository and reference as a stream
	GetManifest(ctx context.Context, repo, reference string) (*ManifestMetadata, error)
	// PutManifest stores a manifest for a repository and reference
	PutManifest(ctx context.Context, repo, reference string, data []byte, mediaType string) (digest string, err error)
	// ManifestExists checks if a manifest exists
	ManifestExists(ctx context.Context, repo, reference string) bool
	// DeleteManifest removes a manifest
	DeleteManifest(ctx context.Context, repo, reference string) error

	// NewUpload creates a new upload session
	NewUpload(ctx context.Context) (*UploadSession, error)
	// GetUpload retrieves an upload session
	GetUpload(ctx context.Context, uuid string) (*UploadSession, error)
	// CopyChunk copies a chunk to an upload session
	CopyChunk(ctx context.Context, uuid string, r io.Reader) (*UploadSession, error)
	// CompleteUpload saves an upload session
	CompleteUpload(ctx context.Context, uuid, expectedDigest string) (string, error)
	// AbortUpload removes an upload session
	AbortUpload(ctx context.Context, uuid string) error
}

// UploadSession represents an ongoing blob upload.
type UploadSession struct {
	UUID    string
	Written int64
}

// FilesystemStorage implements Storage using the filesystem.
type FilesystemStorage struct {
	rootDir string
	uploads syncs.Map[string, *fileUpload]
}

type fileUpload struct {
	mu      sync.Mutex
	uuid    string
	sha256  hash.Hash
	file    *os.File
	written int64
}

func (f *fileUpload) Session() *UploadSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessionLocked()
}

func (f *fileUpload) sessionLocked() *UploadSession {
	return &UploadSession{
		UUID:    f.uuid,
		Written: f.written,
	}
}

func (f *fileUpload) Digest() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return "sha256:" + hex.EncodeToString(f.sha256.Sum(nil))
}

var _ Storage = (*FilesystemStorage)(nil)

// NewFilesystemStorage creates a new filesystem-based storage.
func NewFilesystemStorage(rootDir string) (*FilesystemStorage, error) {
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		return nil, fmt.Errorf("create root directory: %w", err)
	}

	blobsDir := filepath.Join(rootDir, "blobs")
	if err := os.MkdirAll(blobsDir, 0755); err != nil {
		return nil, fmt.Errorf("create blobs directory: %w", err)
	}

	manifestsDir := filepath.Join(rootDir, "manifests")
	if err := os.MkdirAll(manifestsDir, 0755); err != nil {
		return nil, fmt.Errorf("create manifests directory: %w", err)
	}

	return &FilesystemStorage{
		rootDir: rootDir,
	}, nil
}

func (s *FilesystemStorage) blobPath(digest string) string {
	digest = strings.TrimPrefix(digest, "sha256:")
	// Store blobs in a two-level directory structure: blobs/sha256/ab/cd/abcd...
	if len(digest) < 4 {
		return filepath.Join(s.rootDir, "blobs", digest)
	}
	return filepath.Join(s.rootDir, "blobs", "sha256", digest[0:2], digest[2:4], digest)
}

func (s *FilesystemStorage) uploadPath(uuid string) string {
	return filepath.Join(s.rootDir, "uploads", uuid)
}

func (s *FilesystemStorage) manifestPath(repo, reference string) string {
	return filepath.Join(s.rootDir, "manifests", repo, reference)
}

// GetBlob retrieves a blob by digest as a stream.
func (s *FilesystemStorage) GetBlob(ctx context.Context, digest string) (io.ReadCloser, error) {
	path := s.blobPath(digest)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrBlobNotFound
		}
		return nil, fmt.Errorf("open blob: %w", err)
	}
	return f, nil
}

// BlobExists checks if a blob exists.
func (s *FilesystemStorage) BlobExists(ctx context.Context, digest string) bool {
	path := s.blobPath(digest)
	_, err := os.Stat(path)
	return err == nil
}

// BlobSize returns the size of a blob by digest.
func (s *FilesystemStorage) BlobSize(ctx context.Context, digest string) (int64, error) {
	path := s.blobPath(digest)
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrBlobNotFound
		}
		return 0, fmt.Errorf("stat blob: %w", err)
	}
	return st.Size(), nil
}

// DeleteBlob removes a blob.
func (s *FilesystemStorage) DeleteBlob(ctx context.Context, digest string) error {
	path := s.blobPath(digest)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete blob: %w", err)
	}
	return nil
}

// GetManifest retrieves a manifest for a repository and reference.
func (s *FilesystemStorage) GetManifest(ctx context.Context, repo, reference string) (*ManifestMetadata, error) {
	path := s.manifestPath(repo, reference)

	// Read media type from companion file first
	mediaTypePath := path + ".mediatype"
	mediaTypeBytes, err := os.ReadFile(mediaTypePath)
	mediaType := ""
	if err == nil {
		mediaType = string(mediaTypeBytes)
	}

	// Open manifest file for streaming
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrManifestNotFound
		}
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	digest, err := computeDigest(f)
	if err != nil {
		return nil, fmt.Errorf("compute digest: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat manifest: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek manifest: %w", err)
	}

	return &ManifestMetadata{
		MediaType: mediaType,
		Digest:    digest,
		Size:      st.Size(),
		Data:      f,
	}, nil
}

// PutManifest stores a manifest for a repository and reference.
func (s *FilesystemStorage) PutManifest(ctx context.Context, repo, reference string, data []byte, mediaType string) (string, error) {
	if mediaType == "" {
		return "", fmt.Errorf("media type is empty")
	}

	path := s.manifestPath(repo, reference)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("create manifest directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("write manifest: %w", err)
	}

	// Store media type in companion file
	mediaTypePath := path + ".mediatype"
	if err := os.WriteFile(mediaTypePath, []byte(mediaType), 0644); err != nil {
		return "", fmt.Errorf("write media type: %w", err)
	}

	digest, err := computeDigest(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("compute digest: %w", err)
	}
	if !strings.HasPrefix(reference, "sha256:") {
		// Also store by digest
		digestPath := s.manifestPath(repo, digest)
		if err := os.MkdirAll(filepath.Dir(digestPath), 0755); err != nil {
			return "", fmt.Errorf("create manifest digest directory: %w", err)
		}
		if err := os.WriteFile(digestPath, data, 0644); err != nil {
			return "", fmt.Errorf("write manifest by digest: %w", err)
		}
		if err := os.WriteFile(digestPath+".mediatype", []byte(mediaType), 0644); err != nil {
			return "", fmt.Errorf("write manifest by digest media type: %w", err)
		}
	}

	return digest, nil
}

// ManifestExists checks if a manifest exists.
func (s *FilesystemStorage) ManifestExists(ctx context.Context, repo, reference string) bool {
	path := s.manifestPath(repo, reference)
	_, err := os.Stat(path)
	return err == nil
}

// DeleteManifest removes a manifest.
func (s *FilesystemStorage) DeleteManifest(ctx context.Context, repo, reference string) error {
	path := s.manifestPath(repo, reference)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete manifest: %w", err)
	}

	// Also remove media type file
	mediaTypePath := path + ".mediatype"
	os.Remove(mediaTypePath) // Ignore errors

	return nil
}

func (s *FilesystemStorage) NewUpload(ctx context.Context) (*UploadSession, error) {
	fu := &fileUpload{
		uuid:   uuid.New().String(),
		sha256: sha256.New(),
	}
	p := s.uploadPath(fu.uuid)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return nil, fmt.Errorf("create upload directory: %w", err)
	}
	f, err := os.Create(p)
	if err != nil {
		return nil, fmt.Errorf("create upload file: %w", err)
	}
	fu.file = f
	s.uploads.Store(fu.uuid, fu)
	return fu.Session(), nil
}

func (s *FilesystemStorage) CopyChunk(ctx context.Context, uuid string, r io.Reader) (*UploadSession, error) {
	fu, ok := s.uploads.Load(uuid)
	if !ok {
		return nil, errors.New("upload not found")
	}
	fu.mu.Lock()
	defer fu.mu.Unlock()
	mw := io.MultiWriter(fu.file, fu.sha256)
	n, err := io.Copy(mw, r)
	if err != nil {
		return nil, fmt.Errorf("copy chunk: %w", err)
	}
	fu.written += n
	return fu.sessionLocked(), nil
}

func (s *FilesystemStorage) CompleteUpload(ctx context.Context, uuid, expectedDigest string) (string, error) {
	fu, ok := s.uploads.LoadAndDelete(uuid)
	if !ok {
		return "", errors.New("upload not found")
	}
	if err := fu.file.Sync(); err != nil {
		return "", fmt.Errorf("sync upload file: %w", err)
	}
	if err := fu.file.Close(); err != nil {
		return "", fmt.Errorf("close upload file: %w", err)
	}
	digest := fu.Digest()
	if digest != expectedDigest {
		return "", fmt.Errorf("digest mismatch: %s != %s", digest, expectedDigest)
	}
	bp := s.blobPath(digest)
	if err := os.MkdirAll(filepath.Dir(bp), 0755); err != nil {
		return "", fmt.Errorf("create blob directory: %w", err)
	}
	if err := os.Rename(fu.file.Name(), bp); err != nil {
		return "", fmt.Errorf("rename blob: %w", err)
	}
	return digest, nil
}

// GetUpload retrieves an upload session.
func (s *FilesystemStorage) GetUpload(ctx context.Context, uuid string) (*UploadSession, error) {
	fu, ok := s.uploads.Load(uuid)
	if !ok {
		return nil, errors.New("upload not found")
	}
	return fu.Session(), nil
}

// AbortUpload removes an upload session.
func (s *FilesystemStorage) AbortUpload(ctx context.Context, uuid string) error {
	fu, ok := s.uploads.LoadAndDelete(uuid)
	if !ok {
		return nil
	}
	if err := fu.file.Close(); err != nil {
		log.Printf("error closing upload file: %v", err)
	}
	if err := os.Remove(fu.file.Name()); err != nil {
		log.Printf("error removing upload file: %v", err)
	}
	return nil
}
