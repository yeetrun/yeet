# Tailscale OAuth Init Plan

## Goal

Make first-run `yeet init root@host` use a user-friendly Tailscale OAuth setup flow for catch:

- Explain that catch is the small host daemon installed by yeet.
- Do not emit or rely on a browser login URL.
- Require catch hosts to be tagged.
- Prefer a single Tailscale OAuth client secret that can mint reusable auth keys for the required tags.
- If Tailscale rejects the requested catch tag, prompt for a corrected OAuth client secret and retry in interactive mode.
- Keep non-interactive mode deterministic with actionable errors.
- Update public docs/help so users know what OAuth client permissions and ACL tag owner policy are required.

## Constraints

- Work in `/Users/shayne/.config/superpowers/worktrees/yeet/codex-tailscale-oauth-init`.
- Use tests first for behavior changes.
- Do not commit, tag, or push unless explicitly authorized in this turn. The repository policy requires explicit authorization for those actions.
- Keep `TS_AUTHKEY` support for advanced users and CI.
- Do not weaken the tagged catch requirement.
- Avoid logging secrets. Existing `tskey-...` redaction must be extended to OAuth client secrets where output can include them.

## Architecture

Use the catch binary as the authority for Tailscale setup:

1. The local `yeet init` command prompts for or accepts a Tailscale OAuth client secret.
2. The local client passes `TS_CLIENT_SECRET` and `TS_CATCH_TAGS=tag:catch` to the remote `catch install` command.
3. `catch install` uses the OAuth client secret to mint a reusable preauthorized Tailscale auth key with `tag:catch`.
4. The generated auth key is used for the initial `tsnet` login, and the OAuth client secret is stored in catch's service data dir so later Tailscale service networking can reuse it.
5. If the remote install fails because the OAuth secret cannot mint the requested tag, the local init loop prompts for another OAuth secret and retries the install command.
6. If no existing Tailscale state exists and neither `TS_AUTHKEY` nor `TS_CLIENT_SECRET` is provided, catch fails fast with an actionable error. It does not let tsnet print a browser login URL.

## Implementation Tasks

### 1. Add init option parsing and prompt tests

Add tests in `pkg/yeet/init_test.go`.

Expected parsing:

```go
func TestParseInitArgsTailscaleClientSecret(t *testing.T) {
	got, err := parseInitArgs([]string{"root@example.com", "--ts-client-secret=tskey-client-abc"})
	if err != nil {
		t.Fatalf("parseInitArgs returned error: %v", err)
	}
	if got.TSClientSecret != "tskey-client-abc" {
		t.Fatalf("TSClientSecret = %q", got.TSClientSecret)
	}
}

func TestParseInitArgsRejectsAuthKeyAndClientSecret(t *testing.T) {
	_, err := parseInitArgs([]string{
		"root@example.com",
		"--ts-auth-key=tskey-auth-abc",
		"--ts-client-secret=tskey-client-abc",
	})
	if err == nil || !strings.Contains(err.Error(), "--ts-auth-key and --ts-client-secret cannot be used together") {
		t.Fatalf("expected mutual exclusion error, got %v", err)
	}
}
```

Expected prompt helper:

```go
func TestResolveInitTailscaleAuthPromptsForOAuthSecret(t *testing.T) {
	var asked bool
	got, err := resolveInitTailscaleAuth(initTailscaleAuthOptions{
		Interactive: true,
		PromptSecret: func() (string, error) {
			asked = true
			return "tskey-client-test", nil
		},
	})
	if err != nil {
		t.Fatalf("resolveInitTailscaleAuth returned error: %v", err)
	}
	if !asked {
		t.Fatalf("expected OAuth prompt")
	}
	if got.ClientSecret != "tskey-client-test" {
		t.Fatalf("ClientSecret = %q", got.ClientSecret)
	}
}

func TestResolveInitTailscaleAuthNonInteractiveRequiresCredential(t *testing.T) {
	_, err := resolveInitTailscaleAuth(initTailscaleAuthOptions{Interactive: false})
	if err == nil || !strings.Contains(err.Error(), "pass --ts-client-secret") {
		t.Fatalf("expected non-interactive credential error, got %v", err)
	}
}
```

Implement:

- Add `TSClientSecret string` to `initFlagsParsed` and `initOptions`.
- Add `--ts-client-secret` to `cmd/yeet/cli.go`.
- Reject `--ts-auth-key` plus `--ts-client-secret`.
- Add `initTailscaleAuthOptions`, `initTailscaleAuth`, and `resolveInitTailscaleAuth`.
- Prompt text:
  - "yeet installs catch, a small daemon that runs on the host and manages services."
  - "catch must join Tailscale as a tagged device, usually tag:catch."
  - "Paste a Tailscale OAuth client secret with device auth key write access for tag:catch."
  - "Docs: https://yeetrun.com/docs/concepts/tailscale"

### 2. Pass OAuth secret and catch tags through init install

Add tests in `pkg/yeet/init_test.go` for env construction:

```go
func TestRemoteCatchInstallCommandIncludesTailscaleOAuth(t *testing.T) {
	got := remoteCatchInstallCommand(catchInstallCommandOptions{
		Path:             "/tmp/catch",
		UseSudo:          true,
		InstallDocker:    true,
		InstallVMTools:   true,
		TSClientSecret:   "tskey-client-test",
		TSCatchTags:      []string{"tag:catch"},
	})
	for _, want := range []string{
		"TS_CLIENT_SECRET=tskey-client-test",
		"TS_CATCH_TAGS=tag:catch",
		"sudo -E /tmp/catch install",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("command missing %q:\n%s", want, got)
		}
	}
}
```

Implement:

- Introduce `catchInstallCommandOptions` if the function still has too many arguments.
- Add `TSClientSecret` and `TSCatchTags`.
- Keep `TS_AUTHKEY` behavior intact.
- Default `TSCatchTags` to `[]string{"tag:catch"}` when OAuth is used.
- Thread the new fields through:
  - `initCatch`
  - `installInitCatchFn`
  - `installInitCatch`
  - `installInitCatchDirect`
  - `installInitCatchDetached`
  - `remoteCatchInstallCommand`

### 3. Make init retry on Tailscale OAuth/tag rejection

Add tests for install retry with a fake installer:

```go
func TestInitInstallRetriesAfterTailscaleOAuthRejection(t *testing.T) {
	var installs int
	secrets := []string{"bad-secret", "good-secret"}
	var prompts int
	deps := initCatchDeps{
		Install: func(ui *initUI, remote string, useSudo bool, installDocker bool, installVMTools bool, tsAuthKey string, tsClientSecret string, tsCatchTags []string) error {
			installs++
			if tsClientSecret == "bad-secret" {
				return errTailscaleOAuthRejected
			}
			if tsClientSecret != "good-secret" {
				t.Fatalf("unexpected secret %q", tsClientSecret)
			}
			return nil
		},
		PromptTSClientSecret: func() (string, error) {
			secret := secrets[prompts]
			prompts++
			return secret, nil
		},
	}
	// call the focused helper that wraps install retry
	err := runInitInstallWithTailscaleRetry(context.Background(), deps, initInstallRequest{Interactive: true})
	if err != nil {
		t.Fatalf("runInitInstallWithTailscaleRetry returned error: %v", err)
	}
	if installs != 2 || prompts != 2 {
		t.Fatalf("installs=%d prompts=%d", installs, prompts)
	}
}
```

Implement:

- Add a sentinel or classifier:

```go
var errTailscaleOAuthRejected = errors.New("tailscale oauth setup rejected")

func isTailscaleOAuthRejected(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, errTailscaleOAuthRejected) ||
		strings.Contains(err.Error(), "tailscale oauth setup rejected") ||
		strings.Contains(err.Error(), "Tailscale OAuth setup failed")
}
```

- Wrap only the install command in the retry loop after catch has been built/uploaded.
- On interactive rejection: show one concise line, prompt again, retry.
- On non-interactive rejection: return the original actionable error.
- Cap retries at 3.

### 4. Surface remote Tailscale install errors through the output filter

Add tests in `pkg/yeet/init_install_filter_test.go`:

```go
func TestInitInstallFilterCapturesTailscaleOAuthError(t *testing.T) {
	filter := newInitInstallFilter(io.Discard)
	_, _ = filter.Write([]byte("Tailscale OAuth setup failed: tag:catch is not allowed by tagOwners\n"))
	err := filter.ErrorSummary()
	if err == nil || !strings.Contains(err.Error(), "Tailscale OAuth setup failed") {
		t.Fatalf("expected captured OAuth error, got %v", err)
	}
}

func TestInitInstallFilterRedactsTailscaleClientSecret(t *testing.T) {
	var out bytes.Buffer
	filter := newInitInstallFilter(&out)
	_, _ = filter.Write([]byte("TS_CLIENT_SECRET=tskey-client-secret-example\n"))
	if strings.Contains(out.String(), "tskey-client-secret-example") {
		t.Fatalf("secret was not redacted: %q", out.String())
	}
}
```

Implement:

- Redact strings matching `tskey-client-[A-Za-z0-9_-]+` in addition to auth keys.
- Track important install errors in the filter.
- `installInitCatchDirect` and `installInitCatchDetached` should return `filter.ErrorSummary()` when the remote process exits non-zero and a summary exists.

### 5. Add catch-side OAuth auth key generation and storage

Add tests in `pkg/catch/tsns_test.go` and `pkg/catch/tailscale_setup_test.go`:

```go
func TestGenerateTailscaleAuthKeyFromSecretUsesTags(t *testing.T) {
	// Use httptest server and fake Tailscale API response.
	// Assert request contains "tag:catch" and Preauthorized true.
}

func TestWriteCatchTailscaleClientSecret(t *testing.T) {
	root := t.TempDir()
	path, err := WriteCatchTailscaleClientSecret(root, "tskey-client-test")
	if err != nil {
		t.Fatalf("WriteCatchTailscaleClientSecret returned error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(got) != "tskey-client-test" {
		t.Fatalf("stored secret = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
}
```

Implement:

- Factor `pkg/catch/tsns.go` into:

```go
func GenerateTailscaleAuthKeyFromSecret(ctx context.Context, clientSecret string, tags []string) (string, error)
```

- Keep `generateTailscaleAuthKey` using the stored catch secret.
- Add:

```go
func WriteCatchTailscaleClientSecret(rootDir string, clientSecret string) (string, error)
```

that writes to the same path used by `catch.TailscaleSetup`.

### 6. Prevent browser-login fallback in catch install

Add tests in `cmd/catch/catch_test.go`:

```go
func TestInitTSNetFailsWithoutStateOrCredential(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TS_AUTHKEY", "")
	t.Setenv("TS_CLIENT_SECRET", "")
	_, err := initTSNet(dir)
	if err == nil || !strings.Contains(err.Error(), "requires a Tailscale OAuth client secret or auth key") {
		t.Fatalf("expected credential error, got %v", err)
	}
}
```

For this test, isolate the credential/state check into a pure helper to avoid starting real tsnet:

```go
func validateTSNetInstallAuth(dataDir string, tsAuthKey string, tsClientSecret string) error
```

Implement:

- `validateTSNetInstallAuth` checks for existing tsnet state under the catch data dir.
- If there is existing state, allow `tsnet.Up`.
- If no state and no auth key/client secret, return actionable error and do not call `tsnet.Up`.
- If `TS_CLIENT_SECRET` is provided:
  - parse `TS_CATCH_TAGS`, default `tag:catch`;
  - call `catch.GenerateTailscaleAuthKeyFromSecret`;
  - write the OAuth secret to catch data;
  - set generated auth key on `tsnet.Server` or in env for `newTSNetServer`.
- Error message for tag rejection starts with `Tailscale OAuth setup failed:` so the local CLI can classify it.

### 7. Update docs and help

Update:

- `cmd/yeet/cli.go`
- generated `.codex/skills/yeet-cli/references/yeet-help-llm.md`
- `README.md`
- `website/docs/concepts/tailscale.mdx`
- `website/docs/getting-started/installation.mdx`
- `website/docs/getting-started/first-run-validation.mdx`
- `website/docs/cli/yeet-cli.mdx`
- `website/docs/operations/troubleshooting.mdx`

Required public docs content:

- catch explanation: "catch is the small daemon yeet installs on each host."
- Tailscale OAuth setup:
  - Create an OAuth client.
  - Grant device auth key write permissions.
  - Allow it to generate keys for `tag:catch`.
  - Ensure ACL `tagOwners` permits the OAuth client to own `tag:catch`.
  - Run `yeet init root@host` and paste the client secret when prompted, or pass `--ts-client-secret`.
- Mention `--ts-auth-key` as advanced/manual path.
- Remove browser-login URL language from init setup docs.

Run:

```bash
mise exec -- tools/generate-yeet-help-llm.sh
```

### 8. Verification

Run targeted tests:

```bash
mise exec -- go test ./pkg/yeet ./cmd/catch ./pkg/catch -count=1
```

Run full tests:

```bash
mise exec -- go test ./... -count=1
```

Run deterministic gate if scope remains code/docs only:

```bash
mise exec -- pre-commit run --all-files
```

If docs or generated help changed, check submodule status:

```bash
git status --short
git -C website status --short
```

## Manual Review Checklist

- `yeet init root@host` has a concise OAuth prompt and no browser URL.
- `yeet init root@host --ts-client-secret=...` is non-interactive for Tailscale setup.
- `yeet init root@host --ts-auth-key=...` still works.
- Catch install refuses first login without auth key/OAuth secret.
- Catch install requires a tagged Tailscale node.
- Tag owner/OAuth rejection produces an actionable error and interactive retry.
- Docs describe one-key OAuth setup clearly from a public user perspective.
