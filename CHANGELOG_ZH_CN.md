# 变更日志

## [1.0.0] - 2026-09-03

TendKit 1.0.0 是首个稳定版本。

### ✨ 版本亮点

- 在一个 TUI 中管理开发工具。
- 修改清单前先审核扫描结果。
- 按应用策略检查、更新、下载或安装工具。
- 支持中英文界面和日志。

### 🔍 工具发现

- 发现 `PATH` 中受支持的 CLI，包括同一命令的多个安装实例。
- 发现 `/Applications` 和 `~/Applications` 中的 macOS 开发应用。
- 发现全局 Python、npm、Go、uv、RubyGems、Homebrew 和 Cargo 包。
- 扫描结果缺失或有歧义时保留已有条目。

### 🔄 Provider 与操作

- 支持 GitHub Releases、GitHub tags、npm、PyPI、uv、JetBrains、Go、Node.js LTS、Sparkle、Homebrew、Cargo 和自定义 action。
- 支持 `check`、`auto`、`download` 和 `install` 四种模式。
- 支持并发批次、取消、进度显示和 JSONL 日志。

### 🛡️ 安全

- 只在用户明确操作后执行变更，不添加提权操作。
- 写入前校验配置，并原子保存。
- 从错误和日志中脱敏敏感值。
- 有可信校验信息时验证 SHA-256。
- 发布签名制品、校验和及构建来源证明。

### 🖥️ 兼容性

- macOS 和 Linux
- `arm64` 和 `x86_64`
- 不支持 Windows、项目局部依赖和虚拟环境。
- 下载制品需要 `curl` 或 `aria2c`。

### 🧪 相对 v0.1.0-rc.2 的变化

- 稳定 CLI、TUI 和 runner 超时测试。
- 稳定本地 Go 质量工具链。
- 没有用户可见行为或配置变更。

### 📦 升级

无需迁移配置：

```bash
go install github.com/eoctet/tendkit/cmd/tendkit@v1.0.0
```

安装和配置详情见[用户手册](wiki/user-manual_ZH_CN.md)。

[1.0.0]: https://github.com/eoctet/tendkit/compare/v0.1.0-rc.2...v1.0.0
