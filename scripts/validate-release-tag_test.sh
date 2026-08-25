#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
validator="${repository_root}/scripts/validate-release-tag.sh"
temporary_directory="$(mktemp -d)"
trap 'rm -rf "${temporary_directory}"' EXIT

git -C "${temporary_directory}" init --quiet
git -C "${temporary_directory}" config user.email test@example.com
git -C "${temporary_directory}" config user.name "Release Test"
printf 'base\n' >"${temporary_directory}/file"
git -C "${temporary_directory}" add file
git -C "${temporary_directory}" commit --quiet -m base
target_sha="$(git -C "${temporary_directory}" rev-parse HEAD)"
printf 'main\n' >>"${temporary_directory}/file"
git -C "${temporary_directory}" commit --quiet -am main
git -C "${temporary_directory}" update-ref refs/remotes/origin/main HEAD

cat >"${temporary_directory}/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
endpoint="${*: -1}"
case "${endpoint}" in
  repos/test/repo/git/ref/tags/*)
    printf '{"object":{"type":"%s","sha":"tag-object"}}\n' "${FAKE_TAG_KIND:-tag}"
    ;;
  repos/test/repo/git/tags/tag-object)
    printf '{"verification":{"verified":%s},"object":{"type":"%s","sha":"%s"}}\n' \
      "${FAKE_VERIFIED:-true}" "${FAKE_TARGET_KIND:-commit}" "${FAKE_TARGET_SHA}"
    ;;
  repos/test/repo/commits/*/check-runs*)
    printf '{"check_runs":[{"name":"%s","conclusion":"%s","app":{"slug":"%s"}}]}\n' \
      "${FAKE_CHECK_NAME:-pr}" "${FAKE_CHECK_CONCLUSION:-success}" "${FAKE_CHECK_APP:-github-actions}"
    ;;
  'repos/test/repo/releases?per_page=100')
    if [[ "${FAKE_RELEASE_STATE:-missing}" == "missing" ]]; then
      printf '[]\n'
      exit 0
    fi
    if [[ "${FAKE_RELEASE_STATE}" == "draft" ]]; then
      printf '[{"tag_name":"v1.2.3","draft":true,"prerelease":false}]\n'
    else
      printf '[{"tag_name":"v1.2.3","draft":false,"prerelease":false}]\n'
    fi
    ;;
  *)
    printf 'unexpected endpoint: %s\n' "${endpoint}" >&2
    exit 2
    ;;
esac
EOF
chmod +x "${temporary_directory}/gh"

run_validator() {
  (
    cd "${temporary_directory}"
    PATH="${temporary_directory}:${PATH}" \
      RELEASE_REPOSITORY=test/repo \
      RELEASE_MAIN_REF=origin/main \
      FAKE_TARGET_SHA="${FAKE_TARGET_SHA:-${target_sha}}" \
      "${validator}" "$@"
  )
}

expect_pass() {
  local description="$1"
  shift
  if ! output="$("$@" 2>&1)"; then
    printf 'expected success (%s): %s\n' "${description}" "${output}" >&2
    exit 1
  fi
}

expect_fail() {
  local description="$1"
  shift
  if output="$("$@" 2>&1)"; then
    printf 'expected failure (%s): %s\n' "${description}" "${output}" >&2
    exit 1
  fi
}

stable_output="$(run_validator v1.2.3)"
grep -qx 'version=1.2.3' <<<"${stable_output}"
grep -qx 'prerelease=false' <<<"${stable_output}"
prerelease_output="$(run_validator v1.2.3-rc.1)"
grep -qx 'version=1.2.3-rc.1' <<<"${prerelease_output}"
grep -qx 'prerelease=true' <<<"${prerelease_output}"
expect_fail "missing v prefix" run_validator 1.2.3
expect_fail "incomplete version" run_validator v1.2
expect_fail "leading zero" run_validator v1.02.3
if FAKE_TAG_KIND=commit run_validator v1.2.3 >/dev/null 2>&1; then
  printf 'expected lightweight tag failure\n' >&2
  exit 1
fi
if FAKE_VERIFIED=false run_validator v1.2.3 >/dev/null 2>&1; then
  printf 'expected unverified tag failure\n' >&2
  exit 1
fi
if FAKE_TARGET_KIND=tag run_validator v1.2.3 >/dev/null 2>&1; then
  printf 'expected non-commit target failure\n' >&2
  exit 1
fi
if FAKE_CHECK_CONCLUSION=failure run_validator v1.2.3 >/dev/null 2>&1; then
  printf 'expected failed check rejection\n' >&2
  exit 1
fi
if FAKE_CHECK_APP=other run_validator v1.2.3 >/dev/null 2>&1; then
  printf 'expected foreign check rejection\n' >&2
  exit 1
fi
if FAKE_RELEASE_STATE=draft run_validator v1.2.3 >/dev/null 2>&1; then
  printf 'expected existing draft rejection\n' >&2
  exit 1
fi
if FAKE_RELEASE_STATE=published run_validator v1.2.3 >/dev/null 2>&1; then
  printf 'expected published release rejection\n' >&2
  exit 1
fi

unreachable_sha="$(git -C "${temporary_directory}" commit-tree "$(git -C "${temporary_directory}" rev-parse HEAD^{tree})" -m unreachable)"
if FAKE_TARGET_SHA="${unreachable_sha}" run_validator v1.2.3 >/dev/null 2>&1; then
  printf 'expected non-main commit rejection\n' >&2
  exit 1
fi

printf 'release tag validation tests passed\n'
