#!/usr/bin/env bash
set -euo pipefail

export LC_ALL=C

phase_timestamp_us() {
	local phase="$1"
	local log_file="$2"
	local needle required=""
	case "$phase" in
		firecracker) needle="Running Firecracker" ;;
		microvm) needle="Successfully started microvm" ;;
		kernel) needle="Linux version" ;;
		init) needle="Run /usr/local/lib/yeet-vm/yeet-init as init process" ;;
		early-ssh) needle="yeet-early-ssh" ;;
		ssh) needle="yeet early SSH daemon."; required="Started" ;;
		ready) needle="yeet-ready " ;;
		multi-user) needle="multi-user.target"; required="Reached target" ;;
		*) echo "unsupported boot phase: $phase" >&2; return 2 ;;
	esac

	jq -ser --arg needle "$needle" --arg required "$required" '
		def message:
			(.MESSAGE? // "") as $message |
			if ($message | type) == "array" then ($message | implode)
			elif ($message | type) == "string" then $message
			else ""
			end;
		[.[] |
			select(message | contains($needle)) |
			select($required == "" or (message | contains($required))) |
			.__MONOTONIC_TIMESTAMP | tonumber
		][0] // empty
	' "$log_file"
}

wait_for_ssh_gate() {
	local log_file="$1"
	local deadline_ms="$2"
	while [ "$(now_ms)" -lt "$deadline_ms" ]; do
		if phase_timestamp_us early-ssh "$log_file" >/dev/null 2>&1 ||
			phase_timestamp_us ready "$log_file" >/dev/null 2>&1; then
			return 0
		fi
		if [ -n "${journal_pid:-}" ] && ! kill -0 "$journal_pid" 2>/dev/null; then
			wait "$journal_pid" || true
			journal_pid=""
			return 1
		fi
		sleep 0.025
	done
	return 1
}

phase_ms() {
	local unit_start_us="$1"
	local phase="$2"
	local log_file="$3"
	local timestamp_us
	[[ "$unit_start_us" =~ ^[0-9]+$ ]] || { echo "invalid unit start timestamp: $unit_start_us" >&2; return 2; }
	timestamp_us="$(phase_timestamp_us "$phase" "$log_file")" || return
	if [ "$timestamp_us" -lt "$unit_start_us" ]; then
		echo "boot phase $phase predates unit start" >&2
		return 1
	fi
	echo $(((timestamp_us - unit_start_us + 500) / 1000))
}

percentile() {
	local percent="$1"
	shift
	[[ "$percent" =~ ^[0-9]+$ ]] && [ "$percent" -ge 1 ] && [ "$percent" -le 100 ] || {
		echo "percentile must be an integer from 1 to 100" >&2
		return 2
	}
	[ "$#" -gt 0 ] || { echo "percentile requires samples" >&2; return 2; }
	local value
	for value in "$@"; do
		[[ "$value" =~ ^[0-9]+$ ]] || { echo "invalid percentile sample: $value" >&2; return 2; }
	done
	local count="$#"
	local rank=$(((percent * count + 99) / 100))
	printf '%s\n' "$@" | sort -n | awk -v rank="$rank" 'NR == rank { print; exit }'
}

bounded_deadline_ms() {
	local now="$1"
	local deadline="$2"
	local grace="$3"
	local bounded=$((now + grace))
	if [ "$bounded" -lt "$deadline" ]; then
		echo "$bounded"
	else
		echo "$deadline"
	fi
}

run_command_with_timeout() {
	local timeout_ms="$1"
	shift
	python3 - "$timeout_ms" "$@" <<'PY'
import os
import signal
import subprocess
import sys

timeout = int(sys.argv[1]) / 1000
process = subprocess.Popen(sys.argv[2:], start_new_session=True)
try:
    status = process.wait(timeout=timeout)
except subprocess.TimeoutExpired:
    try:
        os.killpg(process.pid, signal.SIGTERM)
        process.wait(timeout=0.25)
    except (ProcessLookupError, subprocess.TimeoutExpired):
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait()
    raise SystemExit(124)
raise SystemExit(status)
PY
}

validate_service_name() {
	local service="$1"
	[[ "$service" =~ ^yeet-bootbench-[a-z0-9][a-z0-9-]*$ ]] || {
		echo "benchmark service must start with yeet-bootbench- and use lowercase letters, digits, or hyphens" >&2
		return 2
	}
}

validate_iterations() {
	local iterations="$1"
	[[ "$iterations" =~ ^[0-9]+$ ]] && [ "$iterations" -ge 5 ] || {
		echo "benchmark iterations must be an integer of at least 5" >&2
		return 2
	}
}

redact_text() {
	local service="$1"
	local catch_host="$2"
	local machine_host="$3"
	python3 -c '
import json
import re
import sys

service, catch_host, machine_host = sys.argv[1:]
machine_name = machine_host.rsplit("@", 1)[-1]
catch_name = catch_host.removeprefix("yeet-")
replacements = {
    service: "<service>",
    catch_host: "<catch-host>",
    machine_host: "<machine-host>",
    machine_name: "<machine-host>",
}
if catch_name != catch_host:
    replacements[catch_name] = "<catch-host>"
ordered = sorted(((key, value) for key, value in replacements.items() if key), reverse=True)

def sanitize_text(value):
    for private, replacement in ordered:
        value = value.replace(private, replacement)
    return re.sub(r"(?<![0-9])(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?![0-9])", "<ipv4>", value)

def sanitize_json(value, key=None):
    if key == "MESSAGE" and isinstance(value, list) and all(isinstance(item, int) and 0 <= item <= 255 for item in value):
        value = bytes(value).decode("utf-8", "replace")
    if isinstance(value, str):
        return sanitize_text(value)
    if isinstance(value, list):
        return [sanitize_json(item) for item in value]
    if isinstance(value, dict):
        return {item_key: sanitize_json(item_value, item_key) for item_key, item_value in value.items()}
    return value

for line in sys.stdin:
    try:
        value = json.loads(line)
    except json.JSONDecodeError:
        sys.stdout.write(sanitize_text(line))
        continue
    print(json.dumps(sanitize_json(value), separators=(",", ":")))
' "$service" "$catch_host" "$machine_host"
}

now_ms() {
	python3 -c 'import time; print(time.monotonic_ns() // 1_000_000)'
}

if [ "${YEET_VM_BOOT_BENCHMARK_LIBRARY:-0}" = 1 ]; then
	return 0 2>/dev/null || exit 0
fi

usage() {
	cat >&2 <<'EOF'
usage: scripts/vm-boot-benchmark.sh --catch-host <alias> --machine-host <ssh-target> --service <yeet-bootbench-name> --iterations <count> --output <directory>
EOF
	exit 2
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || { echo "missing required command: $1" >&2; exit 1; }
}

shell_quote_command() {
	local quoted=""
	local arg
	for arg in "$@"; do
		printf -v arg '%q' "$arg"
		quoted+="${quoted:+ }$arg"
	done
	printf '%s\n' "$quoted"
}

remote_exec() {
	local command
	command="$(shell_quote_command "$@")"
	ssh -o BatchMode=yes "$machine_host" "$command"
}

wait_for_phase() {
	local log_file="$1"
	local phase="$2"
	local deadline_ms="$3"
	while [ "$(now_ms)" -lt "$deadline_ms" ]; do
		if phase_timestamp_us "$phase" "$log_file" >/dev/null 2>&1; then
			return 0
		fi
		if [ -n "${journal_pid:-}" ] && ! kill -0 "$journal_pid" 2>/dev/null; then
			wait "$journal_pid" || true
			journal_pid=""
			return 1
		fi
		sleep 0.025
	done
	return 1
}

phase_or_null() {
	local phase="$1"
	local log_file="$2"
	phase_ms "$unit_start_us" "$phase" "$log_file" 2>/dev/null || echo null
}

probe_guest_ssh() {
	local deadline_ms="$1"
	local remaining_ms
	local output
	while [ "$(now_ms)" -lt "$deadline_ms" ]; do
		remaining_ms=$((deadline_ms - $(now_ms)))
		if [ "$remaining_ms" -gt 2000 ]; then
			remaining_ms=2000
		fi
		if output="$(run_command_with_timeout "$remaining_ms" "$yeet_bin" --host="$catch_host" ssh \
			-o BatchMode=yes -o ConnectTimeout=1 -o ConnectionAttempts=1 \
			"$service" -- cat /proc/uptime 2>/dev/null)"; then
			awk 'NR == 1 { printf "%d\n", ($1 * 1000) + 0.5; exit }' <<<"$output"
			return 0
		fi
		sleep 0.025
	done
	return 1
}

capture_guest_diagnostics() {
	local output_file="$1"
	run_command_with_timeout 5000 "$yeet_bin" --host="$catch_host" ssh "$service" -- sh -lc \
		'systemd-analyze time; systemd-analyze critical-chain; systemd-analyze blame; sudo dmesg' \
		2>&1 | redact_text "$service" "$catch_host" "$machine_host" >"$output_file"
}

stop_journal_follower() {
	if [ -n "${journal_pid:-}" ] && kill -0 "$journal_pid" 2>/dev/null; then
		kill "$journal_pid" 2>/dev/null || true
		wait "$journal_pid" 2>/dev/null || true
	fi
	journal_pid=""
}

catch_host=""
machine_host=""
service=""
iterations=""
output_dir=""
timeout_seconds="${YEET_VM_BOOT_BENCHMARK_TIMEOUT:-30}"
yeet_bin="${YEET_VM_BOOT_BENCHMARK_YEET:-}"

while [ "$#" -gt 0 ]; do
	case "$1" in
		--catch-host) catch_host="${2:-}"; shift 2 ;;
		--machine-host) machine_host="${2:-}"; shift 2 ;;
		--service) service="${2:-}"; shift 2 ;;
		--iterations) iterations="${2:-}"; shift 2 ;;
		--output) output_dir="${2:-}"; shift 2 ;;
		*) usage ;;
	esac
done

[ -n "$catch_host" ] && [ -n "$machine_host" ] && [ -n "$service" ] && [ -n "$iterations" ] && [ -n "$output_dir" ] || usage
validate_service_name "$service"
validate_iterations "$iterations"
[[ "$timeout_seconds" =~ ^[0-9]+$ ]] && [ "$timeout_seconds" -ge 5 ] || { echo "benchmark timeout must be at least 5 seconds" >&2; exit 2; }

for command in awk grep jq mktemp python3 sed sort ssh; do
	require_command "$command"
done

if [ -z "$yeet_bin" ]; then
	if [ -x "$(dirname "${BASH_SOURCE[0]}")/../bin/yeet" ]; then
		yeet_bin="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/bin/yeet"
	elif command -v yeet >/dev/null 2>&1; then
		yeet_bin="$(command -v yeet)"
	else
		echo "set YEET_VM_BOOT_BENCHMARK_YEET to a built yeet binary" >&2
		exit 1
	fi
fi
[ -x "$yeet_bin" ] || { echo "yeet binary is not executable: $yeet_bin" >&2; exit 1; }

if [ -e "$output_dir" ] && [ -n "$(find "$output_dir" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]; then
	echo "benchmark output directory must not already contain files: $output_dir" >&2
	exit 2
fi
mkdir -p "$output_dir"

work_dir="$(mktemp -d)"
journal_pid=""
cleanup() {
	stop_journal_follower
	rm -rf "$work_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

unit="yeet-vm-${service}.service"
samples_file="$work_dir/samples.json"
printf '[]\n' >"$samples_file"

run_boot() {
	local iteration="$1"
	local record="$2"
	local raw_log="$work_dir/boot-${iteration}.raw"
	local follower_err="$work_dir/boot-${iteration}.journal-error"
	local diagnostics="$work_dir/boot-${iteration}.diagnostics"
	local cursor
	local since
	local follow_command
	local deadline_ms
	local optional_phase_deadline_ms
	local guest_uptime_ms=null
	local microvm_timestamp_us=null
	local ssh_probe_ms=null
	local failure=""

	cursor="$(remote_exec journalctl -u "$unit" -n 1 -o json --no-pager 2>/dev/null | jq -sr '[.[] | .__CURSOR? // empty][0] // empty')"
	if [ -n "$cursor" ]; then
		follow_command="$(shell_quote_command journalctl -u "$unit" --after-cursor "$cursor" -f -o json --no-pager)"
	else
		since="$(remote_exec date +%s)"
		follow_command="$(shell_quote_command journalctl -u "$unit" --since "@$since" -f -o json --no-pager)"
	fi
	ssh -o BatchMode=yes "$machine_host" "$follow_command" >"$raw_log" 2>"$follower_err" &
	journal_pid=$!
	sleep 0.05

	if ! remote_exec systemctl restart "$unit"; then
		failure="unit restart failed"
	fi
	unit_start_us="$(remote_exec systemctl show "$unit" -p ExecMainStartTimestampMonotonic --value 2>/dev/null || true)"
	if ! [[ "$unit_start_us" =~ ^[0-9]+$ ]] || [ "$unit_start_us" -eq 0 ]; then
		failure="${failure:+$failure; }unit start timestamp unavailable"
		unit_start_us=0
	fi

	deadline_ms=$(($(now_ms) + timeout_seconds * 1000))
	if [ -z "$failure" ]; then
		if ! wait_for_ssh_gate "$raw_log" "$deadline_ms"; then
			failure="guest SSH gate timed out"
		elif guest_uptime_ms="$(probe_guest_ssh "$deadline_ms")"; then
			:
		else
			failure="SSH probe timed out"
		fi
	fi
	optional_phase_deadline_ms="$(bounded_deadline_ms "$(now_ms)" "$deadline_ms" 2000)"
	wait_for_phase "$raw_log" ready "$optional_phase_deadline_ms" || true
	wait_for_phase "$raw_log" multi-user "$optional_phase_deadline_ms" || true
	stop_journal_follower
	if [ "$guest_uptime_ms" != null ]; then
		microvm_timestamp_us="$(phase_timestamp_us microvm "$raw_log" 2>/dev/null || echo null)"
		if [ "$microvm_timestamp_us" = null ]; then
			failure="${failure:+$failure; }microVM start timestamp unavailable"
		else
			ssh_probe_ms=$(((microvm_timestamp_us - unit_start_us + 500) / 1000 + guest_uptime_ms))
		fi
	fi

	if [ "$record" = 0 ]; then
		return 0
	fi

	if [ "$guest_uptime_ms" != null ]; then
		capture_guest_diagnostics "$diagnostics" || true
	else
		: >"$diagnostics"
	fi
	redact_text "$service" "$catch_host" "$machine_host" <"$raw_log" >"$output_dir/boot-$(printf '%03d' "$iteration").log"
	if [ -s "$follower_err" ]; then
		redact_text "$service" "$catch_host" "$machine_host" <"$follower_err" >>"$output_dir/boot-$(printf '%03d' "$iteration").log"
	fi
	cp "$diagnostics" "$output_dir/boot-$(printf '%03d' "$iteration").diagnostics"

	local sample
	sample="$(jq -n \
		--argjson iteration "$iteration" \
		--argjson firecracker_ms "$(phase_or_null firecracker "$raw_log")" \
		--argjson kernel_ms "$(phase_or_null kernel "$raw_log")" \
		--argjson init_ms "$(phase_or_null init "$raw_log")" \
		--argjson ssh_daemon_ms "$(phase_or_null ssh "$raw_log")" \
		--argjson ssh_ms "$ssh_probe_ms" \
		--argjson guest_uptime_ms "$guest_uptime_ms" \
		--argjson ready_observation_ms "$(phase_or_null ready "$raw_log")" \
		--argjson multi_user_ms "$(phase_or_null multi-user "$raw_log")" \
		--arg failure "$failure" \
		'{iteration:$iteration,firecracker_ms:$firecracker_ms,kernel_ms:$kernel_ms,init_ms:$init_ms,ssh_daemon_ms:$ssh_daemon_ms,ssh_ms:$ssh_ms,guest_uptime_ms:$guest_uptime_ms,ready_observation_ms:$ready_observation_ms,multi_user_ms:$multi_user_ms,failure:(if $failure == "" then null else $failure end)}')"
	jq --argjson sample "$sample" '. + [$sample]' "$samples_file" >"$work_dir/samples.next"
	mv "$work_dir/samples.next" "$samples_file"
}

echo "Warming disposable VM $service..."
run_boot 0 0

for iteration in $(seq 1 "$iterations"); do
	echo "Measuring boot $iteration/$iterations..."
	run_boot "$iteration" 1
done

cp "$samples_file" "$output_dir/samples.json"
jq '
  def metric($name):
    [.[].[$name] | select(type == "number")] | sort |
    if length == 0 then null else {
      min: .[0],
      p50: .[((length * 50 + 99) / 100 | floor) - 1],
      p95: .[((length * 95 + 99) / 100 | floor) - 1],
      max: .[-1]
    } end;
  {
    schema_version: 1,
    iterations: length,
    ssh_ms: metric("ssh_ms"),
    ready_observation_ms: metric("ready_observation_ms"),
    multi_user_ms: metric("multi_user_ms"),
    failed_boots: ([.[] | select(.failure != null)] | length)
  }
' "$samples_file" >"$output_dir/summary.json"

jq . "$output_dir/summary.json"
