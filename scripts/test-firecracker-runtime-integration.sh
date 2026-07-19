#!/usr/bin/env bash
set -euo pipefail

export LC_ALL=C

usage() {
	cat >&2 <<'EOF'
usage: test-firecracker-runtime-integration.sh \
  --scenario NAME --launcher jailer-only \
  --runtime-id ID --runtime-manifest-sha256 SHA256 \
  --firecracker FILE --jailer FILE --guest-dir DIRECTORY --kernel FILE \
  --storage raw|zfs --data-root DIRECTORY --service-root PATH \
  --test-user yeet-vm --test-uid UID --test-gid GID \
  --yeet-source DIRECTORY --yeet-commit COMMIT \
  --assert NAME [--assert NAME ...]
EOF
	exit 2
}

fail() {
	echo "Firecracker runtime integration failed: $*" >&2
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

sha256_stdin() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum | awk '{print $1}'
	else
		shasum -a 256 | awk '{print $1}'
	fi
}

absolute_existing_dir() {
	local path="$1" label="$2"
	[ -d "$path" ] && [ ! -L "$path" ] || fail "$label must be an existing directory, not a symbolic link: $path"
	case "$path" in /*) ;; *) fail "$label must be absolute: $path" ;; esac
}

absolute_regular_file() {
	local path="$1" label="$2"
	[ -f "$path" ] && [ ! -L "$path" ] || fail "$label must be a regular file, not a symbolic link: $path"
	case "$path" in /*) ;; *) fail "$label must be absolute: $path" ;; esac
}

scenario="" launcher="" runtime_id="" runtime_manifest_sha256=""
firecracker="" jailer="" guest_dir="" kernel="" storage=""
data_root="" service_root="" test_user="" test_uid="" test_gid=""
yeet_source="" yeet_commit=""
assertions=()

while [ "$#" -gt 0 ]; do
	case "$1" in
		--scenario) [ "$#" -ge 2 ] || usage; scenario="$2"; shift 2 ;;
		--launcher) [ "$#" -ge 2 ] || usage; launcher="$2"; shift 2 ;;
		--runtime-id) [ "$#" -ge 2 ] || usage; runtime_id="$2"; shift 2 ;;
		--runtime-manifest-sha256) [ "$#" -ge 2 ] || usage; runtime_manifest_sha256="$2"; shift 2 ;;
		--firecracker) [ "$#" -ge 2 ] || usage; firecracker="$2"; shift 2 ;;
		--jailer) [ "$#" -ge 2 ] || usage; jailer="$2"; shift 2 ;;
		--guest-dir) [ "$#" -ge 2 ] || usage; guest_dir="$2"; shift 2 ;;
		--kernel) [ "$#" -ge 2 ] || usage; kernel="$2"; shift 2 ;;
		--storage) [ "$#" -ge 2 ] || usage; storage="$2"; shift 2 ;;
		--data-root) [ "$#" -ge 2 ] || usage; data_root="$2"; shift 2 ;;
		--service-root) [ "$#" -ge 2 ] || usage; service_root="$2"; shift 2 ;;
		--test-user) [ "$#" -ge 2 ] || usage; test_user="$2"; shift 2 ;;
		--test-uid) [ "$#" -ge 2 ] || usage; test_uid="$2"; shift 2 ;;
		--test-gid) [ "$#" -ge 2 ] || usage; test_gid="$2"; shift 2 ;;
		--yeet-source) [ "$#" -ge 2 ] || usage; yeet_source="$2"; shift 2 ;;
		--yeet-commit) [ "$#" -ge 2 ] || usage; yeet_commit="$2"; shift 2 ;;
		--assert) [ "$#" -ge 2 ] || usage; assertions+=("$2"); shift 2 ;;
		*) usage ;;
	esac
done

for required in scenario launcher runtime_id runtime_manifest_sha256 firecracker jailer guest_dir kernel storage data_root service_root test_user test_uid test_gid yeet_source yeet_commit; do
	[ -n "${!required}" ] || usage
done

[[ "$scenario" =~ ^[a-z0-9][a-z0-9-]{0,47}$ ]] || fail "invalid scenario name"
[ "$launcher" = jailer-only ] || fail "the integration launcher must be jailer-only"
[[ "$runtime_id" =~ ^firecracker-v[0-9]+[.][0-9]+[.][0-9]+-yeet-v[1-9][0-9]*$ ]] || fail "invalid runtime ID"
[[ "$runtime_manifest_sha256" =~ ^[0-9a-f]{64}$ ]] || fail "invalid runtime manifest digest"
case "$storage" in raw|zfs) ;; *) fail "storage must be raw or zfs" ;; esac
[[ "$test_uid" =~ ^[1-9][0-9]*$ ]] || fail "test UID must be non-root"
[[ "$test_gid" =~ ^[1-9][0-9]*$ ]] || fail "test GID must be non-root"
[[ "$yeet_commit" =~ ^[0-9a-f]{40}$ ]] || fail "Yeet commit must be an exact full commit"
case "$data_root" in /*) ;; *) fail "data root must be absolute" ;; esac
case "$service_root" in /*) ;; *) fail "service root must be an absolute mountpoint" ;; esac

expected_assertions=(api-ready boot natural-reboot network-ready disk-snapshot-restore cleanup jailer-uid-gid-drop no-memory-snapshot)
declare -A seen_assertions=()
for assertion in "${assertions[@]}"; do
	case " $({ printf '%s ' "${expected_assertions[@]}"; }) " in
		*" $assertion "*) ;;
		*) fail "unknown assertion: $assertion" ;;
	esac
	[ -z "${seen_assertions[$assertion]:-}" ] || fail "duplicate assertion: $assertion"
	seen_assertions[$assertion]=1
done
for assertion in "${expected_assertions[@]}"; do
	[ -n "${seen_assertions[$assertion]:-}" ] || fail "missing required assertion: $assertion"
done

require_command awk
require_command git
require_command jq
absolute_existing_dir "$yeet_source" "Yeet source"
absolute_existing_dir "$guest_dir" "guest directory"
absolute_regular_file "$firecracker" "Firecracker"
absolute_regular_file "$jailer" "jailer"
absolute_regular_file "$kernel" "kernel"
runtime_manifest="$(dirname "$firecracker")/runtime-manifest.json"
absolute_regular_file "$runtime_manifest" "runtime manifest"
[ "$(dirname "$firecracker")" = "$(dirname "$jailer")" ] || fail "Firecracker and jailer must come from one runtime directory"
[ "$(git -C "$yeet_source" rev-parse HEAD)" = "$yeet_commit" ] || fail "checked-out Yeet commit differs from --yeet-commit"
[ "$(sha256_file "$runtime_manifest")" = "$runtime_manifest_sha256" ] || fail "runtime manifest digest mismatch"

runtime_version="$(jq -er --arg id "$runtime_id" '
  select(.runtime_id == $id and .architecture == "amd64") |
  .upstream.version
' "$runtime_manifest")" || fail "runtime manifest identity mismatch"
[ "$(jq -er '.components.firecracker.path' "$runtime_manifest")" = firecracker ] || fail "runtime manifest Firecracker path mismatch"
[ "$(jq -er '.components.jailer.path' "$runtime_manifest")" = jailer ] || fail "runtime manifest jailer path mismatch"
[ "$(sha256_file "$firecracker")" = "$(jq -er '.components.firecracker.sha256' "$runtime_manifest")" ] || fail "Firecracker digest mismatch"
[ "$(sha256_file "$jailer")" = "$(jq -er '.components.jailer.sha256' "$runtime_manifest")" ] || fail "jailer digest mismatch"
[ "$($firecracker --version | sed -n '1p')" = "Firecracker $runtime_version" ] || fail "Firecracker version probe mismatch"
[ "$($jailer --version | sed -n '1p')" = "Jailer $runtime_version" ] || fail "jailer version probe mismatch"

guest_manifest="$guest_dir/manifest.json"
absolute_regular_file "$guest_manifest" "guest manifest"
guest_rootfs_name="$(jq -er '.rootfs' "$guest_manifest")" || fail "guest manifest has no rootfs"
case "$guest_rootfs_name" in ""|/*|*..*|*/*) fail "guest manifest has an unsafe rootfs path" ;; esac
absolute_regular_file "$guest_dir/$guest_rootfs_name" "guest rootfs"

test_mode="${YEET_RUNTIME_INTEGRATION_TEST_MODE:-0}"
if [ "$test_mode" != 1 ]; then
	[ "$(uname -s)" = Linux ] || fail "live integration requires Linux"
	case "$(uname -m)" in x86_64|amd64) ;; *) fail "live integration requires amd64" ;; esac
	[ "$(id -u)" -eq 0 ] || fail "live integration must run as root"
	[ "$test_user" = yeet-vm ] || fail "the production runtime identity is yeet-vm"
	[ "$(id -u "$test_user")" = "$test_uid" ] || fail "test UID does not match yeet-vm"
	[ "$(id -g "$test_user")" = "$test_gid" ] || fail "test GID does not match yeet-vm"
	[ -c /dev/kvm ] && [ -r /dev/kvm ] && [ -w /dev/kvm ] || fail "/dev/kvm is unavailable"
	require_command ssh
	require_command ssh-keygen
	require_command systemctl
	[ -z "${YEET_RUNTIME_INTEGRATION_GO:-}" ] || fail "Go command override requires explicit test mode"
else
	[ -n "${YEET_RUNTIME_INTEGRATION_GO:-}" ] || fail "test mode requires YEET_RUNTIME_INTEGRATION_GO"
fi

[ ! -L "$data_root" ] || fail "data root must not be a symbolic link"
mkdir -p "$data_root/services/catch/run" "$data_root/integration-keys"
chmod 0755 "$data_root" "$data_root/services" "$data_root/services/catch" "$data_root/services/catch/run"
chmod 0700 "$data_root/integration-keys"

key_path="$data_root/integration-keys/$scenario"
if [ "$test_mode" = 1 ]; then
	: >"$key_path"
	printf 'ssh-ed25519 fixture runtime-integration\n' >"$key_path.pub"
else
	[ ! -e "$key_path" ] && [ ! -e "$key_path.pub" ] || fail "integration SSH key already exists: $key_path"
	ssh-keygen -q -t ed25519 -N '' -C "yeet-runtime-integration-$scenario" -f "$key_path"
fi

if [ -n "${YEET_RUNTIME_INTEGRATION_GO:-}" ]; then
	go_command=("$YEET_RUNTIME_INTEGRATION_GO")
elif command -v mise >/dev/null 2>&1; then
	go_command=(mise exec -- go)
else
	require_command go
	go_command=(go)
fi

catch_binary="$data_root/services/catch/run/catch"
(cd "$yeet_source" && "${go_command[@]}" build -o "$catch_binary" ./cmd/catch)
chmod 0755 "$catch_binary"

assertion_csv="$(IFS=,; echo "${assertions[*]}")"
service_hash="$(printf '%s' "$data_root/$scenario" | sha256_stdin)"
service_name="yrt-${scenario:0:28}-${service_hash:0:8}"

(
	cd "$yeet_source"
	env \
		YEET_FIRECRACKER_RUNTIME_INTEGRATION=1 \
		YEET_RUNTIME_INTEGRATION_SCENARIO="$scenario" \
		YEET_RUNTIME_INTEGRATION_SERVICE="$service_name" \
		YEET_RUNTIME_INTEGRATION_RUNTIME_ID="$runtime_id" \
		YEET_RUNTIME_INTEGRATION_RUNTIME_MANIFEST_SHA256="$runtime_manifest_sha256" \
		YEET_RUNTIME_INTEGRATION_FIRECRACKER="$firecracker" \
		YEET_RUNTIME_INTEGRATION_JAILER="$jailer" \
		YEET_RUNTIME_INTEGRATION_GUEST_DIR="$guest_dir" \
		YEET_RUNTIME_INTEGRATION_KERNEL="$kernel" \
		YEET_RUNTIME_INTEGRATION_STORAGE="$storage" \
		YEET_RUNTIME_INTEGRATION_DATA_ROOT="$data_root" \
		YEET_RUNTIME_INTEGRATION_SERVICE_ROOT="$service_root" \
		YEET_RUNTIME_INTEGRATION_TEST_USER="$test_user" \
		YEET_RUNTIME_INTEGRATION_TEST_UID="$test_uid" \
		YEET_RUNTIME_INTEGRATION_TEST_GID="$test_gid" \
		YEET_RUNTIME_INTEGRATION_SSH_PRIVATE_KEY="$key_path" \
		YEET_RUNTIME_INTEGRATION_SSH_PUBLIC_KEY="$key_path.pub" \
		YEET_RUNTIME_INTEGRATION_ASSERTIONS="$assertion_csv" \
		"${go_command[@]}" test ./pkg/catch -run '^TestFirecrackerRuntimeIntegration$' -count=1 -timeout=45m
)

echo "Firecracker runtime integration passed: $scenario"
