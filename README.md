# TendKit

<p align="center">
  <img src="assets/tendkit-logo.png" alt="TendKit logo" width="440">
</p>

> Manage your developer tools in one place. Spot updates early. Stay in control.

[English](README.md) | [简体中文](README_ZH_CN.md)

Developer tools are scattered across `PATH`, package managers, and application folders. You may have lost track of what is installed, what needs attention, and what needs an update.

TendKit brings them together in one terminal dashboard. Scan your machine, view installation status, check for new versions, and update only the tools you need.

TendKit runs locally on macOS and Linux. Its purpose is not to replace your existing package managers, but to give you a simpler way to manage your current development environment.

<p align="center">
  <img src="assets/tendkit-demo.gif" alt="TendKit demo" width="800">
</p>

## ✨ Why TendKit?

| | What you get |
| --- | --- |
| 🧰 | **One clear inventory** — View CLIs, global packages, and macOS developer apps in one place. |
| 🔎 | **Less repetitive checking** — No need to remember every tool's command; TendKit discovers and manages them for you. |
| 🎛️ | **Your choice, every time** — Check only, update automatically, or download an artifact for later. |
| ✅ | **Review before changing** — Accept or reject newly discovered tools and scan changes. |
| 🛡️ | **Production-ready** — Built-in duplicate detection, task handling, safe configuration writes, and audit logs. |
| 📝 | **Configuration-first** — Everything is configuration, so you can add whatever you need. |

TendKit continues to use your existing package managers and update methods. It gives you one place to view and act, but it does not replace those tools.

## 🎯 Use cases

| Situation | How TendKit helps |
| --- | --- |
| **Setting up or auditing a workstation** | Scan once to build a reviewable inventory instead of checking each development environment by hand. |
| **Routine maintenance** | Check all managed tools at once and focus only on items that actually have a new version. |
| **Protecting a stable environment** | Review results one by one and update only selected tools instead of running a blind batch upgrade. |
| **Mixed installation sources** | View PATH tools, Homebrew, global npm/Python/Go/uv/Ruby/Cargo packages, and macOS apps in one place. |

## 🧭 How it works

1. **Scan** — Find supported developer tools on the machine.
2. **Review** — Choose which discoveries to add, edit, or exclude.
3. **Check** — Compare installed and latest versions in one view.
4. **Act** — Run the configured update method or download an artifact for later.

## 🚀 Quick start

### 📦 Install TendKit

Download the package for your operating system from the [latest GitHub Release](https://github.com/eoctet/tendkit/releases/latest). Extract it and place `tendkit` on your `PATH`.

Supported environments:

- macOS or Linux
- `arm64` or `x86_64`
- Ubuntu, Debian, CentOS, and Red Hat Enterprise Linux

Windows is not supported yet. Artifact downloads support `curl` or `aria2c` (install separately).

Go users can also install the binary directly:

```bash
go install github.com/eoctet/tendkit/cmd/tendkit@VERSION
```

> Replace `VERSION` with a tag such as `v0.1.0-rc.1`.

### 🧩 Command-line options

```text
tendkit [options]
tendkit version [options]

--config PATH    use a different configuration file
--lock PATH      use a different process lock file
--color MODE     auto, always, or never
--lang LANG      en or zh
--env-file PATH  load a specific environment file
--no-env-file    disable all environment-file loading
```

The TUI creates `~/.config/tendkit/config.json` and its parent directory when it starts, and never overwrites existing configuration. Scanning, version checks, downloads, updates, and settings are all handled inside the TUI.

## 🔍 What TendKit can find

TendKit can scan and identify:

- supported developer CLIs available on `PATH`;
- Homebrew formulae and casks, global packages managed by npm, Python, Go, uv, Ruby and Cargo;
- developer applications in `/Applications` and `~/Applications` on macOS.

It can check versions through GitHub Releases and tags, Homebrew, npm, PyPI, uv, JetBrains, Go, Node.js, Sparkle feeds, Cargo, and custom commands. Available capabilities depend on the information exposed by each tool or package manager. Project-local dependencies and virtual environments are not currently scanned.

## 🛡️ Safety and control

- Runs with your current user permissions and never adds `sudo`.
- Requires review for newly discovered tools and scan changes.
- Updates, downloads, or installs only when you start the action.
- Validates configuration before saving and writes it atomically.
- Verifies downloaded artifacts with SHA-256 when a trusted checksum is available.
- Preserves the package installation's existing permission model.
- Redacts credentials, tokens, cryptographic keys, and other sensitive information.

Custom provider actions are shell commands. Review them like code, and never place credentials, tokens, or cryptographic keys in configuration or logs. See the [user manual](wiki/user-manual.md) for configuration examples and the complete operating and security model.

## 🛠️ Build from source

Building requires Go 1.23 or later; see [`go.mod`](go.mod) for the exact development toolchain.

```bash
git clone https://github.com/eoctet/tendkit.git
cd tendkit
go build -o ./bin/tendkit ./cmd/tendkit
./bin/tendkit
```

## 📚 Documentation

- [User manual](wiki/user-manual.md) · [简体中文](wiki/user-manual_ZH_CN.md)
- [Development and technical guide](wiki/development-and-technical-guide.md) · [简体中文](wiki/development-and-technical-guide_ZH_CN.md)

## 💬 Feedback and contributing

Found a bug or have an idea? Use the bilingual Issue Forms in this repository. Search existing issues first, and do not include credentials, tokens, cryptographic keys, private paths, or unredacted personal or system information.

Want to contribute code or documentation? Start with [CONTRIBUTING.md](CONTRIBUTING.md). Keep changes focused, add relevant tests, and explain how you verified the result.

Do not report security vulnerabilities in a public issue. Follow the private reporting process in [SECURITY.md](SECURITY.md).

## 📄 License

TendKit is available under the [MIT License](LICENSE).
