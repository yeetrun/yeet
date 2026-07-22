# VM Runtime Status Human Output Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the unusably wide VM runtime status table with a readable single-VM detail view and compact fleet overview while preserving exact JSON output.

**Architecture:** Keep `vmRuntimeStatusRow` as the authoritative data model and split only the human rendering path. Pass explicit detail-versus-fleet intent from the command, format bounded human labels in pure helpers, and retain the existing JSON encoders without changing their schema or values.

**Tech Stack:** Go, `text/tabwriter`, Go's `testing` package, GitButler, repository-managed `mise` tasks.

## Global Constraints

- Human output must answer running, configured, staged, policy, state, and operator-action questions without printing complete storage identities.
- `--format=json` and `--format=json-pretty` must retain the same schema and exact field values.
- An explicitly selected VM gets detail output; an unfiltered command gets fleet output even when only one VM exists.
- The representative fleet table must stay within 100 columns for ordinary service names and supported runtime versions.
- Full SHA-256 values stay out of human output; single-VM detail may show a twelve-character manifest fingerprint.
- Missing live identity renders as `unverified`; configured state must never be substituted for proof of what is running.
- Unknown identity shapes must use bounded deterministic fallbacks instead of failing the read path.
- CLI syntax, read permission, runtime transitions, restart behavior, and database state remain unchanged.
- Use GitButler for commits and do not push or land without explicit user authorization.

---

## File Structure

- Modify `pkg/catch/vm_runtime_status.go`: carry render intent, provide pure human-label helpers, and own detail/fleet rendering.
- Modify `pkg/catch/vm_runtime_status_test.go`: add red/green renderer tests and update old wide-table expectations.
- Keep `docs/superpowers/specs/2026-07-22-vm-runtime-status-human-output-design.md` unchanged unless implementation reveals a contradiction.

No new package or dependency is needed. The renderer already lives beside the status model, and splitting one small rendering path into another file would add navigation without creating a useful boundary.

---

### Task 1: Carry Explicit Detail-versus-Fleet Intent

**Files:**
- Modify: `pkg/catch/vm_runtime_status.go:103-109,599-626`
- Test: `pkg/catch/vm_runtime_status_test.go:52-89,389-403`

**Interfaces:**
- Produces: `type vmRuntimeStatusView uint8`
- Produces: `vmRuntimeStatusFleetView` and `vmRuntimeStatusDetailView`
- Changes: `renderVMRuntimeStatus(io.Writer, string, []vmRuntimeStatusRow, vmRuntimeStatusView) error`
- Preserves: JSON encoding of `[]vmRuntimeStatusRow`

- [ ] **Step 1: Write a failing test proving human view intent does not alter JSON**

Add this test near the existing rendering tests:

```go
func TestVMRuntimeStatusJSONIgnoresHumanView(t *testing.T) {
	row := vmRuntimeStatusRow{
		Service: "devbox",
		Configured: vmRuntimeStatusIdentity{
			ID:                "firecracker-v1.16.1-yeet-v1",
			ManifestSHA256:    strings.Repeat("1", 64),
			FirecrackerSHA256: strings.Repeat("2", 64),
			JailerSHA256:      strings.Repeat("3", 64),
			Source:            "official",
			UpstreamVersion:   "v1.16.1",
			Support:           "supported",
		},
	}

	var fleet, detail bytes.Buffer
	if err := renderVMRuntimeStatus(&fleet, "json", []vmRuntimeStatusRow{row}, vmRuntimeStatusFleetView); err != nil {
		t.Fatal(err)
	}
	if err := renderVMRuntimeStatus(&detail, "json", []vmRuntimeStatusRow{row}, vmRuntimeStatusDetailView); err != nil {
		t.Fatal(err)
	}
	if fleet.String() != detail.String() {
		t.Fatalf("JSON changed with human view:\nfleet: %s\ndetail: %s", fleet.String(), detail.String())
	}
	for _, exact := range []string{row.Configured.ID, row.Configured.ManifestSHA256, row.Configured.FirecrackerSHA256, row.Configured.JailerSHA256} {
		if !strings.Contains(fleet.String(), exact) {
			t.Fatalf("JSON omitted exact identity %q: %s", exact, fleet.String())
		}
	}
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
mise exec -- go test ./pkg/catch -run '^TestVMRuntimeStatusJSONIgnoresHumanView$' -count=1
```

Expected: compilation fails because `vmRuntimeStatusFleetView`, `vmRuntimeStatusDetailView`, and the new render argument do not exist.

- [ ] **Step 3: Add render intent and route command intent explicitly**

Add above `printVMRuntimeStatus`:

```go
type vmRuntimeStatusView uint8

const (
	vmRuntimeStatusFleetView vmRuntimeStatusView = iota
	vmRuntimeStatusDetailView
)
```

Change `printVMRuntimeStatus` to:

```go
func (s *Server) printVMRuntimeStatus(ctx context.Context, w io.Writer, serviceName, format string) error {
	rows, err := s.vmRuntimeStatusRows(ctx, serviceName)
	if err != nil {
		return err
	}
	view := vmRuntimeStatusFleetView
	if serviceName != "" {
		view = vmRuntimeStatusDetailView
	}
	return renderVMRuntimeStatus(w, format, rows, view)
}
```

Change the renderer signature, leaving the current table body in place temporarily:

```go
func renderVMRuntimeStatus(w io.Writer, format string, rows []vmRuntimeStatusRow, view vmRuntimeStatusView) error {
	_ = view
	switch format {
	case "json":
		return json.NewEncoder(w).Encode(rows)
	case "json-pretty":
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	case "", "table":
		return renderLegacyVMRuntimeStatusTable(w, rows)
	default:
		return fmt.Errorf("unsupported VM runtime status format %q", format)
	}
}
```

Move the existing wide-table body, unchanged, into this temporary helper:

```go
func renderLegacyVMRuntimeStatusTable(w io.Writer, rows []vmRuntimeStatusRow) error {
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "SERVICE\tGUEST BASE\tKERNEL\tRUNNING\tCONFIGURED\tSTAGED\tPREVIOUS\tPOLICY\tCHANNEL\tPROMOTED\tJAILER\tSTATE\tACTION"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Service, vmComponentStatusIdentityDisplay(row.GuestBase), vmComponentStatusIdentityDisplay(row.Kernel),
			vmRuntimeStatusIdentityDisplay(row.Running), vmRuntimeStatusIdentityDisplay(&row.Configured),
			vmRuntimeStatusIdentityDisplay(row.Staged), vmRuntimeStatusIdentityDisplay(row.Previous),
			row.Policy, row.Channel, vmRuntimeStatusIdentityDisplay(row.LatestPromoted), row.JailerIsolation,
			row.State, row.RecommendedAction); err != nil {
			return err
		}
	}
	return table.Flush()
}
```

Update every direct renderer call in `vm_runtime_status_test.go` to pass the intended view. The component test uses `vmRuntimeStatusDetailView`; the deterministic-table test uses `vmRuntimeStatusFleetView`.

- [ ] **Step 4: Run the focused rendering tests and verify GREEN**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMRuntimeStatusJSONIgnoresHumanView|TestVMStatusComponents|TestVMRuntimeStatusRenderingIsDeterministic' -count=1
```

Expected: PASS. Human table output is still old at this checkpoint; only command intent and JSON preservation are established.

- [ ] **Step 5: Commit the intent plumbing checkpoint**

Run `but status` and confirm only the two VM runtime status files are uncommitted, then:

```bash
but commit vm-runtime-status-output -m "catch: carry VM runtime status view intent"
```

Expected: one new commit on `vm-runtime-status-output`; no uncommitted changes.

---

### Task 2: Add Bounded Human Identity Labels

**Files:**
- Modify: `pkg/catch/vm_runtime_status.go:628-646`
- Test: `pkg/catch/vm_runtime_status_test.go` beside the rendering tests

**Interfaces:**
- Produces: `vmRuntimeStatusRuntimeSummary(*vmRuntimeStatusIdentity) string`
- Produces: `vmRuntimeStatusRuntimeDetail(*vmRuntimeStatusIdentity, bool) string`
- Produces: `vmRuntimeStatusPromotionSummary(*vmRuntimeStatusIdentity) string`
- Produces: `vmRuntimeStatusComponentSummary(vmComponentStatusIdentity) string`
- Produces: `vmRuntimeStatusHumanState(string) string`
- Produces: `vmRuntimeStatusPolicySummary(vmRuntimeStatusRow) string`
- Produces: `vmRuntimeStatusPolicyDisplay(vmRuntimeStatusRow) string`
- Produces: `sentenceCaseVMRuntimeStatusAction(string) string`

- [ ] **Step 1: Write table-driven failing tests for official, legacy, component, fallback, and state labels**

Add:

```go
func TestVMRuntimeStatusHumanIdentityLabels(t *testing.T) {
	official := &vmRuntimeStatusIdentity{
		ID:              "firecracker-v1.16.1-yeet-v1",
		ManifestSHA256:  strings.Repeat("a", 64),
		Source:          "official",
		UpstreamVersion: "v1.16.1",
		Support:         "supported",
	}
	legacy := &vmRuntimeStatusIdentity{
		ID:             "legacy-firecracker-1-14-3-" + strings.Repeat("b", 64) + "-jailer-" + strings.Repeat("c", 64),
		ManifestSHA256: strings.Repeat("d", 64),
		Source:         "custom-legacy",
		Support:        "legacy-unlisted",
	}
	unknown := &vmRuntimeStatusIdentity{
		ID:     strings.Repeat("unexpected-identity-", 5),
		Source: "future-source",
	}

	if got, want := vmRuntimeStatusRuntimeSummary(official), "1.16.1 official"; got != want {
		t.Fatalf("official summary = %q, want %q", got, want)
	}
	if got, want := vmRuntimeStatusRuntimeDetail(official, true), "v1.16.1 / yeet-v1 [aaaaaaaaaaaa] (official, supported)"; got != want {
		t.Fatalf("official detail = %q, want %q", got, want)
	}
	if got, want := vmRuntimeStatusPromotionSummary(official), "1.16.1 / yeet-v1"; got != want {
		t.Fatalf("promotion summary = %q, want %q", got, want)
	}
	if got, want := vmRuntimeStatusRuntimeSummary(legacy), "1.14.3 legacy"; got != want {
		t.Fatalf("legacy summary = %q, want %q", got, want)
	}
	if got, want := vmRuntimeStatusRuntimeDetail(legacy, true), "v1.14.3 [dddddddddddd] (custom legacy)"; got != want {
		t.Fatalf("legacy detail = %q, want %q", got, want)
	}
	if got := vmRuntimeStatusRuntimeSummary(nil); got != "-" {
		t.Fatalf("nil summary = %q, want -", got)
	}
	if got := vmRuntimeStatusRuntimeSummary(unknown); len(got) > 64 || !strings.Contains(got, "future source") {
		t.Fatalf("unknown summary is not bounded and attributable: %q", got)
	}
}

func TestVMRuntimeStatusHumanComponentAndStateLabels(t *testing.T) {
	guest := vmComponentStatusIdentity{
		ID:             "legacy-guest-ubuntu-26-04-ubuntu-26-04-amd64-v11-" + strings.Repeat("a", 64),
		ManifestSHA256: strings.Repeat("b", 64),
		Source:         "custom-legacy",
	}
	kernel := vmComponentStatusIdentity{
		ID:             "legacy-kernel-linux-7-1-2-yeet-" + strings.Repeat("c", 64),
		ManifestSHA256: strings.Repeat("d", 64),
		Source:         "custom-legacy",
	}
	if got, want := vmRuntimeStatusComponentSummary(guest), "ubuntu-26-04-amd64-v11 (custom legacy)"; got != want {
		t.Fatalf("guest label = %q, want %q", got, want)
	}
	if got, want := vmRuntimeStatusComponentSummary(kernel), "linux-7-1-2-yeet (custom legacy)"; got != want {
		t.Fatalf("kernel label = %q, want %q", got, want)
	}
	officialGuest := vmComponentStatusIdentity{ID: "guest-ubuntu-26.04-amd64-v4", Source: "official"}
	officialKernel := vmComponentStatusIdentity{ID: "kernel-linux-7.1.1-yeet-v2", Source: "official"}
	if got, want := vmRuntimeStatusComponentSummary(officialGuest), "ubuntu-26.04-amd64-v4 (official)"; got != want {
		t.Fatalf("official guest label = %q, want %q", got, want)
	}
	if got, want := vmRuntimeStatusComponentSummary(officialKernel), "linux-7.1.1-yeet-v2 (official)"; got != want {
		t.Fatalf("official kernel label = %q, want %q", got, want)
	}
	for raw, want := range map[string]string{
		"current":                     "healthy",
		"missing-or-untrusted-marker": "marker unverified",
		"running-config-diverged":     "config diverged",
		"failed-rolled-back":          "failed, rolled back",
	} {
		if got := vmRuntimeStatusHumanState(raw); got != want {
			t.Fatalf("state %q = %q, want %q", raw, got, want)
		}
	}
	row := vmRuntimeStatusRow{Policy: "manual", Channel: "stable"}
	if got, want := vmRuntimeStatusPolicySummary(row), "manual/stable"; got != want {
		t.Fatalf("policy summary = %q, want %q", got, want)
	}
	if got, want := vmRuntimeStatusPolicyDisplay(row), "manual / stable"; got != want {
		t.Fatalf("policy detail = %q, want %q", got, want)
	}
	if got, want := sentenceCaseVMRuntimeStatusAction("restart when ready"), "Restart when ready."; got != want {
		t.Fatalf("action = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMRuntimeStatusHumanIdentityLabels|TestVMRuntimeStatusHumanComponentAndStateLabels' -count=1
```

Expected: compilation fails because the human-label helpers do not exist.

- [ ] **Step 3: Implement the pure bounded-label helpers**

Add the following helper set beside the two old full-identity display helpers. Keep the old helpers until Task 4 removes the temporary legacy fleet renderer that still calls them:

```go
const (
	vmRuntimeStatusFingerprintLength = 12
	vmRuntimeStatusFallbackIDMax     = 48
)

func vmRuntimeStatusRuntimeSummary(identity *vmRuntimeStatusIdentity) string {
	if identity == nil {
		return "-"
	}
	label := strings.TrimPrefix(vmRuntimeStatusRuntimeVersion(identity), "v")
	if label == "" {
		label = vmRuntimeStatusBoundedID(identity.ID)
	}
	if source := vmRuntimeStatusSourceSummary(identity.Source); source != "" {
		label += " " + source
	}
	return label
}

func vmRuntimeStatusRuntimeDetail(identity *vmRuntimeStatusIdentity, includeFingerprint bool) string {
	if identity == nil {
		return "-"
	}
	label := vmRuntimeStatusRuntimeRelease(identity)
	if label == "" {
		label = vmRuntimeStatusBoundedID(identity.ID)
	}
	if includeFingerprint && len(identity.ManifestSHA256) >= vmRuntimeStatusFingerprintLength {
		label += " [" + identity.ManifestSHA256[:vmRuntimeStatusFingerprintLength] + "]"
	}
	metadata := []string{}
	if source := vmRuntimeStatusSourceDetail(identity.Source); source != "" {
		metadata = append(metadata, source)
	}
	if support := vmRuntimeStatusSupportDetail(identity.Support); support != "" {
		metadata = append(metadata, support)
	}
	if len(metadata) > 0 {
		label += " (" + strings.Join(metadata, ", ") + ")"
	}
	return label
}

func vmRuntimeStatusPromotionSummary(identity *vmRuntimeStatusIdentity) string {
	if identity == nil {
		return "-"
	}
	label := vmRuntimeStatusRuntimeRelease(identity)
	if label == "" {
		label = vmRuntimeStatusBoundedID(identity.ID)
	}
	return strings.TrimPrefix(label, "v")
}

func vmRuntimeStatusRuntimeVersion(identity *vmRuntimeStatusIdentity) string {
	if identity == nil {
		return ""
	}
	if version := strings.TrimSpace(identity.UpstreamVersion); version != "" {
		return version
	}
	if match := legacyVMRuntimePolicyIDPattern.FindStringSubmatch(identity.ID); len(match) == 4 {
		return "v" + strings.Join(match[1:], ".")
	}
	version, err := vmRuntimeVersionFromID(identity.ID)
	if err != nil {
		return ""
	}
	return version
}

func vmRuntimeStatusRuntimeRelease(identity *vmRuntimeStatusIdentity) string {
	version := vmRuntimeStatusRuntimeVersion(identity)
	if version == "" {
		return ""
	}
	prefix := "firecracker-" + version + "-"
	if strings.HasPrefix(identity.ID, prefix) {
		if release := strings.TrimPrefix(identity.ID, prefix); release != "" {
			return version + " / " + release
		}
	}
	return version
}

func vmRuntimeStatusComponentSummary(identity vmComponentStatusIdentity) string {
	if strings.TrimSpace(identity.ID) == "" {
		return "-"
	}
	label := vmRuntimeStatusTrimDigestSuffix(identity.ID)
	if strings.HasPrefix(label, "legacy-guest-") {
		label = vmRuntimeStatusCollapseRepeatedPrefix(strings.TrimPrefix(label, "legacy-guest-"))
	} else if strings.HasPrefix(label, "legacy-kernel-") {
		label = strings.TrimPrefix(label, "legacy-kernel-")
	} else if strings.HasPrefix(label, "guest-") {
		label = strings.TrimPrefix(label, "guest-")
	} else if strings.HasPrefix(label, "kernel-") {
		label = strings.TrimPrefix(label, "kernel-")
	}
	label = vmRuntimeStatusBoundedID(label)
	if source := vmRuntimeStatusSourceDetail(identity.Source); source != "" {
		label += " (" + source + ")"
	}
	return label
}

func vmRuntimeStatusTrimDigestSuffix(value string) string {
	const suffixLength = 65
	if len(value) >= suffixLength && value[len(value)-suffixLength] == '-' && isLowerSHA256(value[len(value)-64:]) {
		return value[:len(value)-suffixLength]
	}
	return value
}

func vmRuntimeStatusCollapseRepeatedPrefix(value string) string {
	for offset := strings.IndexByte(value, '-'); offset >= 0; {
		prefix := value[:offset]
		if strings.HasPrefix(value[offset+1:], prefix+"-") {
			return value[offset+1:]
		}
		next := strings.IndexByte(value[offset+1:], '-')
		if next < 0 {
			break
		}
		offset += next + 1
	}
	return value
}

func vmRuntimeStatusBoundedID(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= vmRuntimeStatusFallbackIDMax {
		return value
	}
	return string(runes[:vmRuntimeStatusFallbackIDMax-3]) + "..."
}

func vmRuntimeStatusSourceSummary(source string) string {
	switch {
	case source == "official":
		return "official"
	case strings.HasSuffix(source, "-legacy"):
		return "legacy"
	case strings.HasPrefix(source, "local:"):
		return "local"
	default:
		return vmRuntimeStatusWords(source)
	}
}

func vmRuntimeStatusSourceDetail(source string) string {
	if strings.HasPrefix(source, "local:") {
		return "local"
	}
	return vmRuntimeStatusWords(source)
}

func vmRuntimeStatusSupportDetail(support string) string {
	switch support {
	case "", "legacy-unlisted", "local":
		return ""
	default:
		return vmRuntimeStatusWords(support)
	}
}

func vmRuntimeStatusWords(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(value), "-", " ")
}

func vmRuntimeStatusHumanState(state string) string {
	switch state {
	case "":
		return "unknown"
	case "current":
		return "healthy"
	case "missing-or-untrusted-marker":
		return "marker unverified"
	case "running-config-diverged":
		return "config diverged"
	case "failed-rolled-back":
		return "failed, rolled back"
	default:
		return vmRuntimeStatusWords(state)
	}
}

func vmRuntimeStatusPolicySummary(row vmRuntimeStatusRow) string {
	if row.Policy == "" && row.Channel == "" {
		return "-"
	}
	if row.Channel == "" {
		return row.Policy
	}
	if row.Policy == "" {
		return row.Channel
	}
	return row.Policy + "/" + row.Channel
}

func vmRuntimeStatusPolicyDisplay(row vmRuntimeStatusRow) string {
	if row.Policy == "" && row.Channel == "" {
		return "-"
	}
	if row.Channel == "" {
		return row.Policy
	}
	if row.Policy == "" {
		return row.Channel
	}
	return row.Policy + " / " + row.Channel
}

func sentenceCaseVMRuntimeStatusAction(action string) string {
	action = strings.TrimSpace(action)
	if action == "" {
		return ""
	}
	runes := []rune(action)
	runes[0] = unicode.ToUpper(runes[0])
	if runes[len(runes)-1] != '.' {
		runes = append(runes, '.')
	}
	return string(runes)
}
```

Add `unicode` to the imports.

- [ ] **Step 4: Run the focused label tests and verify GREEN**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMRuntimeStatusHumanIdentityLabels|TestVMRuntimeStatusHumanComponentAndStateLabels' -count=1
```

Expected: PASS.

- [ ] **Step 5: Run all VM runtime status tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run '^TestVMRuntimeStatus' -count=1
```

Expected: PASS. The temporary legacy table still has both of its original full-identity display helpers at this checkpoint.

- [ ] **Step 6: Commit the human-label checkpoint**

After `but status` confirms only the two intended status files changed:

```bash
but commit vm-runtime-status-output -m "catch: format readable VM runtime identities"
```

Expected: one new commit; no uncommitted changes.

---

### Task 3: Render the Single-VM Detail View

**Files:**
- Modify: `pkg/catch/vm_runtime_status.go:599-646`
- Test: `pkg/catch/vm_runtime_status_test.go:52-89,389-403`

**Interfaces:**
- Consumes: label helpers from Task 2
- Produces: `renderVMRuntimeStatusDetail(io.Writer, []vmRuntimeStatusRow) error`
- Produces: `writeVMRuntimeStatusWrapped(io.Writer, string, string) error`
- Produces: `vmRuntimeStatusTransitionDisplay(string) string`
- Produces: `vmRuntimeStatusValueOrDash(string) string`

- [ ] **Step 1: Replace the old component-table test with a failing exact detail-view test**

Keep the status-row assertions in `TestVMStatusComponents`, then replace its old table assertions with:

```go
	running := rows[0].Configured
	rows[0].Running = &running
	rows[0].Previous = &vmRuntimeStatusIdentity{
		ID:             "legacy-firecracker-1-14-3-" + strings.Repeat("e", 64) + "-jailer-" + strings.Repeat("f", 64),
		ManifestSHA256: strings.Repeat("8", 64),
		Source:         "custom-legacy",
		Support:        "legacy-unlisted",
	}
	rows[0].State = "current"
	rows[0].LastTransition = "2026-07-22T15:40:39.509530928Z"

	var out bytes.Buffer
	if err := renderVMRuntimeStatus(&out, "table", rows, vmRuntimeStatusDetailView); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"devbox  healthy",
		"Guest base:  ubuntu-26.04-amd64-v4 (official)",
		"Kernel:      linux-7.1.1-yeet-v2 (official)",
		"Runtime",
		"Running:     v1.16.1 / yeet-v1",
		"Configured:  same as running",
		"Staged:      v1.17.0",
		"Previous:    v1.14.3 [888888888888] (custom legacy)",
		"Policy:      manual / stable",
		"Isolation:   jailer",
		"Last change: 2026-07-22 15:40:39 UTC",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("detail status missing %q:\n%s", want, out.String())
		}
	}
	for _, digest := range []string{guest.ManifestSHA256, kernel.ManifestSHA256, configured.ManifestSHA256, staged.ManifestSHA256, strings.Repeat("8", 64)} {
		if digest != "" && strings.Contains(out.String(), digest) {
			t.Fatalf("detail status contains full digest %q:\n%s", digest, out.String())
		}
	}
```

Add a wrapping test:

```go
func TestVMRuntimeStatusDetailWrapsRecommendedAction(t *testing.T) {
	row := vmRuntimeStatusRow{
		Service: "devbox",
		Configured: vmRuntimeStatusIdentity{ID: "runtime", Source: "future-source"},
		Policy: "manual", Channel: "stable", JailerIsolation: "jailer",
		State: "missing-or-untrusted-marker",
		RecommendedAction: "restart the VM when downtime is acceptable to establish a trusted runtime marker and verify the selected host runtime",
	}
	var out bytes.Buffer
	if err := renderVMRuntimeStatus(&out, "table", []vmRuntimeStatusRow{row}, vmRuntimeStatusDetailView); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if len([]rune(line)) > 100 {
			t.Fatalf("detail line is %d columns: %q", len([]rune(line)), line)
		}
	}
	if !strings.Contains(out.String(), "Action:") {
		t.Fatalf("detail status omitted action:\n%s", out.String())
	}
}
```

- [ ] **Step 2: Run the detail tests and verify RED**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMStatusComponents|TestVMRuntimeStatusDetailWrapsRecommendedAction' -count=1
```

Expected: FAIL because table rendering still uses the legacy wide renderer.

- [ ] **Step 3: Route detail view and implement the vertical renderer**

Change the table branch in `renderVMRuntimeStatus`:

```go
	case "", "table":
		if view == vmRuntimeStatusDetailView {
			return renderVMRuntimeStatusDetail(w, rows)
		}
		return renderLegacyVMRuntimeStatusTable(w, rows)
```

Add the detail renderer and shared wrapping helpers:

```go
const vmRuntimeStatusHumanWidth = 100

func renderVMRuntimeStatusDetail(w io.Writer, rows []vmRuntimeStatusRow) error {
	for index, row := range rows {
		if index > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "%s  %s\n\n", row.Service, vmRuntimeStatusHumanState(row.State)); err != nil {
			return err
		}
		components := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		if _, err := fmt.Fprintf(components, "  Guest base:\t%s\n  Kernel:\t%s\n",
			vmRuntimeStatusComponentSummary(row.GuestBase), vmRuntimeStatusComponentSummary(row.Kernel)); err != nil {
			return err
		}
		if err := components.Flush(); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, "\n  Runtime"); err != nil {
			return err
		}
		running := vmRuntimeStatusRuntimeDetail(row.Running, true)
		configured := vmRuntimeStatusRuntimeDetail(&row.Configured, true)
		if row.Running != nil && vmRuntimeStatusIdentityEqual(*row.Running, row.Configured) {
			configured = "same as running"
		}
		runtime := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		if _, err := fmt.Fprintf(runtime, "    Running:\t%s\n    Configured:\t%s\n    Staged:\t%s\n    Previous:\t%s\n",
			running, configured, vmRuntimeStatusRuntimeDetail(row.Staged, true), vmRuntimeStatusRuntimeDetail(row.Previous, true)); err != nil {
			return err
		}
		if err := runtime.Flush(); err != nil {
			return err
		}
		fields := tabwriter.NewWriter(w, 0, 0, 1, ' ', 0)
		if _, err := fmt.Fprintf(fields, "\n  Policy:\t%s\n  Promoted:\t%s\n  Isolation:\t%s\n",
			vmRuntimeStatusPolicyDisplay(row), vmRuntimeStatusRuntimeDetail(row.LatestPromoted, false), vmRuntimeStatusValueOrDash(row.JailerIsolation)); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(fields, "  Last change:\t%s\n", vmRuntimeStatusTransitionDisplay(row.LastTransition)); err != nil {
			return err
		}
		if err := fields.Flush(); err != nil {
			return err
		}
		if row.RecommendedAction != "" {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
			if err := writeVMRuntimeStatusWrapped(w, "  Action: ", sentenceCaseVMRuntimeStatusAction(row.RecommendedAction)); err != nil {
				return err
			}
		}
	}
	return nil
}

func vmRuntimeStatusTransitionDisplay(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return strings.TrimSpace(value)
	}
	return parsed.UTC().Format("2006-01-02 15:04:05 UTC")
}

func vmRuntimeStatusValueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func writeVMRuntimeStatusWrapped(w io.Writer, prefix, value string) error {
	continuation := strings.Repeat(" ", len([]rune(prefix)))
	width := vmRuntimeStatusHumanWidth - len([]rune(prefix))
	if width < 20 {
		width = 20
	}
	for index, line := range vmRuntimeStatusWrapWords(value, width) {
		linePrefix := prefix
		if index > 0 {
			linePrefix = continuation
		}
		if _, err := fmt.Fprintln(w, linePrefix+line); err != nil {
			return err
		}
	}
	return nil
}

func vmRuntimeStatusWrapWords(value string, width int) []string {
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{""}
	}
	lines := []string{}
	current := words[0]
	for _, word := range words[1:] {
		if len([]rune(current))+1+len([]rune(word)) <= width {
			current += " " + word
			continue
		}
		lines = append(lines, current)
		current = word
	}
	return append(lines, current)
}
```

Delete `renderLegacyVMRuntimeStatusTable` only after Task 4 provides `renderVMRuntimeStatusFleet` and the package compiles. Until then, keep the fleet branch calling the legacy helper.

- [ ] **Step 4: Run the detail tests and verify GREEN**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMStatusComponents|TestVMRuntimeStatusDetailWrapsRecommendedAction|TestVMRuntimeStatusJSONIgnoresHumanView' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the detail renderer checkpoint**

After `but status` confirms the intended files:

```bash
but commit vm-runtime-status-output -m "catch: render readable VM runtime detail"
```

Expected: one new commit; no uncommitted changes.

---

### Task 4: Render the Fleet Overview and Attention List

**Files:**
- Modify: `pkg/catch/vm_runtime_status.go:599-646`
- Test: `pkg/catch/vm_runtime_status_test.go:389-403`

**Interfaces:**
- Consumes: Task 2 labels and Task 3 wrapping helper
- Produces: `renderVMRuntimeStatusFleet(io.Writer, []vmRuntimeStatusRow) error`
- Produces: `sharedVMRuntimeStatusPromotion([]vmRuntimeStatusRow) (*vmRuntimeStatusIdentity, string, bool)`
- Removes: temporary `renderLegacyVMRuntimeStatusTable`
- Removes: old full-identity table display helpers

- [ ] **Step 1: Replace the old deterministic-wide-table test with failing fleet behavior tests**

Replace `TestVMRuntimeStatusRenderingIsDeterministic` with:

```go
func TestVMRuntimeStatusFleetRenderingIsCompactAndDeterministic(t *testing.T) {
	promoted := &vmRuntimeStatusIdentity{
		ID: "firecracker-v1.16.1-yeet-v1", ManifestSHA256: strings.Repeat("1", 64),
		Source: "official", UpstreamVersion: "v1.16.1", Support: "supported",
	}
	legacy := vmRuntimeStatusIdentity{
		ID: "legacy-firecracker-1-14-3-" + strings.Repeat("2", 64) + "-jailer-" + strings.Repeat("3", 64),
		ManifestSHA256: strings.Repeat("4", 64), Source: "custom-legacy", Support: "legacy-unlisted",
	}
	official := *promoted
	rows := []vmRuntimeStatusRow{
		{
			Service: "alpha", Configured: legacy, Policy: "manual", Channel: "stable",
			LatestPromoted: promoted, JailerIsolation: "jailer", State: "missing-or-untrusted-marker",
			RecommendedAction: "restart the VM when downtime is acceptable to establish a trusted runtime marker",
		},
		{
			Service: "beta", Running: &official, Configured: official, Policy: "manual", Channel: "stable",
			LatestPromoted: promoted, JailerIsolation: "jailer", State: "current",
		},
	}
	var first, second bytes.Buffer
	if err := renderVMRuntimeStatus(&first, "table", rows, vmRuntimeStatusFleetView); err != nil {
		t.Fatal(err)
	}
	if err := renderVMRuntimeStatus(&second, "table", rows, vmRuntimeStatusFleetView); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatalf("fleet output is non-deterministic:\n%s\n%s", first.String(), second.String())
	}
	for _, want := range []string{
		"VM", "RUNNING", "CONFIGURED", "STAGED", "POLICY", "STATE",
		"alpha", "unverified", "1.14.3 legacy", "marker unverified",
		"beta", "1.16.1 official", "healthy",
		"Promoted stable runtime: 1.16.1 / yeet-v1",
		"Needs attention:", "alpha: Restart the VM",
	} {
		if !strings.Contains(first.String(), want) {
			t.Fatalf("fleet status missing %q:\n%s", want, first.String())
		}
	}
	for _, forbidden := range []string{strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64), "GUEST BASE", "KERNEL", "PREVIOUS", "ACTION"} {
		if strings.Contains(first.String(), forbidden) {
			t.Fatalf("fleet status contains %q:\n%s", forbidden, first.String())
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(first.String(), "\n"), "\n") {
		if len([]rune(line)) > vmRuntimeStatusHumanWidth {
			t.Fatalf("fleet line is %d columns: %q", len([]rune(line)), line)
		}
	}
}

func TestVMRuntimeStatusFleetOmitsUnsharedPromotion(t *testing.T) {
	stable := &vmRuntimeStatusIdentity{ID: "firecracker-v1.16.1-yeet-v1", ManifestSHA256: strings.Repeat("1", 64), UpstreamVersion: "v1.16.1", Source: "official"}
	candidate := &vmRuntimeStatusIdentity{ID: "firecracker-v1.17.0-yeet-v1", ManifestSHA256: strings.Repeat("2", 64), UpstreamVersion: "v1.17.0", Source: "official"}
	rows := []vmRuntimeStatusRow{
		{Service: "alpha", Configured: *stable, Policy: "manual", Channel: "stable", LatestPromoted: stable},
		{Service: "beta", Configured: *candidate, Policy: "manual", Channel: "candidate", LatestPromoted: candidate},
	}
	var out bytes.Buffer
	if err := renderVMRuntimeStatus(&out, "table", rows, vmRuntimeStatusFleetView); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Promoted ") {
		t.Fatalf("fleet status implied an unshared promotion:\n%s", out.String())
	}
}
```

- [ ] **Step 2: Run the fleet tests and verify RED**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMRuntimeStatusFleetRenderingIsCompactAndDeterministic|TestVMRuntimeStatusFleetOmitsUnsharedPromotion' -count=1
```

Expected: FAIL because fleet output still uses the legacy thirteen-column renderer.

- [ ] **Step 3: Implement the compact fleet renderer**

Add:

```go
func renderVMRuntimeStatusFleet(w io.Writer, rows []vmRuntimeStatusRow) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "VM\tRUNNING\tCONFIGURED\tSTAGED\tPOLICY\tSTATE"); err != nil {
		return err
	}
	for _, row := range rows {
		running := "unverified"
		if row.Running != nil {
			running = vmRuntimeStatusRuntimeSummary(row.Running)
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Service, running, vmRuntimeStatusRuntimeSummary(&row.Configured),
			vmRuntimeStatusRuntimeSummary(row.Staged), vmRuntimeStatusPolicySummary(row),
			vmRuntimeStatusHumanState(row.State)); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if promoted, channel, ok := sharedVMRuntimeStatusPromotion(rows); ok {
		if _, err := fmt.Fprintf(w, "\nPromoted %s runtime: %s\n", channel, vmRuntimeStatusPromotionSummary(promoted)); err != nil {
			return err
		}
	}
	actions := make([]vmRuntimeStatusRow, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.RecommendedAction) != "" {
			actions = append(actions, row)
		}
	}
	if len(actions) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\nNeeds attention:"); err != nil {
		return err
	}
	for _, row := range actions {
		if err := writeVMRuntimeStatusWrapped(w, "  "+row.Service+": ", sentenceCaseVMRuntimeStatusAction(row.RecommendedAction)); err != nil {
			return err
		}
	}
	return nil
}

func sharedVMRuntimeStatusPromotion(rows []vmRuntimeStatusRow) (*vmRuntimeStatusIdentity, string, bool) {
	if len(rows) == 0 || rows[0].LatestPromoted == nil || rows[0].Channel == "" {
		return nil, "", false
	}
	want := rows[0].LatestPromoted
	channel := rows[0].Channel
	for _, row := range rows[1:] {
		if row.LatestPromoted == nil || row.Channel != channel || !vmRuntimeStatusIdentityEqual(*want, *row.LatestPromoted) {
			return nil, "", false
		}
	}
	return want, channel, true
}
```

Change the fleet branch in `renderVMRuntimeStatus` from `renderLegacyVMRuntimeStatusTable` to `renderVMRuntimeStatusFleet`. Remove `renderLegacyVMRuntimeStatusTable`, `vmComponentStatusIdentityDisplay`, and `vmRuntimeStatusIdentityDisplay`. The only remaining human output must go through the detail/fleet helpers; JSON continues to encode the untouched rows.

- [ ] **Step 4: Run fleet and all status tests and verify GREEN**

Run:

```bash
mise exec -- go test ./pkg/catch -run '^TestVMRuntimeStatus|^TestVMStatusComponents$' -count=1
```

Expected: PASS.

- [ ] **Step 5: Format and inspect the exact human fixtures**

Run:

```bash
mise exec -- gofmt -w pkg/catch/vm_runtime_status.go pkg/catch/vm_runtime_status_test.go
mise exec -- go test ./pkg/catch -run 'TestVMRuntimeStatusFleetRenderingIsCompactAndDeterministic|TestVMStatusComponents' -count=1 -v
```

Expected: PASS with no formatting changes left by a second `gofmt` run. Inspect the test fixture strings and confirm the detail/fleet layouts match the approved design.

- [ ] **Step 6: Commit the fleet renderer checkpoint**

After `but status` confirms only the intended files:

```bash
but commit vm-runtime-status-output -m "catch: render compact VM runtime fleet status"
```

Expected: one new commit; no uncommitted changes.

---

### Task 5: Verify the Complete Change and Prepare It for Review

**Files:**
- Verify: `pkg/catch/vm_runtime_status.go`
- Verify: `pkg/catch/vm_runtime_status_test.go`
- Verify: `docs/superpowers/specs/2026-07-22-vm-runtime-status-human-output-design.md`
- Verify: `docs/superpowers/plans/2026-07-22-vm-runtime-status-human-output.md`

**Interfaces:**
- Verifies: human detail and fleet output
- Verifies: unchanged JSON status contract
- Produces: a clean GitButler branch ready for explicit landing authorization

- [ ] **Step 1: Run the complete Catch package tests**

Run:

```bash
mise exec -- go test ./pkg/catch -count=1
```

Expected: PASS with zero failed tests.

- [ ] **Step 2: Run the full Go suite and builds**

Run:

```bash
mise exec -- go test ./... -count=1
mise exec -- go build ./cmd/yeet ./cmd/catch
```

Expected: both commands exit 0.

- [ ] **Step 3: Run the deterministic repository gate**

Run:

```bash
mise exec -- pre-commit run --all-files
```

Expected: every hook passes, including private-info scan, coverage/quality, lint, vulnerability scan, and dependency checks. Do not refresh a quality baseline to make this pass.

- [ ] **Step 4: Review the branch diff against the approved scope**

Run:

```bash
but status
but diff
but show vm-runtime-status-output
```

Expected: only the approved spec, implementation plan, status renderer, and status tests are present. There are no CLI syntax, permission, database, runtime-transition, website, or JSON-model changes.

- [ ] **Step 5: Consolidate the unpublished session branch**

Create the required recovery point, then use GitButler to turn the approved design, implementation plan, and implementation checkpoints into the repository's required single landing commit:

Run:

```bash
but oplog snapshot -m "before VM runtime status branch cleanup"
but squash vm-runtime-status-output -m "catch: make VM runtime status readable"
but status
but show vm-runtime-status-output
```

Expected: `vm-runtime-status-output` contains one commit named `catch: make VM runtime status readable`; the commit contains only the two documentation files, renderer, and renderer tests; there are no uncommitted changes.

- [ ] **Step 6: Check whether the target branch moved**

Run:

```bash
but pull --check
```

Expected: the branch is based on current `origin/main` and is ready for user-authorized landing. If the check reports that `origin/main` moved, run `but pull`, then repeat Tasks 5 Steps 1-4 before calling the branch ready. Do not push, land, deploy, or release in this task without that authorization.
