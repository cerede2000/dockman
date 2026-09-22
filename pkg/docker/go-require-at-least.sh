#!/bin/sh
# go-require-at-least MODULE@VERSION...
#
# Raises requirements of the Go module in the working directory, as
# `go mod edit -require` does, but never lowers one.
#
# The image rebuilds Compose, sops and age from their release sources with
# security floors on some dependencies. `go mod edit -require` sets a version,
# it does not raise one: a floor written for one release silently downgrades
# the next release that already ships something newer. Compose 5.5.1 ships
# containerd v2.3.4; the v2.2.8 floor written for Compose 5.3.1 would have
# replaced it with an older line. So a floor below what the release selects
# fails the build, and so does a floor on a module the release no longer
# uses: an override must not outlive its purpose unnoticed.
set -eu

selected=$(go list -m -f '{{.Path}} {{.Version}}' all)

for spec in "$@"; do
	module=${spec%@*}
	wanted=${spec##*@}
	current=$(printf '%s\n' "$selected" | awk -v m="$module" '$1 == m { print $2; exit }')
	if [ -z "$current" ]; then
		echo "go-require-at-least: $module is no longer a dependency; drop its floor" >&2
		exit 1
	fi
	lowest=$(printf '%s\n%s\n' "$current" "$wanted" | sort -V | head -n 1)
	if [ "$wanted" != "$current" ] && [ "$lowest" = "$wanted" ]; then
		echo "go-require-at-least: refusing to lower $module from $current to $wanted" >&2
		exit 1
	fi
	echo "go-require-at-least: $module $current -> $wanted"
	go mod edit -require="$spec"
done
