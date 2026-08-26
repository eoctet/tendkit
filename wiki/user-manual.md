# TendKit User Manual

[English](user-manual.md) | [简体中文](user-manual_ZH_CN.md)

This manual is for TendKit users. It covers installation, first scan, version checks, updates and downloads, configuration, custom applications, and troubleshooting. Contributors should read the [Development and Technical Guide](development-and-technical-guide.md).

## 1. What TendKit is

TendKit is a local inventory and updater for macOS and Linux development environments. It discovers developer tools from `PATH`, global package ecosystems, and macOS application folders, then lets you review discoveries, compare versions, download artifacts, or run updates from one TUI.

TendKit does not replace Homebrew, npm, pip, or another package manager. It has no background scheduler, GUI, remote account, or cloud sync. It works only when you start an action, using the Providers and commands declared in your catalog.

## 2. Requirements and installation

Supported environments:

- macOS, Ubuntu, Debian, CentOS, and Red Hat Enterprise Linux;
- `x86_64` and `arm64`;
- a terminal window of at least 80×24.

Windows is not supported yet. macOS application scanning uses the system `plutil`. Download mode requires `curl` or a separately installed `aria2c`. Release binaries do not require Go.

Download the archive for your platform from the [latest GitHub Release](https://github.com/eoctet/tendkit/releases/latest), extract it, and place `tendkit` on your `PATH`. Go users can also install a specific tag:

```bash
go install github.com/eoctet/tendkit/cmd/tendkit@VERSION
```

Start the TUI without loading a launch-directory environment file:

```bash
tendkit --no-env-file
```

TendKit creates a missing `~/.config/tendkit/config.json`, and never overwrites an existing file.

## 3. Command line

```text
tendkit [global options]
tendkit version [global options]
```

| Command or option | Description |
| --- | --- |
| `version` / `--version` | Print the version without starting the TUI |
| `help` / `-h` / `--help` | Show help |
| `--config PATH` | Use another configuration file; default: `~/.config/tendkit/config.json` |
| `--lock PATH` | Use another process lock; default: `<config path>.lock` |
| `--color MODE` | `auto`, `always`, or `never` |
| `--lang LANG` | Override the interface language with `zh` or `en` |
| `--env-file PATH` | Load a specific environment file |
| `--no-env-file` | Disable all environment-file loading |

Examples:

```bash
tendkit --help
tendkit version --no-env-file
tendkit --lang en
tendkit --config ./custom/config.json
tendkit --config ./custom/config.json --lock ./locks/tendkit.lock
```

Success returns exit code `0`, argument or configuration errors return `2`, and signal cancellation returns `130`. Scanning, checking, downloading, and updating have no standalone CLI subcommands; start them from the TUI.

## 4. First use

1. Start TendKit and confirm that `~/.config/tendkit/config.json` is the catalog you want to maintain.
2. Press `CTRL+S` to open the scan workspace.
3. Press `S` to run a full scan.
4. Review the results: press `J` to add the selected candidate, `A` to add all candidates, or `X` to exclude an item.
5. Press `ESC` to return to the application list.
6. Press `A` to check all enabled applications, or select one and press `C`.
7. Press `U` for a selected application, or `CTRL+U` to run every application's configured update mode.
8. Press `L` to inspect live logs and results.

`U` does not necessarily install software. It runs the selected application's `update_mode`: check only, automatic update, download, or a custom install flow.

## 5. TUI pages and shortcuts

Letter shortcuts are case-sensitive. The footer lists the actions that are actually available in the current state; hidden shortcuts do nothing.

### 5.1 Application management

| Key | Action |
| --- | --- |
| `↑` / `↓` | Select an application |
| `ENTER` | Open application details |
| `TAB` | Switch between the application list and run queue |
| `SPACE` | Enable or disable an application |
| `C` / `A` | Check the selected application / check all |
| `U` / `CTRL+U` | Run the selected application / run every update mode |
| `F` | Search by name prefix |
| `S` | Open settings |
| `CTRL+S` | Open the scan workspace |
| `L` | Enter or leave log focus |
| `Q` | Quit; requests safe cancellation while work is running |

Search accepts up to 20 ASCII letters or digits and is case-insensitive. `CTRL+C` clears the query and `ESC` exits search. You can still browse the list, queue, and logs while work runs, but configuration and task-changing actions may be temporarily hidden.

### 5.2 Settings

Edits first enter an in-memory working copy and are not written before you save.

| Key | Action |
| --- | --- |
| `↑` / `↓` | Select a field or application |
| `←` / `→` | Change an enum, Boolean, or focus |
| `ENTER` | Edit a field or enter application settings |
| `CTRL+S` | Validate, save, and apply immediately |
| `R` | Discard working-copy changes |
| `ESC` | Return; choose save or discard when changes exist |

If another program changes the configuration file, TendKit refuses to overwrite it and asks you to reload. Reloading does not automatically repeat the rejected operation.

### 5.3 Scan workspace

| Key | Action |
| --- | --- |
| `S` | Full scan; a repeated scan requires confirmation |
| `T` | Explicitly rescan the selected managed application |
| `J` / `A` | Add the selected candidate / add all candidates |
| `E` | Edit a new candidate |
| `X` | Exclude a candidate; remove exclusion from a managed item |
| `D` | Delete a managed application |
| `I` | Generate identity for a managed application that has none |
| `M` | Toggle `scan_managed` |
| `A` / `P` / `K` | Merge every conflicting field / choose fields / keep current configuration |
| `TAB` | View details |
| `F` | Search |
| `L` | View scan logs |
| `ESC` | Return to the application list |

## 6. What TendKit discovers

| Scan domain | Coverage |
| --- | --- |
| PATH | Supported built-in developer CLIs found on the current `PATH` |
| Application | macOS only: developer `.app` bundles in `/Applications` and `~/Applications` |
| Python | Top-level global or user distributions |
| Node.js | Global npm packages |
| Go | Installed tools in `GOBIN` / `GOPATH/bin` |
| uv | Tools managed by uv |
| Ruby | Installed gems |

Package scans do not cover project dependencies, virtual environments, or isolated project environments. If a package manager is missing or returns an incomplete inventory, other scan domains continue and existing managed items are not deleted for that reason alone.

Three controls are independent:

| Control | Effect |
| --- | --- |
| `settings.scan.exclude` | Prevents automatic discovery and new candidates without deleting or disabling managed applications; supports `*` and `?` |
| `application.scan_managed` | Controls whether the Scanner may maintain path, identity, Provider, package, and action fields |
| `application.enabled` | Controls participation in checks, updates, and downloads; does not control scanning |

## 7. Version checks, Providers, and update modes

TendKit reads the installed version, asks a Provider for the latest version, and compares them. Built-in Providers:

| Provider | Typical use |
| --- | --- |
| `default` | Every capability comes from custom `provider.actions` |
| `github_release` | Latest GitHub Release, assets, and checksum information |
| `github_tag` | Latest GitHub tag |
| `npm` | npm registry and global npm packages |
| `pypi` | PyPI and Python packages |
| `uv` | uv tools with their existing constraints and indexes |
| `jetbrains` | JetBrains stable releases |
| `go` | Go runtime or Go tools |
| `node_lts` | Latest Node.js LTS |
| `sparkle` | macOS Sparkle appcasts |

A Provider that can check the latest version may not support automatic updates. `provider.actions` can override individual built-in capabilities.

| `update_mode` | Behavior |
| --- | --- |
| `check` | Compare versions only; do not download or install |
| `auto` | Run a built-in Update or `actions.update`, then check the installed version again |
| `download` | Download an artifact and optionally verify SHA-256; do not install |
| `install` | `default` only; run `update`, then `install`, then check the version again |

Download mode asks you to choose when multiple compatible artifacts exist; one safe candidate is selected automatically. If one application has no compatible artifact, only that application is skipped and the rest of the batch may continue.

## 8. Configuration files

Default files:

| Path | Purpose |
| --- | --- |
| `~/.config/tendkit/config.json` | The only persistent catalog; strict JSON with current `schema_version` `1` |
| `~/.config/tendkit/config.json.lock` | Exclusive lock held for the process lifetime |
| `~/.config/tendkit/.env` | Optional user environment input |
| `~/.config/tendkit/logs/run.log` | JSONL run and operation logs |

See the [default configuration template](../internal/config/template/default_config.json) for the complete structure. Unknown fields, extra JSON values, invalid enums, duplicate IDs or identities, and missing required fields all cause loading to fail.

### 8.1 Global settings

| Field | Description |
| --- | --- |
| `language` | `zh` or `en` |
| `timeout_seconds` | Shell idle timeout with no output, from 1 second to 24 hours |
| `workers` | Application concurrency, 1–64 |
| `http.timeout_seconds` | Total HTTP timeout, 1–600 seconds |
| `http.max_concurrency_per_host` | Per-host concurrency, 1–16 |
| `http.retries` | Transient-error retries, 0–5 |
| `downloader.cli` | `curl`, `aria2c`, or its full path |
| `downloader.store_path` | Default download directory |
| `downloader.extra_args` | Allowed extra arguments for the selected downloader |
| `log_dir` / `log_level` | Log directory and `TRACE`–`ERROR` level |
| `provider_urls` | Endpoint templates for built-in network Providers |
| `scan` | Scan domains, extra Bundle IDs, and exclusion rules |

### 8.2 Application fields

| Field | Description |
| --- | --- |
| `id` | Unique, stable ID inside the catalog |
| `name` | Display name |
| `type` | `cli`, `application`, `package`, or `sdk` |
| `description` / `url` | Optional description and project URL |
| `install_path` | Actual executable, application, or package path |
| `enabled` | Whether the application is enabled, including checks, updates, and downloads |
| `update_mode` | `check`, `auto`, `download`, or `install` |
| `provider.type` | Provider name |
| `provider.actions` | Optional `version`, `check`, `update`, `download`, and `install` overrides |
| `package` | Package name, repository, or product code required by a Provider |
| `environment` | Environment variables provided only to this application's actions |
| `identity` | Scanner matching and deduplication; never used for update routing |
| `scan_managed` | Whether the Scanner may maintain discovery-owned fields |
| `status_managed` | Runtime status owned by TendKit; do not edit manually |

## 9. Custom application configuration

Prefer editing applications through the TUI settings page. When direct JSON editing is necessary:

1. Quit TendKit to avoid competing with an active process.
2. Back up `~/.config/tendkit/config.json`.
3. Preserve the complete `settings` and `scan_version_control` from the default template and add or edit objects only in `apps`.
4. Check JSON with `python3 -m json.tool ~/.config/tendkit/config.json`.
5. Restart TendKit. Restore the backup if strict validation reports an error.

### 9.1 Example: custom GitHub Release tool

This object uses a custom command for the installed version, GitHub Releases for the latest version, and the tool's self-update command. Replace the path, repository, and commands before placing it in the `apps` array.

```json
{
    "id": "acme-cli",
    "name": "Acme CLI",
    "type": "cli",
    "description": "Example custom command-line tool",
    "url": "https://github.com/acme/acme-cli",
    "install_path": "/usr/local/bin/acme",
    "enabled": true,
    "update_mode": "auto",
    "provider": {
        "type": "github_release",
        "actions": {
            "version": "{install_path} --version",
            "update": "{install_path} self-update"
        }
    },
    "package": "acme/acme-cli",
    "identity": "cli:acme",
    "scan_managed": false,
    "status_managed": {
        "update_status": "unchecked"
    }
}
```

To check without updating, change `update_mode` to `check` and remove the `update` action.

### 9.2 Example: custom download

For the `default` Provider, the `check` action prints the latest version. This example downloads an artifact matching the version and architecture, then verifies SHA-256 from a checksum file:

```json
{
    "id": "acme-sdk",
    "name": "Acme SDK",
    "type": "sdk",
    "install_path": "/opt/acme/bin/acme",
    "enabled": true,
    "update_mode": "download",
    "provider": {
        "type": "default",
        "actions": {
            "version": "{install_path} --version",
            "check": "curl -fsSL https://downloads.example.com/acme/latest.txt",
            "download": {
                "url": "https://downloads.example.com/acme-{latest_version}-{arch}.tar.gz",
                "filename": "acme-{latest_version}-{arch}.tar.gz",
                "store_path": "~/Downloads/tendkit",
                "checksum_enabled": true,
                "checksum_url": "https://downloads.example.com/acme-{latest_version}-checksums.txt",
                "extra_args": []
            }
        }
    },
    "scan_managed": false,
    "status_managed": {
        "update_status": "unchecked"
    }
}
```

Custom actions are trusted shell code. Test side-effect-free `version` and `check` commands independently before adding update or install commands. Never copy configuration from an untrusted source.

### 9.3 Actions and download fields

| Action | Output or behavior |
| --- | --- |
| `version` | Prints the installed version; required for enabled `default` applications |
| `check` | Prints the latest version; required for `default` |
| `update` | Performs an update; required for `auto` without a built-in Update |
| `download` | Describes URL, filename, directory, verification, and downloader arguments |
| `install` | Installation step for `install` mode; supported only by `default` |

A download object supports `url`, `filename`, `store_path`, `checksum_enabled`, `checksum_url`, `checksum_value`, and `extra_args`. URLs must use HTTP or HTTPS. SHA-256 verification is optional. When it is enabled for a non-GitHub Release download, provide either a fixed digest or a checksum-file URL.

### 9.4 Template placeholders

| Placeholder | Meaning |
| --- | --- |
| `{id}` | Application ID |
| `{name}` / `{app_name}` | Application name |
| `{install_path}` | Installation path |
| `{current_version}` | Installed version |
| `{latest_version}` / `{last_version}` | Latest version |
| `{download_dir}` | Global download directory |
| `{arch}` | `x86_64` or `arm64` |
| `{package}` | Provider package value |

Unknown or unclosed placeholders fail. Dynamic values in shell actions are safely quoted, but the action itself remains trusted code and is not sandboxed.

## 10. Environment variables and credentials

An explicit `--env-file PATH` is used exclusively and must exist. Without it, TendKit loads the startup directory's `.env` if present; only when that file is absent does it try `~/.config/tendkit/.env`, Missing default files are ignored. `--no-env-file` disables all file loading and cannot be combined with `--env-file`.

Provide credentials, tokens, and cryptographic keys only through the process environment or an uncommitted `.env`. Sensitive variables are not passed to actions by default; when inheritance is required, declare the same name with an empty value in the application's `environment`. Never put these values in the catalog, actions, download URLs, paths, or logs.

## 11. Logs, cancellation, and statuses

`~/.config/tendkit/logs/run.log` is JSONL and records runs, scans, configuration, and application operations. Logs rotate by local date or at 128 MiB, with at most five files retained. Logging failures do not change check or update results.

Transient statuses include `waiting`, `checking`, `updating`, and `downloading`. Final statuses include `current`, `update_available`, `updated`, `downloaded`, `downloaded_unverified`, `skipped`, `missing`, and `failed`.

`ESC` cancels only the task shown in the footer or returns from the current page. During active work, `Q` requests safe cancellation and waits for completion. Cancellation or context failure does not save partial run results.

## 12. Troubleshooting

| Symptom | Resolution |
| --- | --- |
| Configuration error at startup | Check JSON, unknown fields, required settings, schema, enums, and file permissions; generate a default configuration in a temporary directory for comparison |
| Another instance is reported | Another process holds the lock; wait for it to exit and do not write the same configuration concurrently |
| Terminal is too small | Resize it to at least 80×24 |
| An application is not discovered | Check scan domains, `exclude`, `PATH`, installation path, and supported coverage; use `T` for an explicit managed-item diagnostic |
| Check or update does not run | Check `enabled`, `install_path`, `update_mode`, Provider, package, and required actions |
| Download fails or is unverified | Check `curl`/`aria2c`, target directory, allowed arguments, and checksum source, then inspect `run.log` |
| Configuration save is rejected | The file may have changed externally; reload when prompted, compare, and apply the change again |
| A shell action fails | Check commands, placeholders, dependencies, permissions, and redacted logs; do not bypass validation by loosening file permissions |

If the problem remains, search existing issues and use the repository Issue Form with publishable reproduction steps, platform details, and redacted logs. Report vulnerabilities privately according to [`SECURITY.md`](../SECURITY.md).
