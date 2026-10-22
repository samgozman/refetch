#!/bin/sh
# Usage: scripts/plugin-version.sh <version> <dir>
# <dir> holds the release's checksums.txt and its four archives. Points the
# plugin at that release: writes <version> into .claude-plugin/plugin.json
# and, into the shim's table, the sha256 of each archive and of the refetch
# binary inside it (the shim checks the cached binary on every run). The
# release job commits the result to the release branch.
set -eu
usage="usage: plugin-version.sh <version> <dir with checksums.txt and the archives>"
version=${1:?$usage}
dir=${2:?$usage}
version=${version#v}
case $version in
*[!0-9.]* | "" | .* | *.) echo "plugin-version: not a release version: $version" >&2; exit 1 ;;
esac

manifest=.claude-plugin/plugin.json
shim=bin/refetch
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

for platform in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
	name="refetch_${version}_${platform}"
	archive="$dir/$name.tar.gz"
	want=$(awk -v a="$name.tar.gz" '$2 == a && $1 ~ /^[0-9a-f]+$/ && length($1) == 64 { print $1 }' "$dir/checksums.txt")
	[ -n "$want" ] || { echo "plugin-version: no sha256 for $name.tar.gz in checksums.txt" >&2; exit 1; }
	[ -f "$archive" ] || { echo "plugin-version: missing $archive" >&2; exit 1; }
	got=$(sha256 "$archive")
	[ "$got" = "$want" ] || { echo "plugin-version: $name.tar.gz does not match checksums.txt" >&2; exit 1; }
	mkdir "$tmp/$platform"
	tar -xzf "$archive" -C "$tmp/$platform" refetch || { echo "plugin-version: no refetch binary in $name.tar.gz" >&2; exit 1; }
	echo "# $want  $name.tar.gz"
	echo "# $(sha256 "$tmp/$platform/refetch")  $name"
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
