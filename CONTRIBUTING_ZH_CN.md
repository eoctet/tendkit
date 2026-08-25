# 为 TendKit 贡献

[English](CONTRIBUTING.md) | [简体中文](CONTRIBUTING_ZH_CN.md)

感谢你帮助改进 TendKit。本文说明如何报告问题、提出变更、准备代码以及提交便于审查的 Pull Request。

## 开始之前

- 提交前搜索已有 Issue 和 Pull Request，避免重复。
- 使用 Issue Form 报告 Bug 或功能建议，并只提供适合公开的信息。
- 不要在公开 Issue 中披露漏洞、凭据、令牌、加密密钥、私有路径或未经脱敏的日志。安全问题请遵循 [`SECURITY_ZH_CN.md`](SECURITY_ZH_CN.md)。
- 实质性功能、公开接口或配置/schema 变更、迁移、安全边界变更或跨组件设计，应先创建 Issue，并与维护者确认范围后再开始实现。
- 每个贡献只处理一个问题；无关清理应单独提出。

文档、测试或明确局部行为的小修正通常可以直接提交。变更影响长期产品或架构契约时，维护者可能要求先创建 Issue。

## 开发环境

必需工具：

- Git
- Go 1.23 或更高版本；使用 [`go.mod`](go.mod) 声明的工具链
- Python 3，用于仓库 JSON 检查
- macOS 或受支持的 Linux 环境；macOS 覆盖完整的平台、Application Bundle 和 PTY 测试面，Linux 支持非 macOS 实现与测试

[`scripts/verify-go-quality.sh`](scripts/verify-go-quality.sh) 列出了可选质量工具及固定版本：`staticcheck`、`govulncheck` 和 `gosec`。缺失时脚本会显示安装命令，但不会自动安装或升级工具。

```bash
git clone https://github.com/eoctet/tendkit.git
cd tendkit
go test ./...
go build ./...
```

测试不得更新真实软件、修改真实 TendKit catalog 或依赖在线 Provider 响应。请使用临时目录、本地测试服务器或替身。

## 项目契约与目录

修改行为前使用以下来源：

- [`README_ZH_CN.md`](README_ZH_CN.md) 与公开的[产品使用手册](wiki/user-manual_ZH_CN.md)定义面向用户的产品行为与边界。
- 本文定义贡献流程；公开的[开发与技术规范](wiki/development-and-technical-guide_ZH_CN.md)定义详细工程与技术标准。
- [`internal/config/template/default_config.json`](internal/config/template/default_config.json)、严格解析和测试定义配置契约。
- 可执行代码与测试代表当前实现。

主要包职责：

- `cmd/tendkit`：进程入口、CLI 参数、信号、退出码和依赖装配
- `internal/service`：扫描与更新事务以及持久化边界
- `internal/model`：共享领域词汇与限制
- `internal/config`：严格 JSON、锁、快照和原子写回
- `internal/scanner`：发现、identity、排除和合并
- `internal/updater`：Provider 解析、并发、下载、校验和更新执行
- `internal/ui`：单事件循环 TUI 状态机
- `pkg/*`：runtime、HTTP、日志、i18n、下载、metadata、错误和版本等通用能力

实现与文档冲突时，应明确指出冲突，不要静默改变公开契约。

## 实施变更

1. 复现问题或明确验收条件。
2. 行为变更先添加能因目标原因失败的聚焦测试。
3. 以最小改动满足验收条件，避免推测性配置、依赖和抽象。
4. 同步更新受影响的公开用户或贡献者文档、配置示例和两个语言版本。
5. 先运行聚焦测试，再按风险扩大验证范围。

Go 代码必须使用 `gofmt` 格式化。遵循现有包边界和错误词汇。新 Provider 只实现真实支持的能力；新扫描域必须保留取消、不完整清单保护、identity、排除和合并语义。配置与命令执行属于安全敏感区域，需要聚焦的负向测试。

## 测试与质量检查

开发时先运行最小相关包测试，例如：

```bash
go test ./internal/config -run TestName -count=1
```

提交 Pull Request 前至少运行：

```bash
gofmt -w <changed-go-files>
go test ./...
go vet ./...
go build ./...
git diff --check
```

已安装固定版本质量工具时，运行完整仓库检查：

```bash
scripts/verify-go-quality.sh
```

完整检查包括格式校验、单元与集成测试、竞态检测、`go vet`、构建、静态分析、漏洞与安全扫描、JSON 校验和 Git 空白检查。若平台或工具导致检查无法运行，请在 Pull Request 中记录准确命令、失败、环境和未覆盖风险；未运行的检查不得表述为通过。

仓库自动化分为三层。[Test](.github/workflows/test.yml) 对 Pull Request 和 `main` push 运行聚焦与全量测试、TUI 竞态检查、`go vet`、构建和发布快照；[Nightly](.github/workflows/nightly.yml) 增加全量竞态检测及重复 PTY/TUI 平台测试；[Release](.github/workflows/release.yml) 只接受属于 `main`、关联 Pull Request 检查通过且带 `v` 前缀的签名 annotated SemVer tag，并在发布前创建和验证 Draft Release。这些 workflow 不能替代 Pull Request 所需的聚焦本地验证证据。

## Commit 与 Pull Request

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

## 审查与验收

维护者会从正确性、契约兼容性、安全边界、测试、文档和范围审查贡献。实质性变更可能被要求拆小，或先独立批准设计。审批不能替代 CI，也不能替代需要真实平台确认时的用户验收。

提交贡献即表示你同意按照仓库的 [MIT License](LICENSE) 授权该贡献。
