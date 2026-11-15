#!/usr/bin/env bash
# Downloads the pinned gVisor release (runsc, containerd shim, gvisor-bin
# sidecars) for the Docker VM's architecture into .cache/gvisor, verifying the
# tarball's SHA-512 checksum.
# The kind config mounts them into every node.
set -euo pipefail

GVISOR_RELEASE="${GVISOR_RELEASE:-20261109.0}"
OUT_DIR="${OUT_DIR:-.cache/gvisor}"

case "$(docker info --format '{{.Architecture}}')" in
  aarch64 | arm64) arch=aarch64 ;;
  x86_64 | amd64) arch=x86_64 ;;
  *) echo "unsupported Docker architecture" >&2; exit 1 ;;
esac

stamp="${OUT_DIR}/.release"
if [[ -f "$stamp" && "$(cat "$stamp")" == "${GVISOR_RELEASE}/${arch}" ]]; then
  echo "gVisor ${GVISOR_RELEASE} (${arch}) already present in ${OUT_DIR}"
  exit 0
fi

base="https://storage.googleapis.com/gvisor/releases/release/${GVISOR_RELEASE}/${arch}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Releases ship as one tarball: runsc, containerd-shim-runsc-v1, and gvisor-bin/
# (sidecars such as gvisor_sentry, which runsc expects next to itself).
curl -fsSL -o "${tmp}/gvisor.tar.bz2" "${base}/gvisor.tar.bz2"
curl -fsSL -o "${tmp}/gvisor.tar.bz2.sha512" "${base}/gvisor.tar.bz2.sha512"
(cd "$tmp" && shasum -a 512 -c gvisor.tar.bz2.sha512)

mkdir -p "${tmp}/x"
tar -xjf "${tmp}/gvisor.tar.bz2" -C "${tmp}/x" runsc containerd-shim-runsc-v1 gvisor-bin

rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"
mv "${tmp}/x/runsc" "${tmp}/x/containerd-shim-runsc-v1" "${tmp}/x/gvisor-bin" "$OUT_DIR/"
echo "${GVISOR_RELEASE}/${arch}" > "$stamp"
echo "installed gVisor ${GVISOR_RELEASE} (${arch}) into ${OUT_DIR}"
