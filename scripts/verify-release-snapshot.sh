#!/usr/bin/env bash
set -euo pipefail

dist_directory="${1:-dist}"
[[ -d "${dist_directory}" ]] || {
  printf 'missing dist directory: %s\n' "${dist_directory}" >&2
  exit 1
}

archives=("${dist_directory}"/*.tar.gz)
[[ ${#archives[@]} -eq 4 && -e "${archives[0]}" ]] || {
  printf 'expected exactly four release archives\n' >&2
  exit 1
}
[[ -f "${dist_directory}/checksums.txt" ]] || {
  printf 'missing checksums.txt\n' >&2
  exit 1
}

declare -a expected_suffixes=(
  darwin_arm64.tar.gz
  darwin_x86_64.tar.gz
  linux_arm64.tar.gz
  linux_x86_64.tar.gz
)
for suffix in "${expected_suffixes[@]}"; do
  matches=("${dist_directory}"/tendkit_*_"${suffix}")
  [[ ${#matches[@]} -eq 1 && -f "${matches[0]}" ]] || {
    printf 'missing or duplicate archive suffix: %s\n' "${suffix}" >&2
    exit 1
  }
done

expected_contents=$'LICENSE\nREADME.md\nREADME_ZH_CN.md\ntendkit'
for archive in "${archives[@]}"; do
  actual_contents="$(tar -tzf "${archive}" | sed 's#^\./##' | sort)"
  [[ "${actual_contents}" == "${expected_contents}" ]] || {
    printf 'unexpected archive contents: %s\n%s\n' "${archive}" "${actual_contents}" >&2
    exit 1
  }
done

(
  cd "${dist_directory}"
  shasum -a 256 -c checksums.txt
)

case "$(uname -m)" in
  arm64) host_arch=arm64 ;;
  x86_64) host_arch=x86_64 ;;
  *)
    printf 'unsupported verification host architecture: %s\n' "$(uname -m)" >&2
    exit 1
    ;;
esac

host_archive=("${dist_directory}"/tendkit_*_darwin_"${host_arch}".tar.gz)
[[ ${#host_archive[@]} -eq 1 && -f "${host_archive[0]}" ]] || {
  printf 'missing host archive for %s\n' "${host_arch}" >&2
  exit 1
}
version="${host_archive[0]#${dist_directory}/tendkit_}"
version="${version%_darwin_${host_arch}.tar.gz}"
temporary_binary="$(mktemp "${TMPDIR:-/tmp}/tendkit-release.XXXXXX")"
trap 'rm -f "${temporary_binary}"' EXIT
tar -xOf "${host_archive[0]}" tendkit >"${temporary_binary}"
chmod +x "${temporary_binary}"
[[ "$("${temporary_binary}" version --no-env-file)" == "tendkit ${version}" ]]
"${temporary_binary}" --help >/dev/null

printf 'release snapshot verification passed\n'
