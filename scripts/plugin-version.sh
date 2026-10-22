#!/bin/sh
# Usage: scripts/plugin-version.sh <version> <checksums.txt>
# Points the plugin at a published release: writes <version> into
# .claude-plugin/plugin.json and the sha256 of each release archive into the
# shim's table. The release job commits the result; that commit, not the
# tag, is what Claude Code sees as an update.
set -eu
version=${1:?usage: plugin-version.sh <version> <checksums.txt>}
sums=${2:?usage: plugin-version.sh <version> <checksums.txt>}
version=${version#v}
case $version in
*[!0-9.]* | "" | .* | *.) echo "plugin-version: not a release version: $version" >&2; exit 1 ;;
esac

manifest=.claude-plugin/plugin.json
shim=bin/refetch
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# One "# <sha256>  <archive>" line per platform the shim can pick.
for platform in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
	asset="refetch_${version}_${platform}.tar.gz"
	line=$(awk -v a="$asset" '$2 == a && $1 ~ /^[0-9a-f]+$/ && length($1) == 64 { print "# " $1 "  " $2 }' "$sums")
	[ -n "$line" ] || { echo "plugin-version: no sha256 for $asset in $sums" >&2; exit 1; }
	echo "$line"
done >"$tmp/table"

awk -v table="$tmp/table" '
	/^# BEGIN SHA256$/ { print; while ((getline l < table) > 0) print l; skip = 1; next }
	/^# END SHA256$/ { skip = 0 }
	!skip { print }
' "$shim" >"$tmp/shim"
grep -q '^# BEGIN SHA256$' "$shim" && grep -q '^# END SHA256$' "$tmp/shim" || { echo "plugin-version: no sha256 table in $shim" >&2; exit 1; }
cat "$tmp/shim" >"$shim" # keeps the shim's mode

n=$(grep -c '^[[:space:]]*"version"[[:space:]]*:' "$manifest" || true)
[ "$n" = 1 ] || { echo "plugin-version: want one \"version\" line in $manifest, found $n" >&2; exit 1; }
sed "s/^\([[:space:]]*\"version\"[[:space:]]*:[[:space:]]*\"\)[^\"]*\"/\1$version\"/" "$manifest" >"$tmp/manifest"
cat "$tmp/manifest" >"$manifest"
