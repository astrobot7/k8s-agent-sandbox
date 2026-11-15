#!/usr/bin/env bash
# Downloads the pinned foundryctl (SigNoz Foundry) for this machine into
# .cache/foundry/bin, verifying the release's SHA-256 checksum.
set -euo pipefail

FOUNDRY_VERSION="${FOUNDRY_VERSION:-0.5.0}"
OUT_DIR="${OUT_DIR:-.cache/foundry}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  arm64 | aarch64) arch=arm64 ;;
  x86_64 | amd64) arch=amd64 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

stamp="${OUT_DIR}/.release"
if [[ -x "${OUT_DIR}/bin/foundryctl" && -f "$stamp" && "$(cat "$stamp")" == "${FOUNDRY_VERSION}/${os}-${arch}" ]]; then
  echo "foundryctl ${FOUNDRY_VERSION} (${os}-${arch}) already present in ${OUT_DIR}"
  exit 0
fi

asset="foundry_${os}_${arch}.tar.gz"
base="https://github.com/SigNoz/foundry/releases/download/v${FOUNDRY_VERSION}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

curl -fsSL -o "${tmp}/${asset}" "${base}/${asset}"
curl -fsSL -o "${tmp}/checksums.txt" "${base}/foundry_${FOUNDRY_VERSION}_checksums.txt"
(cd "$tmp" && grep " ${asset}\$" checksums.txt | shasum -a 256 -c -)

mkdir -p "${tmp}/x"
tar -xzf "${tmp}/${asset}" -C "${tmp}/x"
bin=$(find "${tmp}/x" -type f -name foundryctl -perm -u+x | head -1)
[[ -n "$bin" ]] || { echo "foundryctl not found in ${asset}" >&2; exit 1; }

mkdir -p "${OUT_DIR}/bin"
install -m 0755 "$bin" "${OUT_DIR}/bin/foundryctl"
echo "${FOUNDRY_VERSION}/${os}-${arch}" > "$stamp"
echo "installed foundryctl ${FOUNDRY_VERSION} (${os}-${arch}) into ${OUT_DIR}/bin"
