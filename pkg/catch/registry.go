// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"os"
	"strings"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/registry"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/util/mak"
)

type registryInstaller interface {
	io.WriteCloser
	Fail()
}

type newRegistryInstaller func(*Server, FileInstallerCfg) (registryInstaller, error)

func (s *Server) newRegistry() *containerRegistry {
	base := s.cfg.RegistryStorage
	if base == nil {
		if s.cfg.ContainerdSocket == "" {
			log.Fatalf("containerd socket not configured; set --containerd-socket (default /run/containerd/containerd.sock)")
		}
		if _, err := os.Stat(s.cfg.ContainerdSocket); err != nil {
			log.Fatalf("containerd socket %q not found: %v", s.cfg.ContainerdSocket, err)
		}
		var err error
		base, err = registry.NewContainerdCacheStorage(s.cfg.ContainerdSocket)
		if err != nil {
			log.Fatalf("NewContainerdCacheStorage: %v", err)
		}
	}
	storage := &internalRegistryStorage{
		s:            s,
		base:         base,
		repoPrefix:   svc.InternalRegistryHost,
		newInstaller: defaultRegistryInstaller,
	}
	return &containerRegistry{
		s:       s,
		storage: storage,
		handler: registry.NewHandler(storage),
	}
}

func defaultRegistryInstaller(s *Server, cfg FileInstallerCfg) (registryInstaller, error) {
	return NewFileInstaller(s, cfg)
}

type containerRegistry struct {
	s       *Server
	storage *internalRegistryStorage
	handler http.Handler
}

func (cr *containerRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only allow read-only access to the registry from localhost.
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		log.Printf("ParseAddrPort: %v", err)
		http.Error(w, "Registry is read-only", http.StatusMethodNotAllowed)
		return
	}
	if ap.Addr().IsLoopback() {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			http.Error(w, "Registry is read-only", http.StatusMethodNotAllowed)
			return
		}
	} else {
		if err := cr.s.verifyCaller(r.Context(), r.RemoteAddr); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
	}
	cr.handler.ServeHTTP(w, r)
}

type internalRegistryStorage struct {
	s            *Server
	base         registry.Storage
	repoPrefix   string
	newInstaller newRegistryInstaller
}

func (s *internalRegistryStorage) storageRepo(repo string) string {
	if s.repoPrefix == "" {
		return repo
	}
	return fmt.Sprintf("%s/%s", s.repoPrefix, repo)
}

func (s *internalRegistryStorage) GetBlob(ctx context.Context, digest string) (io.ReadCloser, error) {
	return s.base.GetBlob(ctx, digest)
}

func (s *internalRegistryStorage) BlobSize(ctx context.Context, digest string) (int64, error) {
	return s.base.BlobSize(ctx, digest)
}

func (s *internalRegistryStorage) BlobExists(ctx context.Context, digest string) bool {
	return s.base.BlobExists(ctx, digest)
}

func (s *internalRegistryStorage) DeleteBlob(ctx context.Context, digest string) error {
	return s.base.DeleteBlob(ctx, digest)
}

func (s *internalRegistryStorage) GetManifest(ctx context.Context, repo, reference string) (*registry.ManifestMetadata, error) {
	if isDigest(reference) {
		return s.base.GetManifest(ctx, s.storageRepo(repo), reference)
	}
	manifest, ok, err := s.lookupManifest(repo, reference)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, registry.ErrManifestNotFound
	}
	return s.base.GetManifest(ctx, s.storageRepo(repo), manifest.BlobHash)
}

func (s *internalRegistryStorage) PutManifest(ctx context.Context, repo, reference string, data []byte, mediaType string) (string, error) {
	if isDigest(reference) {
		return s.base.PutManifest(ctx, s.storageRepo(repo), reference, data, mediaType)
	}
	svcName, err := parseRepo(repo)
	if err != nil {
		return "", err
	}
	refs, stageOnly, err := refsForTag(reference)
	if err != nil {
		return "", err
	}
	digest, err := s.base.PutManifest(ctx, s.storageRepo(repo), reference, data, mediaType)
	if err != nil {
		log.Printf("registry PutManifest failed for %q:%q: %v", repo, reference, err)
		return "", err
	}
	d, err := s.s.cfg.DB.MutateData(func(d *db.Data) error {
		ir, ok := d.Images[db.ImageRepoName(repo)]
		if !ok {
			ir = &db.ImageRepo{
				Refs: make(map[db.ImageRef]db.ImageManifest, len(refs)),
			}
			mak.Set(&d.Images, db.ImageRepoName(repo), ir)
		}
		for _, ref := range refs {
			ir.Refs[db.ImageRef(ref)] = db.ImageManifest{
				ContentType: mediaType,
				BlobHash:    digest,
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if err := s.stageCompose(d, svcName, repo, stageOnly); err != nil {
		log.Printf("registry install failed: %v", err)
	}
	return digest, nil
}

func (s *internalRegistryStorage) ManifestExists(ctx context.Context, repo, reference string) bool {
	if isDigest(reference) {
		return s.base.ManifestExists(ctx, s.storageRepo(repo), reference)
	}
	_, ok, err := s.lookupManifest(repo, reference)
	return err == nil && ok
}

func (s *internalRegistryStorage) DeleteManifest(ctx context.Context, repo, reference string) error {
	if isDigest(reference) {
		if err := s.base.DeleteManifest(ctx, s.storageRepo(repo), reference); err != nil {
			return err
		}
		_, err := s.s.cfg.DB.MutateData(func(d *db.Data) error {
			ir, ok := d.Images[db.ImageRepoName(repo)]
			if !ok {
				return nil
			}
			for ref, mf := range ir.Refs {
				if mf.BlobHash == reference {
					delete(ir.Refs, ref)
				}
			}
			return nil
		})
		return err
	}
	_, err := s.s.cfg.DB.MutateData(func(d *db.Data) error {
		ir, ok := d.Images[db.ImageRepoName(repo)]
		if !ok {
			return nil
		}
		delete(ir.Refs, db.ImageRef(reference))
		return nil
	})
	if err != nil {
		return err
	}
	_ = s.base.DeleteManifest(ctx, s.storageRepo(repo), reference)
	return nil
}

func (s *internalRegistryStorage) NewUpload(ctx context.Context) (*registry.UploadSession, error) {
	return s.base.NewUpload(ctx)
}

func (s *internalRegistryStorage) GetUpload(ctx context.Context, uuid string) (*registry.UploadSession, error) {
	return s.base.GetUpload(ctx, uuid)
}

func (s *internalRegistryStorage) CopyChunk(ctx context.Context, uuid string, r io.Reader) (*registry.UploadSession, error) {
	return s.base.CopyChunk(ctx, uuid, r)
}

func (s *internalRegistryStorage) CompleteUpload(ctx context.Context, uuid, expectedDigest string) (string, error) {
	return s.base.CompleteUpload(ctx, uuid, expectedDigest)
}

func (s *internalRegistryStorage) AbortUpload(ctx context.Context, uuid string) error {
	return s.base.AbortUpload(ctx, uuid)
}

func (s *internalRegistryStorage) lookupManifest(repo, reference string) (db.ImageManifest, bool, error) {
	dv, err := s.s.getDB()
	if err != nil {
		return db.ImageManifest{}, false, err
	}
	ir, ok := dv.Images().GetOk(db.ImageRepoName(repo))
	if !ok {
		return db.ImageManifest{}, false, nil
	}
	mf, ok := ir.Refs().GetOk(db.ImageRef(reference))
	if !ok {
		return db.ImageManifest{}, false, nil
	}
	return mf, true, nil
}

func (s *internalRegistryStorage) stageCompose(d *db.Data, svcName, repo string, stageOnly bool) error {
	if s.newInstaller == nil {
		return fmt.Errorf("registry installer missing")
	}
	inst, err := s.newInstaller(s.s, FileInstallerCfg{
		InstallerCfg: InstallerCfg{
			ServiceName: svcName,
			ClientOut:   io.Discard,
			Printer:     log.Printf,
		},
		PayloadName: "compose.yml",
		StageOnly:   stageOnly,
	})
	if err != nil {
		return fmt.Errorf("NewFileInstaller: %w", err)
	}

	// Check if previous generation compose file exists and copy it if found.
	var composeFile string
	if svc, ok := d.Services[svcName]; ok && svc.Generation > 0 {
		prevGen := svc.Generation - 1
		if prevFile, ok := svc.Artifacts.Gen(db.ArtifactDockerComposeFile, prevGen); ok {
			content, err := os.ReadFile(prevFile)
			if err != nil {
				inst.Fail()
				_ = inst.Close()
				return fmt.Errorf("failed to read previous generation compose file: %w", err)
			}
			composeFile = string(content)
		}
	}

	// If no previous file found or couldn't read it, use template.
	if composeFile == "" {
		image := fmt.Sprintf("%s/%s", svc.InternalRegistryHost, repo)
		composeFile = fmt.Sprintf(composeTemplate, svcName, image, s.s.serviceDataDir(svcName))
	}

	if _, err := io.Copy(inst, strings.NewReader(composeFile)); err != nil {
		inst.Fail()
		_ = inst.Close()
		return fmt.Errorf("failed to write compose file: %w", err)
	}
	if err := inst.Close(); err != nil {
		return fmt.Errorf("failed to close installer: %w", err)
	}
	return nil
}

func parseRepo(repo string) (string, error) {
	if repo == "" {
		return "", fmt.Errorf("invalid repo: empty")
	}
	if strings.Count(repo, "/") > 1 {
		return "", fmt.Errorf("invalid repo: %q", repo)
	}
	svcName, container, ok := strings.Cut(repo, "/")
	if !ok {
		return svcName, nil
	}
	if svcName == "" || container == "" || strings.Contains(container, "/") {
		return "", fmt.Errorf("invalid repo: %q", repo)
	}
	return svcName, nil
}

func refsForTag(tag string) ([]string, bool, error) {
	switch tag {
	case "run":
		return []string{"run", "staged"}, false, nil
	case "latest":
		return []string{"staged"}, true, nil
	default:
		return nil, false, fmt.Errorf("invalid tag: %q", tag)
	}
}

func isDigest(reference string) bool {
	return strings.HasPrefix(reference, "sha256:")
}

const composeTemplate = `services:
  %s:
    image: %s
    restart: unless-stopped
    volumes:
      - %s:/data
`
