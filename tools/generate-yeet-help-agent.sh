#!/usr/bin/env bash
# Copyright (c) 2025 AUTHORS All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

out="${1:-.codex/skills/yeet-cli/references/yeet-help-agent.md}"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

declare -A seen
queue=("")

seen_key() {
  local cmd="$1"
  if [[ -z "$cmd" ]]; then
    printf '__top_level__'
    return
  fi
  printf '%s' "$cmd"
}

run_help() {
  local cmd="$1"
  if [[ -z "$cmd" ]]; then
    mise exec -- go run ./cmd/yeet --help-agent
    return
  fi

  local args=()
  read -r -a args <<<"$cmd"
  mise exec -- go run ./cmd/yeet "${args[@]}" --help-agent
}

section_title() {
  local cmd="$1"
  local body="$2"

  if [[ -z "$cmd" ]]; then
    printf 'Top-level'
  elif grep -q '^## Commands$' <<<"$body"; then
    printf 'Group: %s' "$cmd"
  elif [[ "$cmd" == *" "* ]]; then
    printf 'Group Command: %s' "$cmd"
  else
    printf 'Command: %s' "$cmd"
  fi
}

extract_help_targets() {
  grep -Eo 'Run `yeet [^`]+ --help-agent`' |
    sed -E 's/^Run `yeet (.*) --help-agent`$/\1/'
}

{
  cat <<'HEADER'
# Yeet CLI --help-agent Outputs

Generated from this repo using:

```bash
tools/generate-yeet-help-agent.sh
```

If command behavior changes, rerun the generator and commit the updated
reference.

HEADER
} >"$tmp"

first_section=true
while ((${#queue[@]} > 0)); do
  cmd="${queue[0]}"
  queue=("${queue[@]:1}")
  key="$(seen_key "$cmd")"
  [[ -n "${seen[$key]+x}" ]] && continue
  seen[$key]=1

  body="$(run_help "$cmd")"
  title="$(section_title "$cmd" "$body")"

  {
    if [[ "$first_section" == true ]]; then
      first_section=false
    else
      printf '\n'
    fi
    printf '## %s\n\n' "$title"
    printf '````\n%s\n````\n' "$body"
  } >>"$tmp"

  while IFS= read -r target; do
    [[ -z "$target" ]] && continue
    target_key="$(seen_key "$target")"
    [[ -n "${seen[$target_key]+x}" ]] && continue
    queue+=("$target")
  done < <(printf '%s\n' "$body" | extract_help_targets)
done

mv "$tmp" "$out"
trap - EXIT
