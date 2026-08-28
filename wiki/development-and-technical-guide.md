# TendKit Development and Technical Guide

[English](development-and-technical-guide.md) | [简体中文](development-and-technical-guide_ZH_CN.md)

This guide is for TendKit contributors. It defines the development environment, code boundaries, technical constraints, testing requirements, and pull request standards. Before contributing, also read [`CONTRIBUTING.md`](../CONTRIBUTING.md), [`SECURITY.md`](../SECURITY.md), and [`README.md`](../README.md).

## 1. Development principles

- Keep each change focused on one clearly defined problem and use the smallest change that satisfies its acceptance conditions.
- For behavior changes, add a focused test that reproduces the problem before implementing the fix.
- Do not add configuration, dependencies, frameworks, or abstractions for hypothetical needs.
- Preserve unrelated existing changes, and never commit local configuration, logs, build artifacts, credentials, tokens, or cryptographic keys.
- When public CLI behavior, the JSON schema, statuses, or a security boundary changes, update tests, the README, and the wiki together.
- If implementation and documentation disagree, identify the conflict in an issue or pull request instead of silently changing a public contract.

For a material feature, public interface or configuration/schema change, migration, security-boundary change, or cross-component design, open an issue and agree on the scope with maintainers before implementation.

Small corrections to documentation, tests, or clearly local behavior can normally be submitted directly. Maintainers may ask for an issue first when a change affects long-term product or architecture contracts.

## 2. Development environment

Required tools:

- Git;
- Go 1.23 or later, using the toolchain declared in [`go.mod`](../go.mod);
- Python 3, used only for repository JSON checks;
- macOS or a supported Linux environment.

macOS provides the complete platform, Application Bundle, and PTY test surface. Linux covers the non-macOS implementation. `aria2c` and `curl` are needed only for manual download testing; automated tests must not depend on real downloaders or remote sites.

```bash
git clone https://github.com/eoctet/tendkit.git
cd tendkit
go test ./...
go build ./...
```

The full quality script uses pinned versions of `staticcheck`, `govulncheck`, and `gosec`. It never installs or upgrades tools automatically; when a dependency is missing, it reports the required version and installation command.

```bash
scripts/verify-go-quality.sh
```

## 3. Technology boundaries

| Area | Standard |
| --- | --- |
| Primary language | Go |
| Minimum version | Go 1.23 |
| Module | `github.com/eoctet/tendkit` |
| Dependency policy | Prefer the Go standard library |
| Build entry point | `./cmd/tendkit` |
| Distribution | A single binary built without cgo |
| Platforms | macOS, Ubuntu, Debian, CentOS, Red Hat Enterprise Linux |
| Architectures | `x86_64`, `arm64`; Windows is not supported yet |
| UI | Single-page ANSI/termios TUI |
| Persistence | Strict JSON configuration and JSONL logs; no database or background service |

Do not add a wrapper library when the standard library already provides a maintainable solution. Before introducing a language, module, binary, or service, explain in an issue why existing capabilities are insufficient, whether it enters the build or runtime chain, how its version is pinned, how it is tested offline, its license and supply-chain impact, and the cost of removing it later.

## 4. Project layout and responsibilities

| Path | Responsibility |
| --- | --- |
| `cmd/tendkit` | Process entry point, CLI flags, signals, exit codes, and dependency assembly |
| `internal/service` | Scan and update transactions, lock scope, and final persistence |
| `internal/model` | Shared domain vocabulary for configuration, providers, statuses, and limits |
| `internal/config` | Strict JSON, defaults, validation, caching, locking, and atomic writes |
| `internal/scanner` | Discovery, identity, exclusion, deduplication, and merging |
| `internal/updater` | Provider resolution, concurrent work, downloads, verification, and updates |
| `internal/ui` | Single-event-loop TUI state machine |
| `pkg/*` | Shared runtime, HTTP, logging, i18n, download, metadata, error, and version capabilities |

`cmd` assembles dependencies and does not own business transactions. Cross-package interfaces are minimal and defined by their consumers; lower-level packages must not depend on `cmd`. Before adding a file, check whether an existing package already owns that responsibility. Create a new package only for a stable boundary reused by multiple consumers.

Typical call flow:

```text
CLI / TUI
    -> internal/service
        -> internal/scanner or internal/updater
            -> local tools, package managers, providers, shell, or downloader
        -> atomic commit through internal/config
    -> JSONL logs
```

## 5. Go coding standards

- Use `gofmt` and follow standard Go naming, error, and documentation conventions.
- Library packages return errors with operation context; they do not call `os.Exit` or choose user-facing exit codes.
- Preserve causes with `%w` or `errors.Join`. Do not ignore create, write, encode, sync, or close failures.
- Every goroutine must have a clear owner, exit condition, and wait path. Concurrency must remain bounded.
- Network, command, download, and background operations must accept and propagate `context.Context`.
- Do not log complete catalog commands, complete environments, credentials, tokens, or cryptographic keys.
- Never put real credentials in code, tests, examples, configuration, or logs.

## 6. Configuration and model standards

[`internal/config/template/default_config.json`](../internal/config/template/default_config.json) is the default configuration template. Configuration parsing must continue to reject:

- unknown fields and extra top-level JSON values;
- unsupported `schema_version` values;
- invalid enums, out-of-range numbers, duplicate IDs, and duplicate non-empty identities;
- missing required fields or update modes that cannot execute.

When adding a configuration field, update all of the following:

1. `internal/model` structures and stable vocabulary;
2. defaults, normalization, and strict validation in `internal/config`;
3. the default configuration template;
4. relevant TUI editors and both i18n catalogs;
5. user documentation and configuration examples;
6. compatibility or migration behavior and regression tests.

`config.json` is trusted executable configuration. Application results belong only in `status_managed`; temporary scan observations do not become user configuration. `identity` is for Scanner matching and deduplication only and must never route Provider behavior.

## 7. Shell, Provider, and download standards

`provider.actions` values are complete shell scripts. Do not split, rebuild, or change the semantics of multiline scripts, pipelines, functions, or heredocs.

- System, distribution, architecture, and shell selection come only from `pkg/runtime/system_info.go`.
- Use the existing template renderer and shell quoting for dynamic values; never inject paths, versions, packages, or URLs through string concatenation.
- Run external commands in independent process groups and respond to cancellation and timeouts.
- Keep stdout/stderr, HTTP responses, logs, event queues, and retries bounded.

When adding a Provider:

- implement only its real, minimal capabilities in `internal/updater/provider`;
- register it through the Registry instead of branching on Provider names in the engine;
- do not register empty capabilities;
- support concurrent calls and prompt cancellation;
- use the shared HTTP source and test with `httptest.Server` or a custom transport;
- return a clear application-level error when a required package is missing instead of guessing or rewriting configuration.

The downloader currently supports `aria2c` and `curl`. Extra arguments must pass the existing adapter validation and must not override program-controlled URL, output, configuration, progress, or side-effect options.

## 8. Scanner standards

Add a scan domain or package ecosystem by implementing a `Handler` in `internal/scanner/handler` and returning candidates, exclusion aliases, completeness, and errors.

- A Handler does not persist data or depend on UI, Service, or Config.
- The root Scanner owns identity, exclusions, matching, merging, and state preservation.
- Each discovery type should define a stable identity; when identity is absent, preserve existing path, package, and name fallbacks.
- Merging must preserve user-maintained Providers, actions, update modes, and other policy fields.
- An incomplete package-manager inventory must not delete managed items that were not observed.
- Tests use temporary directories, temporary scripts, and fakes rather than the contributor's installed tools.

## 9. Concurrency, persistence, and security

- Application work uses a bounded worker pool; never create an unbounded goroutine per application.
- Within a batch, deduplicate only waiting and running application IDs; a completed application may be added again.
- Configuration changes go through the configuration facade, with business lock scope owned by Service.
- A business operation commits one complete configuration and preserves revision, disk baseline, and external-change checks.
- Configuration writes use a same-directory temporary file, file `fsync`, atomic rename, and directory `fsync`.
- Cancellation or context failure must not save partial run results; logging failure must not alter the operation result.
- Configuration, lock, and log files remain owned by the current user and not writable by group or others.
- All logs and user-visible errors pass through sensitive-value redaction.

Concurrency changes need coverage for reordering, duplicates, cancellation, and close races. Run `go test -race ./...` when supported by the toolchain.

## 10. TUI and internationalization

- User-facing fixed text comes from `pkg/i18n`; maintain `zh.json` and `en.json` together.
- Do not translate raw command output, JSON fields, status codes, Provider names, or log event names.
- Only the TUI event loop modifies the view model; background work sends bounded events.
- Every normal, error, signal, and cancellation exit path must restore terminal state.
- Preserve the 80×24 minimum full layout, Chinese double-width behavior, and smaller-terminal fallback.
- Letter shortcuts are case-sensitive. The footer's visible actions must exactly match permitted actions.

Visible TUI changes should test key sequences, focus states, hidden-key no-ops, Chinese layout, small terminals, scrolling, cancellation, and shutdown. Include a screenshot or short recording in the pull request when useful.

## 11. Testing strategy

Start with the smallest relevant test while developing:

```bash
go test ./internal/config -run TestName -count=1
```

Tests must not install or update real software, modify a real TendKit catalog, contact live Providers, or download large artifacts. Use `t.TempDir()` for files, local servers or custom transports for networking, and temporary script fakes for commands and downloaders.

High-risk areas need both success and failure coverage:

- strict configuration parsing, schema, defaults, and file permissions;
- version extraction, normalization, and comparison;
- shell templates, quoting, and sensitive environment filtering;
- Provider retry, rate limiting, cancellation, and artifact selection;
- worker state machines, dynamic batches, and download verification;
- Scanner identity, deduplication, incomplete inventories, and recovery;
- i18n keys, format arguments, and TUI state matrices.

## 12. Pre-submit checks

Run focused tests while iterating. Before opening a pull request, run at least:

```bash
gofmt -w <changed-go-files>
go test ./...
go vet ./...
go build ./...
git diff --check
```

When the pinned quality tools are installed, run:

```bash
scripts/verify-go-quality.sh
```

If a platform or unavailable tool prevents a check, record the exact command, environment, failure, and remaining risk in the pull request. Never report an unrun check as passing.

Repository automation uses three layers:

- [Test](../.github/workflows/test.yml) validates pull requests and pushes to `main` with focused and full tests, race checks for the TUI, `go vet`, builds, and a release snapshot.
- [Nightly](../.github/workflows/nightly.yml) adds the full race suite and repeated PTY/TUI platform tests.
- [Release](../.github/workflows/release.yml) accepts only signed, annotated `v`-prefixed SemVer tags that belong to `main` and have a successful associated pull request check; it creates and verifies a Draft release before publishing it.

These workflows do not replace the focused local evidence required in a pull request.

## 13. Commits and pull requests

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
