// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/shayne/yeet/pkg/compress"
)

// Registry implements an OCI Distribution Specification v1.1 compliant registry.
type Registry struct {
	storage Storage
	mux     *http.ServeMux
}

// New creates a new OCI-compliant registry with the given storage backend.
func New(storage Storage) *Registry {
	r := &Registry{
		storage: storage,
		mux:     http.NewServeMux(),
	}
	r.setupRoutes()
	return r
}

// PathType represents the type of registry operation
type PathType int

const (
	PathTypeUnknown PathType = iota
	PathTypeManifest
	PathTypeBlob
	PathTypeBlobUploadInit
	PathTypeBlobUpload
	PathTypeTagsList
)

func (pt PathType) String() string {
	switch pt {
	case PathTypeManifest:
		return "manifest"
	case PathTypeBlob:
		return "blob"
	case PathTypeBlobUploadInit:
		return "blob_upload_init"
	case PathTypeBlobUpload:
		return "blob_upload"
	case PathTypeTagsList:
		return "tags_list"
	default:
		return "unknown"
	}
}

// RegistryPath holds the parsed components of a registry path
type RegistryPath struct {
	Type      PathType
	Repo      string
	Reference string // For manifests: tag or digest; for blobs: digest; for uploads: uuid
}

// ParseRegistryPath parses a Docker Registry V2 API path
func ParseRegistryPath(path string) (*RegistryPath, error) {
	// Remove leading/trailing slashes
	path = strings.Trim(path, "/")

	// Split into parts
	parts := strings.Split(path, "/")

	// Must start with v2
	if len(parts) < 2 || parts[0] != "v2" {
		return nil, fmt.Errorf("path must start with /v2/")
	}

	// Need at least: v2, repo, operation
	if len(parts) < 3 {
		return nil, fmt.Errorf("path too short")
	}

	// Find the operation (manifests, blobs, tags)
	// Repo can have slashes, so we need to find where it ends
	var opIdx int
	var op string
	for i := 1; i < len(parts); i++ {
		if parts[i] == "manifests" || parts[i] == "blobs" || parts[i] == "tags" {
			opIdx = i
			op = parts[i]
			break
		}
	}

	if op == "" {
		return nil, fmt.Errorf("no valid operation found (manifests/blobs/tags)")
	}

	// Everything between v2 and the operation is the repo
	repo := strings.Join(parts[1:opIdx], "/")
	if repo == "" {
		return nil, fmt.Errorf("empty repository name")
	}

	result := &RegistryPath{
		Repo: repo,
	}

	// Parse based on operation type
	switch op {
	case "manifests":
		// /v2/<repo>/manifests/<reference>
		if len(parts) <= opIdx+1 {
			return nil, fmt.Errorf("manifests path missing reference")
		}
		result.Type = PathTypeManifest
		result.Reference = strings.Join(parts[opIdx+1:], "/")

	case "blobs":
		// /v2/<repo>/blobs/<digest>
		// /v2/<repo>/blobs/uploads/
		// /v2/<repo>/blobs/uploads/<uuid>
		if len(parts) <= opIdx+1 {
			return nil, fmt.Errorf("blobs path missing subpath")
		}

		if parts[opIdx+1] == "uploads" {
			if len(parts) == opIdx+2 {
				// /v2/<repo>/blobs/uploads/
				result.Type = PathTypeBlobUploadInit
			} else {
				// /v2/<repo>/blobs/uploads/<uuid>
				result.Type = PathTypeBlobUpload
				result.Reference = parts[opIdx+2]
			}
		} else {
			// /v2/<repo>/blobs/<digest>
			result.Type = PathTypeBlob
			result.Reference = parts[opIdx+1]
		}

	case "tags":
		// /v2/<repo>/tags/list
		if len(parts) <= opIdx+1 || parts[opIdx+1] != "list" {
			return nil, fmt.Errorf("tags path must be tags/list")
		}
		result.Type = PathTypeTagsList

	default:
		return nil, fmt.Errorf("unknown operation: %s", op)
	}

	return result, nil
}

// setupRoutes configures all OCI Distribution Spec routes.
func (r *Registry) setupRoutes() {
	r.mux.HandleFunc("/v2", r.handleAPIVersion)
	r.mux.HandleFunc("/v2/_catalog", r.handleCatalog)
	r.mux.HandleFunc("/v2/", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v2/" {
			r.handleAPIVersion(w, req)
			return
		}
		result, err := ParseRegistryPath(req.URL.Path)
		if err != nil {
			r.vlog("ParseRegistryPath(%s) error: %v", req.URL.Path, err)
			http.NotFound(w, req)
			return
		}
		r.vlog("%s result: %+v", req.URL.Path, result)
		switch result.Type {
		case PathTypeManifest:
			r.handleManifest(w, req, result.Repo, result.Reference)
		case PathTypeBlob:
			r.handleBlob(w, req, result.Repo, result.Reference)
		case PathTypeBlobUploadInit:
			r.handleBlobUploadInitiate(w, req, result.Repo)
		case PathTypeBlobUpload:
			r.handleBlobUpload(w, req, result.Repo, result.Reference)
		case PathTypeTagsList:
			http.NotFound(w, req)
		default:
			log.Println("unknown path type", result.Type)
		}
	})
}

// handleCatalog handles the /v2/_catalog endpoint.
func (r *Registry) handleCatalog(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrCodeUnsupported, "method not allowed", nil)
		return
	}
	WriteError(w, http.StatusNotImplemented, ErrCodeUnsupported, "not implemented", nil)
}

// ServeHTTP implements http.Handler for the registry.
func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

// handleAPIVersion handles the /v2/ endpoint (OCI API version check).
func (r *Registry) handleAPIVersion(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrCodeUnsupported, "method not allowed", nil)
		return
	}

	// OCI spec: return 200 OK with Docker-Distribution-API-Version header
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(http.StatusOK)
}

// handleManifest handles manifest operations.
func (r *Registry) handleManifest(w http.ResponseWriter, req *http.Request, repo, reference string) {
	switch req.Method {
	case http.MethodGet:
		r.handleManifestGet(w, req, repo, reference)
	case http.MethodHead:
		r.handleManifestHead(w, req, repo, reference)
	case http.MethodPut:
		r.handleManifestPut(w, req, repo, reference)
	case http.MethodDelete:
		r.handleManifestDelete(w, req, repo, reference)
	default:
		WriteError(w, http.StatusMethodNotAllowed, ErrCodeUnsupported, "method not allowed", nil)
	}
}

const verbose = false

func (r *Registry) vlog(format string, args ...any) {
	if verbose {
		log.Printf(format, args...)
	}
}

// handleManifestGet retrieves a manifest.
func (r *Registry) handleManifestGet(w http.ResponseWriter, req *http.Request, repo, reference string) {
	r.vlog("handleManifestGet %s %s", repo, reference)
	mf, err := r.storage.GetManifest(req.Context(), repo, reference)
	if err != nil {
		r.vlog("handleManifestGet error: %v", err)
		if errors.Is(err, ErrManifestNotFound) {
			WriteError(w, http.StatusNotFound, ErrCodeManifestUnknown, "manifest not found", nil)
			return
		}
		WriteError(w, http.StatusInternalServerError, ErrCodeManifestInvalid, err.Error(), nil)
		return
	}
	defer mf.Data.Close()

	// Set required headers
	if mf.MediaType != "" {
		w.Header().Set("Content-Type", mf.MediaType)
	} else {
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	}
	w.Header().Set("Docker-Content-Digest", mf.Digest)
	w.Header().Set("Content-Length", strconv.Itoa(int(mf.Size)))

	// Check for compression support
	acceptEncoding := req.Header.Get("Accept-Encoding")
	encoding := compress.SelectEncoding(acceptEncoding)

	if encoding != "" {
		// Use compression
		cw, err := compress.NewResponseWriter(w, encoding)
		if err != nil {
			// Fall back to uncompressed on error
			w.WriteHeader(http.StatusOK)
			io.Copy(w, mf.Data)
			return
		}
		defer cw.Close()

		w = cw
	}

	w.WriteHeader(http.StatusOK)
	io.Copy(w, mf.Data)
}

// handleManifestHead checks if a manifest exists.
func (r *Registry) handleManifestHead(w http.ResponseWriter, req *http.Request, repo, reference string) {
	r.vlog("handleManifestHead %s %s", repo, reference)
	mf, err := r.storage.GetManifest(req.Context(), repo, reference)
	if err != nil {
		r.vlog("handleManifestHead error: %v", err)
		if errors.Is(err, ErrManifestNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer mf.Data.Close()

	// Set required headers
	if mf.MediaType != "" {
		w.Header().Set("Content-Type", mf.MediaType)
	} else {
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	}
	w.Header().Set("Docker-Content-Digest", mf.Digest)
	w.Header().Set("Content-Length", strconv.Itoa(int(mf.Size)))

	w.WriteHeader(http.StatusOK)
}

func computeDigest(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// handleManifestPut uploads a manifest.
func (r *Registry) handleManifestPut(w http.ResponseWriter, req *http.Request, repo, reference string) {
	r.vlog("handleManifestPut %s %s", repo, reference)

	// Decompress request body if needed
	if err := compress.DecompressRequest(req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrCodeManifestInvalid,
			"failed to decompress request body", nil)
		return
	}

	// Read manifest data
	data, err := io.ReadAll(req.Body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, ErrCodeManifestInvalid, "failed to read manifest", nil)
		return
	}
	req.Body.Close()

	// Get media type from Content-Type header
	mediaType := req.Header.Get("Content-Type")
	if mediaType == "" {
		mediaType = "application/vnd.oci.image.manifest.v1+json"
	}

	// Validate it's valid JSON
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		WriteError(w, http.StatusBadRequest, ErrCodeManifestInvalid, "invalid JSON", nil)
		return
	}

	// Validate mediaType field matches Content-Type header if present
	if manifestMediaType, ok := manifest["mediaType"].(string); ok {
		if manifestMediaType != mediaType {
			WriteError(w, http.StatusBadRequest, ErrCodeManifestInvalid,
				"manifest mediaType does not match Content-Type header", nil)
			return
		}
	}

	// Store manifest
	digest, err := r.storage.PutManifest(req.Context(), repo, reference, data, mediaType)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrCodeManifestInvalid, err.Error(), nil)
		return
	}

	// Set response headers
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", repo, digest))

	// OCI spec: check for subject field and set OCI-Subject header if present
	if subject, ok := manifest["subject"].(map[string]any); ok {
		if subjectDigest, ok := subject["digest"].(string); ok {
			w.Header().Set("OCI-Subject", subjectDigest)
		}
	}

	w.WriteHeader(http.StatusCreated)
}

// handleManifestDelete deletes a manifest.
func (r *Registry) handleManifestDelete(w http.ResponseWriter, req *http.Request, repo, reference string) {
	if err := r.storage.DeleteManifest(req.Context(), repo, reference); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrCodeManifestInvalid, err.Error(), nil)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// handleBlob handles blob operations.
func (r *Registry) handleBlob(w http.ResponseWriter, req *http.Request, repo, digest string) {
	switch req.Method {
	case http.MethodGet:
		r.handleBlobGet(w, req, repo, digest)
	case http.MethodHead:
		r.handleBlobHead(w, req, repo, digest)
	case http.MethodDelete:
		r.handleBlobDelete(w, req, repo, digest)
	default:
		WriteError(w, http.StatusMethodNotAllowed, ErrCodeUnsupported, "method not allowed", nil)
	}
}

// handleBlobGet retrieves a blob.
func (r *Registry) handleBlobGet(w http.ResponseWriter, req *http.Request, repo, digest string) {
	rc, err := r.storage.GetBlob(req.Context(), digest)
	if err != nil {
		if err == ErrBlobNotFound {
			WriteError(w, http.StatusNotFound, ErrCodeBlobUnknown, "blob not found", nil)
			return
		}
		WriteError(w, http.StatusInternalServerError, ErrCodeBlobUnknown, err.Error(), nil)
		return
	}
	defer rc.Close()
	size, sizeErr := r.storage.BlobSize(req.Context(), digest)

	// Set required headers
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest)

	// Check for compression support
	acceptEncoding := req.Header.Get("Accept-Encoding")
	encoding := compress.SelectEncoding(acceptEncoding)

	if encoding != "" {
		// Use compression
		cw, err := compress.NewResponseWriter(w, encoding)
		if err != nil {
			// Fall back to uncompressed on error
			w.WriteHeader(http.StatusOK)
			io.Copy(w, rc)
			return
		}
		defer cw.Close()

		w = cw
	} else if sizeErr == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}

	w.WriteHeader(http.StatusOK)
	io.Copy(w, rc)
}

// handleBlobHead checks if a blob exists.
func (r *Registry) handleBlobHead(w http.ResponseWriter, req *http.Request, repo, digest string) {
	size, err := r.storage.BlobSize(req.Context(), digest)
	if err != nil {
		if errors.Is(err, ErrBlobNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Set required headers
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))

	w.WriteHeader(http.StatusOK)
}

// handleBlobDelete deletes a blob.
func (r *Registry) handleBlobDelete(w http.ResponseWriter, req *http.Request, repo, digest string) {
	if err := r.storage.DeleteBlob(req.Context(), digest); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrCodeBlobUnknown, err.Error(), nil)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// handleBlobUploadInitiate initiates a blob upload.
func (r *Registry) handleBlobUploadInitiate(w http.ResponseWriter, req *http.Request, repo string) {
	if req.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrCodeUnsupported, "method not allowed", nil)
		return
	}

	// Check for cross-repository blob mount (OCI spec)
	mountDigest := req.URL.Query().Get("mount")
	fromRepo := req.URL.Query().Get("from")

	if mountDigest != "" && fromRepo != "" {
		// Attempt to mount blob from another repository
		if r.storage.BlobExists(req.Context(), mountDigest) {
			// Mount successful
			if !strings.HasPrefix(mountDigest, "sha256:") {
				mountDigest = "sha256:" + mountDigest
			}
			w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", repo, mountDigest))
			w.Header().Set("Docker-Content-Digest", mountDigest)
			w.WriteHeader(http.StatusCreated)
			return
		}
		// Fall through to create upload session if mount fails
	}

	// Create new upload session
	session, err := r.storage.NewUpload(req.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrCodeBlobUploadInvalid, err.Error(), nil)
		return
	}

	// Return upload URL
	uploadURL := fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, session.UUID)
	w.Header().Set("Location", uploadURL)
	w.Header().Set("Range", "0-0")
	w.Header().Set("Docker-Upload-UUID", session.UUID)
	w.WriteHeader(http.StatusAccepted)
}

// handleBlobUpload handles an ongoing blob upload.
func (r *Registry) handleBlobUpload(w http.ResponseWriter, req *http.Request, repo, uploadUUID string) {
	switch req.Method {
	case http.MethodPatch:
		r.handleBlobUploadChunk(w, req, repo, uploadUUID)
	case http.MethodPut:
		r.handleBlobUploadComplete(w, req, repo, uploadUUID)
	case http.MethodGet:
		r.handleBlobUploadStatus(w, req, repo, uploadUUID)
	case http.MethodDelete:
		r.handleBlobUploadCancel(w, req, repo, uploadUUID)
	default:
		WriteError(w, http.StatusMethodNotAllowed, ErrCodeUnsupported, "method not allowed", nil)
	}
}

// handleBlobUploadChunk handles chunked upload.
func (r *Registry) handleBlobUploadChunk(w http.ResponseWriter, req *http.Request, repo, uploadUUID string) {
	// Decompress request body if needed
	if err := compress.DecompressRequest(req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrCodeBlobUploadInvalid,
			"failed to decompress request body", nil)
		return
	}

	session, err := r.storage.CopyChunk(req.Context(), uploadUUID, req.Body)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrCodeBlobUploadInvalid, "failed to write chunk", nil)
		return
	}

	// Return current status
	uploadURL := fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, uploadUUID)
	w.Header().Set("Location", uploadURL)
	w.Header().Set("Range", fmt.Sprintf("0-%d", session.Written-1))
	w.Header().Set("Docker-Upload-UUID", uploadUUID)
	w.WriteHeader(http.StatusAccepted)
}

// handleBlobUploadComplete completes a blob upload.
func (r *Registry) handleBlobUploadComplete(w http.ResponseWriter, req *http.Request, repo, uploadUUID string) {
	// Decompress request body if needed
	if err := compress.DecompressRequest(req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrCodeBlobUploadInvalid,
			"failed to decompress request body", nil)
		return
	}

	// Get expected digest from query parameter
	expectedDigest := req.URL.Query().Get("digest")
	if expectedDigest == "" {
		WriteError(w, http.StatusBadRequest, ErrCodeDigestInvalid, "digest parameter required", nil)
		return
	}

	if _, err := r.storage.CopyChunk(req.Context(), uploadUUID, req.Body); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrCodeBlobUploadInvalid, "failed to copy chunk", nil)
		return
	}

	// Store blob
	digest, err := r.storage.CompleteUpload(req.Context(), uploadUUID, expectedDigest)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrCodeBlobUploadInvalid, err.Error(), nil)
		return
	}

	// Return success
	blobURL := fmt.Sprintf("/v2/%s/blobs/%s", repo, digest)
	w.Header().Set("Location", blobURL)
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusCreated)
}

// handleBlobUploadStatus returns the status of an upload.
func (r *Registry) handleBlobUploadStatus(w http.ResponseWriter, req *http.Request, repo, uploadUUID string) {
	session, err := r.storage.GetUpload(req.Context(), uploadUUID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrCodeBlobUploadUnknown, "upload not found", nil)
		return
	}

	uploadURL := fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, uploadUUID)
	w.Header().Set("Location", uploadURL)
	if n := session.Written; n > 0 {
		w.Header().Set("Range", fmt.Sprintf("0-%d", n-1))
	} else {
		w.Header().Set("Range", "0-0")
	}
	w.Header().Set("Docker-Upload-UUID", uploadUUID)
	w.WriteHeader(http.StatusNoContent)
}

// handleBlobUploadCancel cancels an upload.
func (r *Registry) handleBlobUploadCancel(w http.ResponseWriter, req *http.Request, repo, uploadUUID string) {
	if err := r.storage.AbortUpload(req.Context(), uploadUUID); err != nil {
		WriteError(w, http.StatusNotFound, ErrCodeBlobUploadUnknown, "upload not found", nil)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// NewHandler creates a new HTTP handler for the registry.
func NewHandler(storage Storage) http.Handler {
	return New(storage)
}

// ListenAndServe starts the registry HTTP server.
func ListenAndServe(addr string, storage Storage) error {
	registry := New(storage)
	return http.ListenAndServe(addr, registry)
}

// BasePath returns the base path for registry URLs.
func BasePath() string {
	return "/v2"
}

// ManifestPath returns the path for a manifest.
func ManifestPath(repo, reference string) string {
	return path.Join(BasePath(), repo, "manifests", reference)
}

// BlobPath returns the path for a blob.
func BlobPath(repo, digest string) string {
	return path.Join(BasePath(), repo, "blobs", digest)
}
