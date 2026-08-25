# TendKit

<p align="center">
  <img src="assets/tendkit-logo.png" alt="TendKit logo" width="720">
</p>

> See every tool. Spot every update. Stay in control.

[English](README.md) | [简体中文](README_ZH_CN.md)

Your development tools are everywhere: shell paths, language package managers, and application folders. Keeping track of them is tedious.

TendKit brings them into one terminal dashboard. Scan your machine, see what is installed, check for new versions, and update only the tools you choose.

TendKit runs locally on macOS and Linux. It does not replace your package managers, and it never turns updates into an unchecked batch command.

> TendKit is currently at version `0.1.0` and is pre-release software.

## ✨ Why TendKit?

| | What you get |
| --- | --- |
| 🧰 | **One clear inventory** — See CLIs, global packages, and macOS developer apps in one place. |
| 🔎 | **Less manual checking** — Compare installed and latest versions without remembering a different command for every tool. |
| 🎛️ | **Your choice, every time** — Check only, update automatically, or download an artifact for later. |
| ✅ | **Review before changing** — Accept or reject newly found tools and scan changes. |
| 🛡️ | **Built for real workstations** — Duplicate detection, cancellation, safe configuration writes, and structured logs are included. |
| 🌐 | **English and Chinese** — Switch the interface language at startup or in settings. |

## 🧭 How it works

1. 🔍 **Scan** — TendKit finds supported tools from `PATH`, global package ecosystems, and macOS application folders.
2. 👀 **Review** — You decide which discoveries and configuration changes to keep.
3. 🚀 **Check or update** — TendKit uses the right provider for each tool and shows the result in the TUI.

Supported sources include GitHub Releases and tags, npm, PyPI, uv, JetBrains, Go, Node.js, Sparkle feeds, and custom commands.

## 🚀 Quick start

### ✅ Requirements

- macOS or Linux on `arm64` or `x86_64`
- Go 1.23 or later; see [`go.mod`](go.mod) for the exact development toolchain
- `aria2c` or `curl` only when downloading artifacts

Windows is not supported yet. Supported Linux distributions are Ubuntu, Debian, CentOS, and Red Hat Enterprise Linux.

### 🛠️ Build and run

```bash
git clone https://github.com/eoctet/tendkit.git
cd tendkit
mkdir -p bin
go build -o ./bin/tendkit ./cmd/tendkit
tendkit_bin="$(pwd)/bin/tendkit"

mkdir -p /tmp/tendkit-demo
cd /tmp/tendkit-demo
"$tendkit_bin" --no-env-file
```

TendKit opens the terminal interface and creates `conf/config.json` in the directory where it starts. It never overwrites an existing configuration file. Using a temporary directory for the first run keeps the trial separate from your normal workspace.

## ⌨️ Everyday use

| Key | Action |
| --- | --- |
| `↑` / `↓` | Select a tool |
| `ENTER` | Open details or confirm an action |
| `SPACE` | Enable or disable the selected tool |
| `C` / `A` | Check one tool / check all |
| `U` / `CTRL+U` | Update one tool / update all |
| `F` | Search |
| `S` / `CTRL+S` | Open settings / scan management |
| `L` | View logs |
| `ESC` | Go back or cancel |
| `Q` | Quit |

Letter shortcuts are case-sensitive.

### 🧩 Command-line options

```text
tendkit [options]
tendkit version [options]

--config PATH    use a different configuration file
--lock PATH      use a different process lock file
--color MODE     auto, always, or never
--lang LANG      en or zh
--env-file PATH  load a specific environment file
--no-env-file    do not load .env from the launch directory
```

Scanning, version checks, downloads, updates, and settings are handled inside the TUI.

## 🛡️ Safety by default

TendKit keeps you in control:

- It runs commands with your current user permissions and never adds `sudo`.
- It validates configuration before running commands or saving changes.
- It locks the active configuration and writes updates atomically.
- It can verify downloaded artifacts with SHA-256 when a trusted checksum is available.
- It supports cancellation and stops child process groups.
- It keeps runtime logs in `logs/run.log` and limits retained log files.

Custom provider actions are shell commands. Review them like code, and never place credentials or tokens in configuration or logs. Read the [user manual](docs/product/user-manual.md) for the full operating and security model.

## 📚 Documentation

- [User manual](docs/product/user-manual.md)
- [Features and product boundaries](docs/architecture/features.md)
- [Architecture](docs/architecture/architecture.md)
- [Development guide](docs/architecture/development.md)
- [Technology stack](docs/architecture/technology-stack.md)
- [TUI interaction design](docs/product/tui-interaction-design.md)

The detailed product and architecture documents are currently maintained in Simplified Chinese.

## 💬 Feedback and contributing

Found a bug or have an idea? Use the bilingual Issue Forms in this repository. Search existing issues first and remove secrets or private information from logs and screenshots.

Want to contribute code or documentation? Start with [CONTRIBUTING.md](CONTRIBUTING.md). Keep changes focused, add relevant tests, and explain how you verified the result.

Do not report security vulnerabilities in a public issue. Follow the private reporting process in [SECURITY.md](SECURITY.md).

## 📄 License

TendKit is available under the [MIT License](LICENSE).
