# TendKit 产品使用手册

[English](user-manual.md) | [简体中文](user-manual_ZH_CN.md)

本文面向 TendKit 最终用户，介绍安装、首次扫描、版本检查、更新与下载、配置管理、自定义应用和故障排查。贡献代码请阅读[开发与技术规范](development-and-technical-guide_ZH_CN.md)。

## 1. TendKit 是什么

TendKit 是 macOS 与 Linux 开发环境的本地工具清单与更新器。它从 `PATH`、全局包生态和 macOS 应用目录发现开发工具，在一个 TUI 中完成审核、版本比较、下载或更新。

TendKit 不替代 Homebrew、npm、pip 或其他包管理器，也不提供后台定时任务、GUI、远程账号或云端同步。它只在你启动操作时，使用 catalog 中已配置的 Provider 和命令工作。

## 2. 系统要求与安装

支持：

- macOS、Ubuntu、Debian、CentOS、Red Hat Enterprise Linux；
- `x86_64` 与 `arm64`；
- 至少 80×24 的终端窗口。

暂不支持 Windows。macOS 应用扫描使用系统自带的 `plutil`；下载模式需要 `curl` 或自行安装的 `aria2c`。使用发布包不需要安装 Go。

从 [Latest GitHub Release](https://github.com/eoctet/tendkit/releases/latest) 下载对应平台归档，解压并将 `tendkit` 放入 `PATH`。Go 用户也可以安装指定标签：

```bash
go install github.com/eoctet/tendkit/cmd/tendkit@VERSION
```

首次体验可禁止加载启动目录的环境变量文件：

```bash
tendkit --no-env-file
```

TendKit 会创建默认配置文件的 `~/.config/tendkit/config.json`，但不会覆盖已有文件。

## 3. 命令行

```text
tendkit [通用选项]
tendkit version [通用选项]
```

| 命令或选项 | 说明 |
| --- | --- |
| `version` / `--version` | 输出程序版本，不启动 TUI |
| `help` / `-h` / `--help` | 显示帮助 |
| `--config PATH` | 使用其他配置文件；默认 `~/.config/tendkit/config.json` |
| `--lock PATH` | 使用其他进程锁；默认 `<配置路径>.lock` |
| `--color MODE` | `auto`、`always` 或 `never` |
| `--lang LANG` | 使用 `zh` 或 `en` 覆盖界面语言 |
| `--env-file PATH` | 加载指定的环境变量文件 |
| `--no-env-file` | 禁用全部环境变量文件加载 |

示例：

```bash
tendkit --help
tendkit version --no-env-file
tendkit --lang zh
tendkit --config ./custom/config.json
tendkit --config ./custom/config.json --lock ./locks/tendkit.lock
```

成功返回退出码 `0`，参数或配置错误返回 `2`，信号取消返回 `130`。扫描、检查、下载和更新没有独立 CLI 子命令，均在 TUI 中发起。

## 4. 首次使用

1. 启动 TendKit，确认 `~/.config/tendkit/config.json` 是你准备长期维护的 catalog。
2. 按 `CTRL+S` 进入扫描工作台。
3. 按 `S` 执行全量扫描。
4. 审核结果：按 `J` 加入当前候选，按 `A` 加入全部候选，或按 `X` 排除不需要的项目。
5. 按 `ESC` 返回应用列表。
6. 按 `A` 检查全部已启用应用，或选中应用后按 `C` 单独检查。
7. 对需要处理的应用按 `U`，或按 `CTRL+U` 执行全部应用各自配置的更新模式。
8. 按 `L` 查看实时日志和结果。

`U` 不一定代表直接安装。它会执行所选应用的 `update_mode`：只检查、自动更新、下载，或自定义安装流程。

## 5. TUI 页面与快捷键

字母快捷键区分大小写。界面 footer 显示当前状态下真正可用的动作；未显示的快捷键不会执行。

### 5.1 应用管理

| 按键 | 操作 |
| --- | --- |
| `↑` / `↓` | 选择应用 |
| `ENTER` | 打开应用详情 |
| `TAB` | 切换应用列表与执行队列 |
| `SPACE` | 启用或停用应用 |
| `C` / `A` | 检查当前应用 / 检查全部 |
| `U` / `CTRL+U` | 执行当前应用 / 执行全部的更新模式 |
| `F` | 按名称前缀搜索 |
| `S` | 打开配置页 |
| `CTRL+S` | 打开扫描工作台 |
| `L` | 打开或关闭日志焦点 |
| `Q` | 退出；运行中会请求安全取消 |

搜索最多接受 20 个 ASCII 字母或数字，不区分大小写。`CTRL+C` 清除查询，`ESC` 退出搜索。运行中仍可浏览列表、队列和日志，但会改变配置或任务集合的入口可能暂时隐藏。

### 5.2 配置页

配置修改先进入内存工作副本，保存前不会写入文件。

| 按键 | 操作 |
| --- | --- |
| `↑` / `↓` | 选择字段或应用 |
| `←` / `→` | 切换枚举、布尔值或焦点 |
| `ENTER` | 编辑字段或进入应用配置 |
| `CTRL+S` | 校验、保存并立即生效 |
| `R` | 放弃工作副本修改 |
| `ESC` | 返回；存在未保存修改时选择保存或放弃 |

如果配置文件被其他程序修改，TendKit 会拒绝覆盖并提示重新加载。重新加载不会自动重复刚才被拒绝的操作。

### 5.3 扫描工作台

| 按键 | 操作 |
| --- | --- |
| `S` | 全量扫描；再次扫描时需要确认 |
| `T` | 显式重扫当前正式应用 |
| `J` / `A` | 加入当前候选 / 加入全部候选 |
| `E` | 编辑新增候选 |
| `X` | 排除候选；对已排除正式项则取消排除 |
| `D` | 删除正式应用 |
| `I` | 为缺少 identity 的正式应用生成 identity |
| `M` | 切换 `scan_managed` |
| `A` / `P` / `K` | 对冲突合并全部字段 / 选择部分字段 / 保持当前配置 |
| `TAB` | 查看详情 |
| `F` | 搜索 |
| `L` | 查看扫描日志 |
| `ESC` | 返回应用列表 |

## 6. 可以发现什么

| 扫描域 | 范围 |
| --- | --- |
| PATH | 内置目录支持且能在当前 `PATH` 定位的开发 CLI |
| Application | 仅 macOS：`/Applications` 与 `~/Applications` 中识别为开发工具的 `.app` |
| Python | 顶层全局或用户级 distribution |
| Node.js | 全局 npm 包 |
| Go | `GOBIN` / `GOPATH/bin` 中的已安装工具 |
| uv | uv 管理的工具 |
| Ruby | 已安装 gem |
| Homebrew formula | 使用 Homebrew formula 安装的包 |
| Homebrew cask | 使用 Homebrew cask 安装的macOS应用 |
| Cargo | Cargo cli 管理的包 |

包扫描不覆盖项目内依赖、虚拟环境或单独项目环境。某个包管理器不存在或返回不完整清单时，其他扫描域仍可继续，已有纳管项目也不会仅因此被删除。

三个开关互不替代：

| 控制项 | 作用 |
| --- | --- |
| `settings.scan.exclude` | 阻止自动发现与新增候选，不删除或停用正式应用；支持 `*` 和 `?` |
| `application.scan_managed` | 决定 Scanner 能否维护该应用的路径、identity、Provider、package 和动作等字段 |
| `application.enabled` | 决定应用是否参与检查、更新和下载；不控制扫描 |

## 7. 版本检查、Provider 与更新模式

TendKit 先读取当前版本，再通过 Provider 获取最新版本并比较。内置 Provider：

| Provider | 典型用途 |
| --- | --- |
| `default` | 所有能力由自定义 `provider.actions` 提供 |
| `github_release` | GitHub 最新 Release、资产与校验信息 |
| `github_tag` | GitHub 最新 tag |
| `npm` | npm registry 与全局 npm 包 |
| `pypi` | PyPI 与 Python 包 |
| `uv` | uv 工具及其已有约束和 index |
| `jetbrains` | JetBrains 正式版本 |
| `go` | Go 运行时或 Go 工具 |
| `node_lts` | 最新 Node.js LTS |
| `sparkle` | macOS Sparkle appcast |
| `homebrew` | Homebrew formula/cask |
| `cargo` | Cargo binary crate |

Provider 能检查最新版，不代表一定能自动更新。`provider.actions` 可以逐项覆盖内置能力。

| `update_mode` | 行为 |
| --- | --- |
| `check` | 只比较版本，不下载、不安装 |
| `auto` | 有更新时执行内置 Update 或 `actions.update`，完成后重新检查版本 |
| `download` | 下载制品，可执行 SHA-256 校验，但不安装 |
| `install` | 仅限 `default`；依次执行 `update`、`install`，然后重新检查版本 |

下载模式在多个兼容资产之间需要你选择；单个安全候选会自动选中。某个应用没有兼容资产时只跳过该应用，其余批次可以继续。

## 8. 配置文件

默认文件：

| 路径 | 用途 |
| --- | --- |
| `~/.config/tendkit/config.json` | 唯一持久化 catalog；严格 JSON，当前 `schema_version` 为 `1` |
| `~/.config/tendkit/config.json.lock` | 进程生命周期内的排他锁 |
| `~/.config/tendkit/.env` | 可选用户环境变量输入 |
| `~/.config/tendkit/logs/run.log` | JSONL 运行与操作日志 |

完整默认结构见[默认配置模板](../internal/config/template/default_config.json)。未知字段、多余 JSON、非法枚举、重复 ID/identity 和缺失必填字段都会使加载失败。

### 8.1 全局配置

| 字段 | 说明 |
| --- | --- |
| `language` | `zh` 或 `en` |
| `timeout_seconds` | shell 连续无输出的空闲超时，1 秒至 24 小时 |
| `workers` | 应用并发数，1–64 |
| `http.timeout_seconds` | HTTP 总超时，1–600 秒 |
| `http.max_concurrency_per_host` | 单主机并发，1–16 |
| `http.retries` | 瞬时错误重试次数，0–5 |
| `downloader.cli` | `curl`、`aria2c` 或其完整路径 |
| `downloader.store_path` | 默认下载目录 |
| `downloader.extra_args` | 对应下载器允许的额外参数 |
| `log_dir` / `log_level` | 日志目录与 `TRACE`–`ERROR` 等级 |
| `provider_urls` | 内置网络 Provider 的端点模板 |
| `scan` | 各扫描域、额外 Bundle ID 和排除规则 |

### 8.2 应用配置字段

| 字段 | 说明 |
| --- | --- |
| `id` | catalog 内唯一稳定 ID |
| `name` | 界面显示名称 |
| `type` | `cli`、`application`、`package` 或 `sdk` |
| `description` / `url` | 可选说明与项目地址 |
| `install_path` | 可执行文件、应用或包的实际路径 |
| `enabled` | 是否启用，包括检查、更新和下载 |
| `update_mode` | `check`、`auto`、`download` 或 `install` |
| `provider.type` | Provider 名称 |
| `provider.actions` | 可选的 `version`、`check`、`update`、`download`、`install` 覆盖 |
| `package` | Provider 所需的包名、仓库或产品码 |
| `environment` | 只提供给该应用 action 的环境变量 |
| `identity` | Scanner 用于匹配和去重；不参与更新路由 |
| `scan_managed` | 是否允许 Scanner 维护发现字段 |
| `status_managed` | TendKit 维护的运行状态，不要手工编辑 |

## 9. 自定义应用配置

优先在 TUI 配置页编辑应用。需要直接修改 JSON 时：

1. 退出 TendKit，避免与活动进程争用配置。
2. 备份 `~/.config/tendkit/config.json`。
3. 从默认模板保留完整 `settings` 和 `scan_version_control`，只在 `apps` 数组中添加或修改对象。
4. 使用 `python3 -m json.tool ~/.config/tendkit/config.json` 检查 JSON。
5. 重新启动 TendKit；严格校验失败时根据错误恢复备份。

### 9.1 示例：自定义 GitHub Release 工具

下面的对象使用自定义命令读取当前版本、GitHub Release 查询最新版本，并通过工具自更新。请替换路径、仓库和命令后放入 `apps` 数组。

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

如果只想检查版本，将 `update_mode` 改为 `check` 并移除 `update` action。

### 9.2 示例：自定义下载

`default` Provider 的最新版由 `check` action 输出。下面的示例下载匹配版本和架构的制品，并从校验文件验证 SHA-256：

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

自定义 action 是可信 shell 代码。先在终端中独立验证无副作用的 `version` 和 `check`，再加入更新或安装命令。不要复制来源不明的配置。

### 9.3 Action 与下载字段

| Action | 输出或行为 |
| --- | --- |
| `version` | 输出当前版本；`default` Provider 的启用应用必须提供 |
| `check` | 输出最新版本；`default` Provider 必须提供 |
| `update` | 执行更新；`auto` 没有内置更新能力时必须提供 |
| `download` | 描述 URL、文件名、目录、校验与下载器参数 |
| `install` | `install` 模式的安装步骤，仅 `default` 支持 |

下载对象支持 `url`、`filename`、`store_path`、`checksum_enabled`、`checksum_url`、`checksum_value` 和 `extra_args`。URL 只接受 HTTP/HTTPS。SHA-256 校验是可选能力；非 GitHub Release 下载启用校验时，必须提供固定 SHA-256 或校验文件 URL。

### 9.4 模板占位符

| 占位符 | 含义 |
| --- | --- |
| `{id}` | 应用 ID |
| `{name}` / `{app_name}` | 应用名称 |
| `{install_path}` | 安装路径 |
| `{current_version}` | 当前版本 |
| `{latest_version}` / `{last_version}` | 最新版本 |
| `{download_dir}` | 全局下载目录 |
| `{arch}` | `x86_64` 或 `arm64` |
| `{package}` | Provider 使用的 package 值 |

未知或未闭合占位符会失败。shell action 中的动态值会安全引用，但 action 本身仍是可信代码，不是沙箱。

## 10. 环境变量与凭据

显式 `--env-file PATH` 指定环境变量文件加载；未指定时，若启动目录 `.env` 存在则加载它，仅在它不存在时才尝试 `~/.config/tendkit/.env`，缺失的默认文件会被忽略；`--no-env-file` 禁用全部文件加载，且不能与 `--env-file` 同时使用。

凭据、令牌和加密密钥只通过进程环境或未提交的 `.env` 提供。敏感变量默认不会传给 action；确实需要继承时，在应用 `environment` 中以同名空值显式声明。不要把这些值写入 catalog、action、下载 URL、路径或日志。

## 11. 日志、取消与状态

`~/.config/tendkit/logs/run.log` 是 JSONL，记录运行、扫描、配置和应用操作。日志按本地日期或 128 MiB 滚动，最多保留 5 个文件。日志故障不会改变检查或更新结果。

过程状态包括 `waiting`、`checking`、`updating` 和 `downloading`；最终状态包括 `current`、`update_available`、`updated`、`downloaded`、`downloaded_unverified`、`skipped`、`missing` 和 `failed`。

`ESC` 只取消 footer 当前展示的任务或返回当前页面。`Q` 在任务运行中会请求安全取消并等待结束。取消或上下文失败时，不保存部分运行结果。

## 12. 故障排查

| 现象 | 处理 |
| --- | --- |
| 启动报配置错误 | 检查 JSON、未知字段、必填设置、schema、枚举和文件权限；可在临时目录生成默认配置对比 |
| 提示已有实例 | 另一进程持有 lock；确认其退出后再启动，不要并发写同一配置 |
| 终端尺寸不足 | 调整到至少 80×24 |
| 扫描不到应用 | 检查扫描域、`exclude`、`PATH`、安装路径和支持范围；正式应用可用 `T` 单项诊断 |
| 不执行检查或更新 | 检查 `enabled`、`install_path`、`update_mode`、Provider、package 和相应 action |
| 下载失败或未验证 | 检查 `curl`/`aria2c`、目标目录、允许参数和校验源，并查看 `run.log` |
| 配置保存被拒绝 | 文件可能被外部修改；按提示重新加载、比较后再应用修改 |
| shell action 失败 | 检查命令、占位符、依赖、权限和脱敏日志，不要通过放宽文件权限绕过校验 |

仍无法解决时，请搜索现有 Issue，再通过仓库 Issue Form 提交可公开的复现步骤、平台信息和已脱敏日志。安全漏洞请遵循 [`SECURITY_ZH_CN.md`](../SECURITY_ZH_CN.md) 私下报告。
