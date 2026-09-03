# Changelog

## [1.0.0] - 2026-09-03

TendKit 1.0.0 is the first stable release.

### ✨ Highlights

- Manage developer tools in one TUI.
- Review scan results before changing the catalog.
- Check, update, download, or install tools by application policy.
- Use the interface and logs in English or Simplified Chinese.

### 🔍 Discovery

- Finds supported CLIs on `PATH`, including multiple installations of the same command.
- Finds macOS developer applications in `/Applications` and `~/Applications`.
- Finds global Python, npm, Go, uv, RubyGems, Homebrew, and Cargo packages.
- Keeps existing entries when scan results are missing or ambiguous.

### 🔄 Providers and actions

- Supports GitHub Releases, GitHub tags, npm, PyPI, uv, JetBrains, Go, Node.js LTS, Sparkle, Homebrew, Cargo, and custom actions.
- Supports four modes: `check`, `auto`, `download`, and `install`.
- Supports concurrent batches, cancellation, progress, and JSONL logs.

### 🛡️ Safety

- Runs changes only after explicit user action and never adds privilege escalation.
- Validates configuration before atomic writes.
- Redacts sensitive values from errors and logs.
- Verifies SHA-256 checksums when trusted checksum data is available.
- Publishes signed releases with checksums and build-provenance attestations.

### 🖥️ Compatibility

- macOS and Linux
- `arm64` and `x86_64`
- Windows, project-local dependencies, and virtual environments are not supported.
- Artifact downloads require `curl` or `aria2c`.

### 🧪 Since v0.1.0-rc.2

- Stabilized CLI, TUI, and runner timeout tests.
- Stabilized the local Go quality toolchain.
- No user-facing behavior or configuration changes.

### 📦 Upgrade

No configuration migration is required:

```bash
go install github.com/eoctet/tendkit/cmd/tendkit@v1.0.0
```

See the [user manual](wiki/user-manual.md) for installation and configuration details.

[1.0.0]: https://github.com/eoctet/tendkit/compare/v0.1.0-rc.2...v1.0.0
