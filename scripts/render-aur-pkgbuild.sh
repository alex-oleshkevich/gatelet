#!/usr/bin/env bash
set -euo pipefail

usage() {
  printf 'usage: scripts/render-aur-pkgbuild.sh <version> <x86_64-sha256>\n' >&2
}

if [[ $# -ne 2 ]]; then
  usage
  exit 2
fi

version="$1"
x86_64_sha256="$2"

if [[ "$version" == v* ]]; then
  version="${version#v}"
fi

if [[ "$version" == *-* ]]; then
  printf 'error: AUR pkgver cannot contain hyphen: %s\n' "$version" >&2
  exit 1
fi

if [[ ! "$version" =~ ^[0-9]+[.][0-9]+[.][0-9]+([.][0-9A-Za-z_+.]+)?$ ]]; then
  printf 'error: invalid AUR pkgver: %s\n' "$version" >&2
  exit 1
fi

if [[ ! "$x86_64_sha256" =~ ^[0-9a-fA-F]{64}$ ]]; then
  printf 'error: invalid x86_64 sha256: %s\n' "$x86_64_sha256" >&2
  exit 1
fi

while IFS= read -r line; do
  case "$line" in
    pkgver=*)
      printf 'pkgver=%s\n' "$version"
      ;;
    sha256sums_x86_64=*)
      printf "sha256sums_x86_64=('%s')\n" "$x86_64_sha256"
      ;;
    *)
      printf '%s\n' "$line"
      ;;
  esac
done < PKGBUILD
