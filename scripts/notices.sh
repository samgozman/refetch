#!/bin/sh
# Writes THIRD_PARTY_NOTICES to stdout: the license text of the Go standard
# library and of every module compiled into cmd/refetch on any release target. A module that ships
# no license file fails the script, except forks of Go's own packages, whose
# files carry the Go Authors' BSD notice (listed in GO_FORKS).
set -eu
GO_FORKS="github.com/bogdanfinn/fhttp"
# Go's LICENSE: in GOROOT, or (Homebrew moves it out) the identical text
# every golang.org/x module ships.
golicense=$(go env GOROOT)/LICENSE
[ -f "$golicense" ] || golicense=$(go list -m -f '{{.Dir}}' golang.org/x/net)/LICENSE

echo "refetch includes the following third-party software. Each component's"
echo "license text follows its name."
echo
echo "================================================================================"
echo "The Go standard library"
echo
cat "$golicense"

# Every release target, so each archive carries every license compiled into
# it and the file does not depend on the machine that generates it.
for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
  GOOS=${target%/*} GOARCH=${target#*/} go list -deps \
    -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' ./cmd/refetch
done |
  sort -u |
  while read -r path version dir; do
    lic=$(find "$dir" -maxdepth 1 -type f \( -iname 'LICEN[CS]E*' -o -iname 'COPYING*' \) | sort | head -1)
    note=""
    if [ -z "$lic" ]; then
      case " $GO_FORKS " in
        *" $path "*) lic="$golicense"; note="(a fork of Go's net/http; its files carry the Go Authors' BSD notice)" ;;
        *) echo "notices: no license file in $path $version" >&2; exit 1 ;;
      esac
    fi
    echo
    echo "================================================================================"
    echo "$path $version $note"
    echo
    cat "$lic"
  done
