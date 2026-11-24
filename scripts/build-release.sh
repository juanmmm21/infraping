#!/usr/bin/env bash
# Compila los binarios de una versión para todas las plataformas soportadas y deja en dist/
# los archivos comprimidos y SHA256SUMS. Lo usa el workflow de release y se puede lanzar en local.
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <version>   (e.g. v0.2.0)" >&2
  exit 2
fi
version="$1"
root="$(cd "$(dirname "$0")/.." && pwd)"
dist="$root/dist"
platforms=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)

rm -rf "$dist"
mkdir -p "$dist"

for platform in "${platforms[@]}"; do
  goos="${platform%/*}"
  goarch="${platform#*/}"
  name="infraping_${version#v}_${goos}_${goarch}"
  bin="infraping"
  [[ "$goos" == "windows" ]] && bin="infraping.exe"
  stage="$dist/$name"
  mkdir -p "$stage"
  echo "building $name"
  (cd "$root" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$stage/$bin" ./cmd/infraping)
  cp "$root/LICENSE" "$root/README.md" "$root/examples/infraping.example.yaml" "$stage/"
  # Windows espera .zip; el resto, .tar.gz para conservar el bit de ejecución.
  if [[ "$goos" == "windows" ]]; then
    (cd "$dist" && zip -qr "$name.zip" "$name")
  else
    tar -C "$dist" -czf "$dist/$name.tar.gz" "$name"
  fi
  rm -rf "$stage"
done

(cd "$dist" && sha256sum -- *.tar.gz *.zip > SHA256SUMS)
echo "artifacts in $dist:"
ls -1 "$dist"
