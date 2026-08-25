<!--
Thank you for contributing to TendKit.
Read CONTRIBUTING.md before submitting. Remove sections that do not apply.
For the Simplified Chinese template, use .github/PULL_REQUEST_TEMPLATE/pull_request_zh_cn.md.
Do not disclose an undisclosed vulnerability in a pull request; follow SECURITY.md.
-->

## Summary

<!-- What problem does this change solve, and what is the user-visible result? -->

## Scope and approach

<!-- Describe the important implementation choices, boundaries, and anything intentionally left out. -->

## Related issue

<!-- Use "Closes #123" when this PR should close an issue. -->

## Verification

<!-- List the exact commands and results. Do not mark an unrun check as passing. -->

- [ ] Focused tests
- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] `go build ./...`
- [ ] `git diff --check`
- [ ] `scripts/verify-go-quality.sh` (when pinned tools are available)

Commands and results:

```text

```

## Risk and compatibility

<!-- Note configuration/schema, CLI, platform, security, migration, rollback, or performance impact. Write "None" when applicable. -->

## UI evidence

<!-- For visible TUI changes, add sanitized screenshots or a short recording. Otherwise remove this section. -->

## Checklist

- [ ] The change is focused and contains no unrelated cleanup.
- [ ] Behavior changes have relevant tests.
- [ ] User-visible or contract changes have relevant documentation and translations.
- [ ] Logs, examples, and fixtures contain no secrets or private data.
- [ ] Remaining untested behavior and risks are described above.

<!-- Preferred title: <type>[optional scope]: <description> -->
