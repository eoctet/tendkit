# TendKit 开发与技术规范

[English](development-and-technical-guide.md) | [简体中文](development-and-technical-guide_ZH_CN.md)

本文面向 TendKit 项目贡献者，说明开发环境、代码边界、技术约束、测试要求和 Pull Request 标准。开始贡献前，也请阅读仓库根目录的 [`CONTRIBUTING_ZH_CN.md`](../CONTRIBUTING_ZH_CN.md)、[`SECURITY_ZH_CN.md`](../SECURITY_ZH_CN.md) 和 [`README_ZH_CN.md`](../README_ZH_CN.md)。

## 1. 开发原则

- 每次变更只解决一个明确问题，并以最小改动满足验收条件。
- 行为变更先添加能够复现问题的聚焦测试，再实现修复。
- 不为假设需求引入配置、依赖、框架或抽象。
- 保留无关的现有改动，不提交本地配置、日志、构建产物、凭据、令牌或加密密钥。
- 公开 CLI、JSON schema、状态或安全边界发生变化时，同步更新测试、README 和 wiki。
- 实现与文档不一致时，先在 Issue 或 Pull Request 中指出冲突，不要静默改变公开契约。

实质性功能、公开接口或配置/schema 变更、迁移、安全边界变更或跨组件设计，应先创建 Issue，并与维护者确认范围后再开始实现。

文档、测试或明确局部行为的小修正通常可以直接提交。变更影响长期产品或架构契约时，维护者可能要求先创建 Issue。

## 2. 开发环境

必需工具：

- Git；
- Go 1.23 或更高版本，以 [`go.mod`](../go.mod) 声明的工具链为准；
- Python 3，仅用于仓库 JSON 校验；
- macOS 或受支持的 Linux 环境。

macOS 覆盖完整的平台、Application Bundle 和 PTY 测试面；Linux 覆盖非 macOS 实现。`aria2c` 和 `curl` 仅在人工验证下载流程时需要，自动化测试不得依赖真实下载器或远程站点。

```bash
git clone https://github.com/eoctet/tendkit.git
cd tendkit
go test ./...
go build ./...
```

完整质量脚本使用固定版本的 `staticcheck`、`govulncheck` 和 `gosec`。脚本不会自动安装或升级工具；缺少依赖时会显示所需版本和安装方式。

```bash
scripts/verify-go-quality.sh
```

## 3. 技术栈边界

| 项目 | 规范 |
| --- | --- |
| 主语言 | Go |
| 最低版本 | Go 1.23 |
| 模块 | `github.com/eoctet/tendkit` |
| 依赖策略 | Go 标准库优先；当前不使用第三方 Go module |
| 构建入口 | `./cmd/tendkit` |
| 分发方式 | 无 cgo 的单二进制 |
| 平台 | macOS、Ubuntu、Debian、CentOS、Red Hat Enterprise Linux |
| 架构 | `x86_64`、`arm64`；暂不支持 Windows |
| UI | ANSI/termios 单页 TUI |
| 持久化 | 严格 JSON 配置和 JSONL 日志；无数据库与后台服务 |

标准库已经提供可维护方案时，不引入同类封装库。新增语言、模块、二进制或服务前，应在 Issue 中说明：现有能力为何不足、依赖位于构建链还是运行链、如何锁定版本、离线测试方式、许可证与供应链影响，以及未来移除成本。

## 4. 项目结构与职责

| 路径 | 职责 |
| --- | --- |
| `cmd/tendkit` | 进程入口、CLI 参数、信号、退出码和依赖装配 |
| `internal/service` | 扫描与更新事务、锁范围和最终持久化 |
| `internal/model` | 配置、Provider、状态和限制等共享领域词汇 |
| `internal/config` | 严格 JSON、默认值、校验、缓存、锁和原子写入 |
| `internal/scanner` | 工具发现、identity、排除、去重和合并 |
| `internal/updater` | Provider 解析、并发任务、下载、校验和更新执行 |
| `internal/ui` | 单事件循环 TUI 状态机 |
| `pkg/*` | runtime、HTTP、日志、i18n、下载、metadata、错误和版本等通用能力 |

`cmd` 只负责装配，不承载业务事务。跨包接口由调用方定义最小能力；底层包不得依赖 `cmd`。新增文件前先确认现有包是否已经拥有该职责，只有边界稳定且被多个上层复用时才创建新包。

典型调用路径：

```text
CLI / TUI
    -> internal/service
        -> internal/scanner 或 internal/updater
            -> 本机工具、包管理器、Provider、shell 或下载器
        -> internal/config 原子提交
    -> JSONL 日志
```

## 5. Go 编码规范

- 使用 `gofmt`，遵循 Go 标准命名、错误和文档风格。
- 库包返回包含操作上下文的错误，不调用 `os.Exit`，不决定用户级退出码。
- 使用 `%w` 或 `errors.Join` 保留错误原因；不得忽略创建、写入、编码、同步和关闭失败。
- goroutine 必须有明确所有者、退出条件和等待路径；并发数量必须有界。
- 网络、命令、下载和后台任务必须接收并传播 `context.Context`。
- 不记录完整 catalog 命令、完整环境变量、凭据、令牌或加密密钥。
- 不在代码、测试、示例、配置或日志中加入真实凭据。

## 6. 配置与模型规范

[`internal/config/template/default_config.json`](../internal/config/template/default_config.json) 是默认配置模板。配置解析必须继续拒绝：

- 未知字段和多余顶层 JSON 值；
- 不支持的 `schema_version`；
- 非法枚举、越界数值、重复 ID 和重复非空 identity；
- 缺失的真正必填字段或不可执行的更新模式。

新增配置字段时必须同步更新：

1. `internal/model` 的结构和稳定词汇；
2. `internal/config` 的默认值、归一化与严格校验；
3. 默认配置模板；
4. 必要的 TUI 编辑界面和中英文 i18n；
5. 用户文档和配置示例；
6. 兼容或迁移策略及回归测试。

`config.json` 是受信任的可执行配置。应用运行结果只写入 `status_managed`；扫描临时观察不进入用户配置。`identity` 只负责 Scanner 的识别和去重，不得用于 Provider 路由。

## 7. Shell、Provider 与下载规范

`provider.actions` 是完整 shell 脚本。不要拆分、拼接或改变多行、管道、函数与 heredoc 语义。

- 系统、发行版、架构和 shell 选择统一来自 `pkg/runtime/system_info.go`。
- 动态值使用既有模板渲染与 shell 引用能力，不用字符串拼接注入路径、版本、包名或 URL。
- 外部进程在独立进程组中运行，并响应取消和超时。
- stdout/stderr、HTTP 响应、日志、事件队列和重试都必须保持有界。

新增 Provider 时：

- 在 `internal/updater/provider` 实现实际支持的最小能力接口；
- 通过 Registry 注册，不在引擎中增加 Provider 名称分支；
- 不注册没有真实实现的空能力；
- 支持并发调用并及时响应取消；
- 使用共享 HTTP source，测试使用 `httptest.Server` 或自定义 transport；
- 缺少必要 package 时返回明确的应用级错误，不猜测或写回配置。

下载器当前支持 `aria2c` 和 `curl`。额外参数必须经过现有 adapter 校验，不得覆盖程序控制的 URL、输出路径、配置、进度或副作用选项。

## 8. Scanner 规范

新增扫描域或包生态时，在 `internal/scanner/handler` 实现 `Handler`，并返回候选、排除别名、完成状态和错误。

- Handler 不持久化数据，也不依赖 UI、Service 或 Config。
- 根 Scanner 统一负责 identity、排除、匹配、合并和状态保留。
- 每种发现类型都应定义稳定 identity；identity 缺失时仍使用既有路径、package 或名称信号。
- 合并时保留用户维护的 Provider、action、更新模式和其他策略字段。
- 包管理器清单不完整时，不得误删未观察到的已纳管项目。
- 测试使用临时目录、临时脚本和替身，不依赖贡献者机器的实际安装状态。

## 9. 并发、持久化与安全

- 应用任务使用有界 worker pool，不按应用数量创建无界 goroutine。
- 同一批次只对等待和执行中的应用 ID 去重；已完成应用可以再次加入。
- 配置修改只通过配置 facade，并由 Service 持有业务锁范围。
- 一次操作只提交一次完整配置；必须保留 revision、磁盘基线和外部修改检测。
- 配置使用同目录临时文件、文件 `fsync`、原子 rename 和目录 `fsync`。
- 取消或上下文失败时不保存部分运行结果；日志失败不得改变操作结果。
- 配置、锁和日志保持当前用户所有，不能被组或其他用户写入。
- 所有日志和用户可见错误都经过敏感信息脱敏。

并发变更至少覆盖乱序、重复、取消和关闭竞争；工具链支持时运行 `go test -race ./...`。

## 10. TUI 与国际化

- 面向用户的固定文案通过 `pkg/i18n` 获取，并同时维护 `zh.json` 与 `en.json`。
- 外部命令输出、JSON 字段、状态码、Provider 和日志事件名不翻译。
- TUI 只有事件循环可以修改 view model；后台任务只发送有界事件。
- 正常退出、错误、信号和取消路径都必须恢复终端状态。
- 维持 80×24 最小完整布局、中文双宽字符和更小终端降级。
- 字母快捷键区分大小写；界面 footer 显示的动作必须与实际允许动作一致。

可见 TUI 变更应测试键序列、焦点状态、隐藏键无副作用、中文布局、小终端、滚动、取消和关闭路径。Pull Request 有帮助时应附截图或短录屏。

## 11. 测试策略

行为变更先运行最小相关测试：

```bash
go test ./internal/config -run TestName -count=1
```

测试不得安装或更新真实软件、修改真实 TendKit catalog、访问真实 Provider，或下载大型制品。文件测试使用 `t.TempDir()`；网络测试使用本地服务器或自定义 transport；命令与下载器使用临时脚本替身。

高风险区域需要包含成功与失败用例：

- 严格配置解析、schema、默认值与文件权限；
- 版本提取、归一化和比较；
- shell 模板、引用和敏感环境过滤；
- Provider 重试、限流、取消与资产选择；
- worker 状态机、动态批次和下载校验；
- Scanner identity、去重、不完整清单和恢复；
- i18n 键、格式参数和 TUI 状态矩阵。

## 12. 提交前检查

开发过程中先运行聚焦测试。提交 Pull Request 前至少运行：

```bash
gofmt -w <changed-go-files>
go test ./...
go vet ./...
go build ./...
git diff --check
```

已安装固定版本质量工具时，运行：

```bash
scripts/verify-go-quality.sh
```

若某项因平台或工具不可用而无法运行，在 Pull Request 中记录准确命令、环境、失败原因和未覆盖风险；不得把未运行的检查表述为通过。

仓库自动化分为三层：

- [Test](../.github/workflows/test.yml) 对 Pull Request 和 `main` push 运行聚焦与全量测试、TUI 竞态检查、`go vet`、构建和发布快照。
- [Nightly](../.github/workflows/nightly.yml) 增加全量竞态检测及重复 PTY/TUI 平台测试。
- [Release](../.github/workflows/release.yml) 只接受属于 `main`、关联 Pull Request 检查通过且带 `v` 前缀的签名 annotated SemVer tag，并在发布前创建和验证 Draft Release。

这些 workflow 不能替代 Pull Request 所需的聚焦本地验证证据。

## 13. Commit 与 Pull Request

Commit 标题应清晰并使用祈使语气。Commit 标题推荐使用 Conventional Commits 前缀，Pull Request 标题应使用该格式：

```text
<type>[optional scope]: <description>
```

常用类型包括 `feat`、`fix`、`docs`、`refactor`、`perf`、`test`、`build`、`ci` 和 `chore`。

Pull Request 应当：

- 说明问题、范围、方案和重要权衡；
- 适用时使用 `Closes #123` 关联 Issue；
- 列出准确的验证命令和结果；
- 为行为变更补充测试，为用户可见变更补充中英文文档；
- 指出平台特有、未测试行为和剩余风险；
- 避免生成文件、构建产物、本地配置、日志、凭据、令牌、加密密钥和无关格式调整；
- 可视 TUI 变更在有帮助时提供截图或短录屏。

欢迎用 Draft Pull Request 提前讨论设计。当范围稳定、相关检查通过且描述包含足以复现和评估的证据时，Pull Request 才适合进入最终审查。
