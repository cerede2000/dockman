#!/usr/bin/env bash
set -euo pipefail

# The image ships three Go binaries it does not build from this module:
# docker-compose, sops and age-keygen, each rebuilt from its release source
# with security floors on some dependencies (see pkg/docker/go-require-at-least.sh).
# check-govuln.sh covers the binaries built from core/; this covers those three.
#
# go-require-at-least already refuses a floor that would lower a version, and a
# floor on a module the release no longer uses. Neither tells us a floor has
# become too low, which is how a reachable containerd advisory (GO-2026-6597)
# sat in the shipped Compose binary unnoticed. This closes that direction.
#
# Everything comes from the Dockerfile: the release versions, the floor lists
# and the Go toolchain are read out of it, never restated here. A gate holding
# its own copy of the floors would drift from the image exactly as silently.

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dockerfile="$repository_root/pkg/docker/Dockerfile"
floor_script="$repository_root/pkg/docker/go-require-at-least.sh"
allowlist_file="$repository_root/security/govuln-thirdparty-allowlist.txt"

work_dir="$(mktemp -d)"
findings_file="$(mktemp)"
allowed_file="$(mktemp)"

cleanup() {
  chmod -R u+w "$work_dir" 2>/dev/null || true
  rm -rf "$work_dir"
  rm -f "$findings_file" "$allowed_file"
}
trap cleanup EXIT

# The value of an `ARG NAME=value` default, which is the single place a version
# is written down.
arg_value() {
  local name="$1" value
  value="$(sed -n "s/^ARG ${name}=\\(.*\\)\$/\\1/p" "$dockerfile" | head -n 1)"
  if [[ -z $value ]]; then
    echo "check-govuln-thirdparty: no ARG ${name} default in $dockerfile" >&2
    exit 1
  fi
  printf '%s\n' "$value"
}

# The floor specs the Dockerfile passes to go-require-at-least for one source
# tree, with their ${ARG} references expanded. Collected between `cd /<name>-source`
# and the `go mod tidy` that ends that tree's edits, so a restructured
# Dockerfile yields an empty list and fails loudly rather than scanning an
# unfloored release.
floors_for() {
  local name="$1" spec module var version
  while read -r spec; do
    module="${spec%@*}"
    var="${spec##*@}"
    var="${var#\$\{}"
    var="${var%\}}"
    version="$(arg_value "$var")"
    printf '%s@%s\n' "$module" "$version"
  done < <(awk -v name="$name" '
    $0 ~ "cd /" name "-source" { inside = 1; next }
    inside && /go mod tidy/ { inside = 0 }
    inside {
      line = $0
      while (match(line, /"[^"]+@\$\{[A-Z_]+\}"/)) {
        print substr(line, RSTART + 1, RLENGTH - 2)
        line = substr(line, RSTART + RLENGTH)
      }
    }
  ' "$dockerfile")
}

export GOTOOLCHAIN="go$(arg_value GO_VERSION)"
export GOOS=linux
export GOARCH=amd64
export GOFLAGS=-mod=mod

# name | module | ARG holding its version | package the Dockerfile builds
targets=(
  "compose|github.com/docker/compose/v5|COMPOSE_VERSION|./cmd"
  "sops|github.com/getsops/sops/v3|SOPS_VERSION|./cmd/sops"
  "age|filippo.io/age|AGE_VERSION|./cmd/age-keygen"
)

cd "$work_dir"

for target in "${targets[@]}"; do
  IFS='|' read -r name module version_arg package <<< "$target"
  version="$(arg_value "$version_arg")"

  floors=()
  while IFS= read -r floor; do
    floors+=("$floor")
  done < <(floors_for "$name")
  if [[ ${#floors[@]} -eq 0 ]]; then
    echo "check-govuln-thirdparty: no floors found for /${name}-source; the Dockerfile layout changed" >&2
    exit 1
  fi

  echo "== ${module}@${version} (${package})"

  go mod download "${module}@${version}"
  source_dir="$(go mod download -json "${module}@${version}" | jq -r .Dir)"
  cp -R "$source_dir" "$work_dir/$name"
  chmod -R u+w "$work_dir/$name"

  report_file="$work_dir/$name.json"
  (
    cd "$work_dir/$name"
    # Floor and tidy chatter belongs in the log, not in the findings.
    bash "$floor_script" "${floors[@]}" >&2
    go mod tidy >&2
    govulncheck -json "$package"
  ) > "$report_file"

  # govulncheck always emits its config; an empty or configless report means it
  # never analysed anything, which must not read as a clean binary.
  if ! jq -e -s 'any(.[]; has("config"))' "$report_file" > /dev/null; then
    echo "check-govuln-thirdparty: govulncheck produced no report for ${module}@${version}" >&2
    exit 1
  fi

  # A one-element trace is a module/package presence finding. Longer traces are
  # symbols reachable from the binary the image ships.
  jq -r 'select(.finding and (.finding.trace | length) > 1) | .finding.osv' \
    "$report_file" >> "$findings_file"
done

sort -u -o "$findings_file" "$findings_file"
sed -E '/^[[:space:]]*(#|$)/d' "$allowlist_file" | sort -u > "$allowed_file"

unexpected="$(comm -23 "$findings_file" "$allowed_file")"
stale="$(comm -13 "$findings_file" "$allowed_file")"

if [[ -n $unexpected ]]; then
  echo "Unexpected reachable Go vulnerabilities in a rebuilt third-party binary:" >&2
  echo "$unexpected" >&2
  echo "Raise the matching floor in pkg/docker/Dockerfile, or review and allowlist it." >&2
  exit 1
fi

if [[ -n $stale ]]; then
  echo "Resolved or unreachable allowlist entries must be removed:" >&2
  echo "$stale" >&2
  exit 1
fi

if [[ -s $findings_file ]]; then
  echo "Only reviewed, currently-unfixed third-party findings remain:"
  cat "$findings_file"
else
  echo "No reachable Go vulnerabilities in the rebuilt third-party binaries."
fi
