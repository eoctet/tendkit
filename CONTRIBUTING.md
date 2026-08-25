# Contributing to TendKit

[English](CONTRIBUTING.md) | [简体中文](CONTRIBUTING_ZH_CN.md)

Thank you for helping improve TendKit. This guide describes how to report problems, propose changes, prepare code, and submit a reviewable pull request.

## Before you start

- Search existing issues and pull requests before opening a duplicate.
- Use an Issue Form for bugs and feature requests, and include only information that is safe to publish.
- Do not disclose vulnerabilities, credentials, tokens, cryptographic keys, private paths, or unredacted logs in a public issue. Follow [SECURITY.md](SECURITY.md) for security reports.
- For a material feature, public interface or configuration/schema change, migration, security-boundary change, or cross-component design, open an issue and agree on the scope with maintainers before implementation.
- Keep each contribution focused on one problem. Unrelated cleanup should be proposed separately.

Small corrections to documentation, tests, or clearly local behavior can normally be submitted directly. Maintainers may ask for an issue first when a change affects long-term product or architecture contracts.

## Development setup

Required tools:

- Git
- Go 1.23 or later; use the toolchain declared by [`go.mod`](go.mod)
- Python 3 for repository JSON checks
- macOS or a supported Linux environment; macOS provides the complete platform, Application Bundle, and PTY test surface, while Linux supports the non-macOS implementation and tests

Optional quality tools and their pinned versions are listed in [`scripts/verify-go-quality.sh`](scripts/verify-go-quality.sh): `staticcheck`, `govulncheck`, and `gosec`. The script reports installation commands if they are missing; it does not install or upgrade tools automatically.

```bash
git clone https://github.com/eoctet/tendkit.git
cd tendkit
go test ./...
go build ./...
```

Tests must not update real software, modify a real TendKit catalog, or depend on live provider responses. Use temporary directories and local test servers or fakes.

## Project contracts and layout

Use these sources before changing behavior:

- [`README.md`](README.md) and the public [user manual](wiki/user-manual.md) define user-visible product behavior and boundaries.
- This guide defines the contribution workflow; the public [development and technical guide](wiki/development-and-technical-guide.md) defines detailed engineering and technology standards.
- [`internal/config/template/default_config.json`](internal/config/template/default_config.json), strict parsing, and tests define the configuration contract.
- Executable code and tests represent the current implementation.

Key packages:

- `cmd/tendkit`: process entry point, CLI flags, signals, exit codes, and dependency assembly
- `internal/service`: scan and update transactions and persistence boundaries
- `internal/model`: shared domain vocabulary and limits
- `internal/config`: strict JSON parsing, locking, snapshots, and atomic writes
- `internal/scanner`: discovery, identity, exclusion, and merge behavior
- `internal/updater`: provider resolution, concurrency, downloads, verification, and update execution
- `internal/ui`: single-event-loop TUI state machine
- `pkg/*`: reusable runtime, HTTP, logging, i18n, download, metadata, error, and version helpers

If implementation and documentation disagree, call out the conflict instead of silently changing a public contract.

## Making a change

1. Reproduce the problem or establish the acceptance condition.
2. For behavior changes, add a focused test that fails for the intended reason.
3. Make the smallest change that satisfies the condition; avoid speculative configuration, dependencies, and abstractions.
4. Update affected public user or contributor documentation, configuration examples, and both language versions.
5. Run focused tests first, then broader checks in proportion to risk.

Go code must be formatted with `gofmt`. Follow existing package boundaries and error vocabulary. New providers should implement only real supported capabilities; new scan domains must preserve cancellation, incomplete-list protection, identity, exclusion, and merge semantics. Configuration and command execution are security-sensitive areas and require focused negative tests.

## Testing and quality checks

Run the smallest relevant package test while iterating, for example:

```bash
go test ./internal/config -run TestName -count=1
```

Before opening a pull request, run at least:

```bash
gofmt -w <changed-go-files>
go test ./...
go vet ./...
go build ./...
git diff --check
```

When the pinned quality tools are available, run the complete repository check:

```bash
scripts/verify-go-quality.sh
```

The complete check includes formatting verification, unit and integration tests, the race detector, `go vet`, builds, static analysis, vulnerability and security scanning, JSON validation, and Git whitespace checks. If a platform or tool prevents a check, describe the exact command, failure, environment, and remaining risk in the pull request; do not report an unrun check as passing.

Repository automation uses three layers. [Test](.github/workflows/test.yml) validates pull requests and pushes to `main` with focused and full tests, race checks for the TUI, `go vet`, builds, and a release snapshot. [Nightly](.github/workflows/nightly.yml) adds the full race suite and repeated PTY/TUI platform tests. [Release](.github/workflows/release.yml) accepts only signed, annotated `v`-prefixed SemVer tags that belong to `main` and have a successful associated pull request check; it creates and verifies a Draft release before publishing it. These workflows do not replace the focused local evidence required in a pull request.

## Commits and pull requests

Use clear, imperative commit subjects. Conventional Commit prefixes are preferred for commit subjects and expected in pull request titles:

```text
<type>[optional scope]: <description>
```

Common types are `feat`, `fix`, `docs`, `refactor`, `perf`, `test`, `build`, `ci`, and `chore`.

A pull request should:

- explain the problem, scope, solution, and important tradeoffs;
- link the relevant issue with `Closes #123` when applicable;
- list the exact verification commands and results;
- include tests for behavior changes and bilingual documentation for user-visible changes;
- identify platform-specific or untested behavior and remaining risks;
- avoid generated files, build artifacts, local configuration, logs, credentials, tokens, cryptographic keys, and unrelated formatting changes;
- include screenshots or a short recording for visible TUI changes when useful.

Draft pull requests are welcome for early design feedback. A pull request is ready for final review when its scope is stable, relevant checks pass, and the description contains enough evidence to reproduce and assess the change.

## Review and acceptance

Maintainers review contributions for correctness, contract compatibility, security boundaries, tests, documentation, and scope. Review may request smaller commits or a separately approved design for material changes. Approval does not replace CI or user acceptance where platform behavior must be confirmed.

By contributing, you agree that your contribution is licensed under the repository's [MIT License](LICENSE).
