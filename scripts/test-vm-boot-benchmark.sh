#!/usr/bin/env bash
set -euo pipefail

export LC_ALL=C

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
driver="$repo_root/scripts/vm-boot-benchmark.sh"
fixtures="$repo_root/scripts/testdata/vm-boot-benchmark"

fail() {
	echo "VM boot benchmark test failed: $*" >&2
	exit 1
}

assert_eq() {
	local want="$1"
	local got="$2"
	[ "$got" = "$want" ] || fail "got '$got', want '$want'"
}

assert_not_contains() {
	local value="$1"
	local forbidden="$2"
	case "$value" in
		*"$forbidden"*) fail "output contains private value '$forbidden'" ;;
	esac
}

assert_fails() {
	if "$@" >/dev/null 2>&1; then
		fail "command unexpectedly succeeded: $*"
	fi
}

YEET_VM_BOOT_BENCHMARK_LIBRARY=1 source "$driver"

assert_eq 1500 "$(phase_ms 1000000 ssh "$fixtures/boot-complete.log")"
assert_eq 50 "$(phase_ms 1000000 microvm "$fixtures/boot-complete.log")"
assert_eq 900 "$(phase_ms 1000000 early-ssh "$fixtures/boot-complete.log")"
assert_eq 1550 "$(phase_ms 1000000 ready "$fixtures/boot-complete.log")"
assert_eq 1600 "$(phase_ms 1000000 multi-user "$fixtures/boot-complete.log")"
assert_fails phase_ms 1000000 ssh "$fixtures/boot-incomplete.log"

assert_eq 50 "$(percentile 50 10 20 30 40 50 60 70 80 90 100)"
assert_eq 100 "$(percentile 95 10 20 30 40 50 60 70 80 90 100)"
assert_fails percentile 95
assert_fails percentile 0 10
assert_eq 2000 "$(bounded_deadline_ms 1000 5000 1000)"
assert_eq 1500 "$(bounded_deadline_ms 1000 1500 1000)"
assert_eq fixture "$(run_command_with_timeout 1000 printf fixture)"
timeout_start_ms="$(now_ms)"
assert_fails run_command_with_timeout 100 sh -c 'sleep 10'
timeout_elapsed_ms=$(($(now_ms) - timeout_start_ms))
[ "$timeout_elapsed_ms" -lt 1000 ] || fail "timed command took ${timeout_elapsed_ms}ms"

validate_service_name yeet-bootbench-fixture
assert_fails validate_service_name production-name
validate_iterations 5
assert_fails validate_iterations 1
assert_fails validate_iterations not-a-number

private_service=yeet-bootbench-private
private_catch=catch-private.example
private_machine=root@machine-private.example
redacted="$(printf '%s\n' \
	"service=$private_service catch=$private_catch machine=$private_machine ip=192.0.2.10" |
	redact_text "$private_service" "$private_catch" "$private_machine")"
assert_not_contains "$redacted" "$private_service"
assert_not_contains "$redacted" "$private_catch"
assert_not_contains "$redacted" "$private_machine"
assert_not_contains "$redacted" "192.0.2.10"
assert_eq 'service=<service> catch=<catch-host> machine=<machine-host> ip=<ipv4>' "$redacted"

binary_json="$(jq -nc --arg message "service=$private_service host=machine-private.example ip=192.0.2.10" \
	'{__MONOTONIC_TIMESTAMP:"1000000",MESSAGE:($message | explode)}')"
redacted_json="$(printf '%s\n' "$binary_json" | redact_text "$private_service" "$private_catch" "$private_machine")"
assert_eq 'service=<service> host=<machine-host> ip=<ipv4>' "$(jq -r .MESSAGE <<<"$redacted_json")"
assert_not_contains "$redacted_json" "$private_service"
assert_not_contains "$redacted_json" "machine-private.example"

echo "VM boot benchmark fixtures verified"
