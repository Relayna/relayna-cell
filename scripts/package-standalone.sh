#!/usr/bin/env bash
# Build standalone release artifacts locally.
set -euo pipefail
cd "$(dirname "$0")/.."

arch=${1:-}
out_dir=${2:-bin/standalone}
if [[ ( "$arch" != arm64 && "$arch" != amd64 ) || $# -gt 2 ]]; then
  echo "usage: $0 {amd64|arm64} [output-directory]" >&2
  exit 2
fi

mkdir -p "$out_dir"
out_dir=$(cd "$out_dir" && pwd)
staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT
name="relayna-cell-linux-$arch"
bundle="$staging/$name"
mkdir -p "$bundle/bin" "$bundle/deploy" "$bundle/docs" "$bundle/examples"

for program in cell cell-init cell-server cell-task; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o "$bundle/bin/$program" "./cmd/$program"
done
cp LICENSE "$bundle/"
cp docs/linux-setup.md docs/standalone-packaging.md "$bundle/docs/"
cp -R deploy/runtime "$bundle/deploy/"
cp -R examples/relayna-sdk-task "$bundle/examples/"

COPYFILE_DISABLE=1 tar -C "$staging" -czf "$staging/$name.tar.gz" "$name"
(
  cd "$staging"
  if command -v sha256sum >/dev/null; then
    sha256sum "$name.tar.gz"
  else
    shasum -a 256 "$name.tar.gz"
  fi
) > "$staging/$name.tar.gz.sha256"
mv "$staging/$name.tar.gz" "$staging/$name.tar.gz.sha256" "$out_dir/"
printf '%s\n' "$out_dir/$name.tar.gz" "$out_dir/$name.tar.gz.sha256"
