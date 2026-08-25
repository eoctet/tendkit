#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'release validation failed: %s\n' "$*" >&2
  exit 1
}

api() {
  gh api --method GET "$1"
}

tag="${1:-${GITHUB_REF_NAME:-}}"
repository="${RELEASE_REPOSITORY:-${GITHUB_REPOSITORY:-}}"
main_ref="${RELEASE_MAIN_REF:-origin/main}"

[[ -n "${tag}" ]] || die "tag is required"
[[ -n "${repository}" ]] || die "repository is required"

semver_pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
if ! LC_ALL=C grep -Eq "${semver_pattern}" <<<"${tag}"; then
  die "tag ${tag} is not a supported v-prefixed SemVer"
fi

ref_json="$(api "repos/${repository}/git/ref/tags/${tag}")" || die "cannot read tag ref ${tag}"
ref_type="$(jq -er '.object.type' <<<"${ref_json}")" || die "tag ref response has no object type"
[[ "${ref_type}" == "tag" ]] || die "${tag} is a lightweight tag"

tag_object_sha="$(jq -er '.object.sha' <<<"${ref_json}")" || die "tag ref response has no object SHA"
tag_json="$(api "repos/${repository}/git/tags/${tag_object_sha}")" || die "cannot read annotated tag object"
verified="$(jq -er '.verification.verified' <<<"${tag_json}")" || die "tag response has no verification result"
[[ "${verified}" == "true" ]] || die "annotated tag signature is not verified by GitHub"

target_type="$(jq -er '.object.type' <<<"${tag_json}")" || die "tag response has no target type"
[[ "${target_type}" == "commit" ]] || die "annotated tag must point directly to a commit"
target_sha="$(jq -er '.object.sha' <<<"${tag_json}")" || die "tag response has no target SHA"

git cat-file -e "${target_sha}^{commit}" 2>/dev/null || die "target commit is not present in the checkout"
git merge-base --is-ancestor "${target_sha}" "${main_ref}" || die "target commit is not reachable from ${main_ref}"

checks_json="$(api "repos/${repository}/commits/${target_sha}/check-runs?per_page=100")" || die "cannot read check runs"
if ! jq -e 'any(.check_runs[]?; .name == "pr" and .app.slug == "github-actions" and .conclusion == "success")' \
  >/dev/null <<<"${checks_json}"; then
  die "target commit has no successful GitHub Actions pr check"
fi

releases_endpoint="repos/${repository}/releases?per_page=100"
release_pages="$(gh api --method GET --paginate "${releases_endpoint}")" || \
  die "cannot list existing Releases"
release_matches="$(jq -ce --arg tag "${tag}" -s \
  '[.[][] | select(.tag_name == $tag)]' <<<"${release_pages}")" || \
  die "cannot inspect existing Releases"
release_count="$(jq -r 'length' <<<"${release_matches}")"
if [[ "${release_count}" -gt 1 ]]; then
  die "multiple Releases exist for ${tag}"
fi
if [[ "${release_count}" -eq 1 ]]; then
  if jq -e '.[0].draft == true' >/dev/null <<<"${release_matches}"; then
    die "a Draft Release already exists for ${tag}; preserve it for diagnosis"
  fi
  die "a published Release already exists for ${tag}"
fi

version="${tag#v}"
version_without_build="${version%%+*}"
prerelease=false
if [[ "${version_without_build}" == *-* ]]; then
  prerelease=true
fi

output="target_sha=${target_sha}
version=${version}
prerelease=${prerelease}"
printf '%s\n' "${output}"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  printf '%s\n' "${output}" >>"${GITHUB_OUTPUT}"
fi
