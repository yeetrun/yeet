#!/usr/bin/env bash
set -euo pipefail

export LC_ALL=C

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
driver="$repo_root/scripts/test-firecracker-runtime-integration.sh"
tmp_dir="$(mktemp -d)"
cleanup() { rm -rf "$tmp_dir"; }
trap cleanup EXIT INT TERM
fail() { echo "Firecracker runtime integration fixture test failed: $*" >&2; exit 1; }

mkdir -p "$tmp_dir/runtime" "$tmp_dir/guest" "$tmp_dir/kernel" "$tmp_dir/bin"
runtime_id=firecracker-v1.16.1-yeet-v1
runtime_version=v1.16.1
yeet_commit="$(git -C "$repo_root" rev-parse HEAD)"

cat >"$tmp_dir/runtime/firecracker" <<EOF
#!/usr/bin/env bash
[ "\${1:-}" = --version ] && { echo 'Firecracker $runtime_version'; exit 0; }
exit 1
EOF
cat >"$tmp_dir/runtime/jailer" <<EOF
#!/usr/bin/env bash
[ "\${1:-}" = --version ] && { echo 'Jailer $runtime_version'; exit 0; }
exit 1
EOF
chmod +x "$tmp_dir/runtime/firecracker" "$tmp_dir/runtime/jailer"
firecracker_sha="$(shasum -a 256 "$tmp_dir/runtime/firecracker" | awk '{print $1}')"
jailer_sha="$(shasum -a 256 "$tmp_dir/runtime/jailer" | awk '{print $1}')"
jq -n --arg id "$runtime_id" --arg version "$runtime_version" --arg fc "$firecracker_sha" --arg jailer "$jailer_sha" '{
  schema_version:1,runtime_id:$id,architecture:"amd64",upstream:{version:$version},
  components:{firecracker:{path:"firecracker",sha256:$fc},jailer:{path:"jailer",sha256:$jailer}}
}' >"$tmp_dir/runtime/runtime-manifest.json"
runtime_manifest_sha256="$(shasum -a 256 "$tmp_dir/runtime/runtime-manifest.json" | awk '{print $1}')"

printf 'rootfs fixture\n' >"$tmp_dir/guest/rootfs.ext4.zst"
printf 'kernel fixture\n' >"$tmp_dir/kernel/vmlinux"
jq -n '{rootfs:"rootfs.ext4.zst"}' >"$tmp_dir/guest/manifest.json"

cat >"$tmp_dir/bin/go-fixture" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
{
	echo "argv=$*"
	echo "scenario=${YEET_RUNTIME_INTEGRATION_SCENARIO:-}"
	echo "service=${YEET_RUNTIME_INTEGRATION_SERVICE:-}"
	echo "runtime=${YEET_RUNTIME_INTEGRATION_RUNTIME_ID:-}"
	echo "storage=${YEET_RUNTIME_INTEGRATION_STORAGE:-}"
	echo "assertions=${YEET_RUNTIME_INTEGRATION_ASSERTIONS:-}"
} >>"$YEET_RUNTIME_INTEGRATION_FIXTURE_LOG"
if [ "${1:-}" = build ]; then
	while [ "$#" -gt 0 ]; do
		if [ "$1" = -o ]; then
			mkdir -p "$(dirname "$2")"
			printf '#!/usr/bin/env bash\nexit 0\n' >"$2"
			exit 0
		fi
		shift
	done
fi
EOF
chmod +x "$tmp_dir/bin/go-fixture"

base_args=(
	--scenario ubuntu-current --launcher jailer-only
	--runtime-id "$runtime_id" --runtime-manifest-sha256 "$runtime_manifest_sha256"
	--firecracker "$tmp_dir/runtime/firecracker" --jailer "$tmp_dir/runtime/jailer"
	--guest-dir "$tmp_dir/guest" --kernel "$tmp_dir/kernel/vmlinux" --storage raw
	--data-root "$tmp_dir/data" --service-root "$tmp_dir/service"
	--test-user yeet-vm --test-uid 20001 --test-gid 20001
	--yeet-source "$repo_root" --yeet-commit "$yeet_commit"
)
assertions=(api-ready boot natural-reboot network-ready disk-snapshot-restore cleanup jailer-uid-gid-drop no-memory-snapshot)
for assertion in "${assertions[@]}"; do base_args+=(--assert "$assertion"); done

log="$tmp_dir/commands.log"
YEET_RUNTIME_INTEGRATION_TEST_MODE=1 \
	YEET_RUNTIME_INTEGRATION_GO="$tmp_dir/bin/go-fixture" \
	YEET_RUNTIME_INTEGRATION_FIXTURE_LOG="$log" \
	"$driver" "${base_args[@]}" >/dev/null

grep -F 'argv=build -o ' "$log" >/dev/null || fail "driver did not build the exact Catch source"
grep -F "argv=test ./pkg/catch -run ^TestFirecrackerRuntimeIntegration$ -count=1 -timeout=45m" "$log" >/dev/null || fail "driver did not run the repository integration test"
grep -F 'scenario=ubuntu-current' "$log" >/dev/null || fail "scenario was not forwarded"
grep -F "runtime=$runtime_id" "$log" >/dev/null || fail "runtime identity was not forwarded"
grep -F 'storage=raw' "$log" >/dev/null || fail "storage mode was not forwarded"
grep -F "assertions=$(IFS=,; echo "${assertions[*]}")" "$log" >/dev/null || fail "assertion set was not forwarded exactly"

expect_failure() {
	local label="$1"
	shift
	if YEET_RUNTIME_INTEGRATION_TEST_MODE=1 YEET_RUNTIME_INTEGRATION_GO="$tmp_dir/bin/go-fixture" \
		YEET_RUNTIME_INTEGRATION_FIXTURE_LOG="$log" "$driver" "$@" >/dev/null 2>&1; then
		fail "driver accepted invalid fixture: $label"
	fi
}

missing_assert=("${base_args[@]:0:${#base_args[@]}-2}")
expect_failure missing-assertion "${missing_assert[@]}"

direct_launcher=("${base_args[@]}")
for ((i=0; i<${#direct_launcher[@]}; i++)); do
	[ "${direct_launcher[$i]}" != --launcher ] || direct_launcher[$((i+1))]=firecracker-direct
done
expect_failure direct-launcher "${direct_launcher[@]}"

bad_digest=("${base_args[@]}")
for ((i=0; i<${#bad_digest[@]}; i++)); do
	[ "${bad_digest[$i]}" != --runtime-manifest-sha256 ] || bad_digest[$((i+1))]="$(printf '0%.0s' {1..64})"
done
expect_failure runtime-digest "${bad_digest[@]}"

duplicate_assert=("${base_args[@]}" --assert api-ready)
expect_failure duplicate-assertion "${duplicate_assert[@]}"

echo "Firecracker runtime integration driver fixtures verified"
