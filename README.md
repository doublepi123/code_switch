# claude-switch

`claude-switch` 是一个用于切换 Claude Code 后端供应商的 Go 命令行工具，安装后的命令名是 `cs`。

直接输入 `cs` 就会进入 TUI 配置界面。

它会更新 Claude Code 使用的 `settings.json`，让你在不同兼容供应商之间快速切换，同时保留无关配置。

仓库地址：

```bash
git clone git@github.com:doublepi123/claude_switch.git
cd claude_switch
```

## 功能

- 支持列出当前内置供应商
- 支持查看当前 Claude Code 指向的供应商
- 支持保存各供应商 API Key
- 支持 TUI 交互式选择供应商并配置 API Key
- 支持方向键选择 provider 和模型
- 支持显示已保存 API Key 的掩码摘要
- 支持一键切换 `~/.claude/settings.json`
- 切换前自动备份原始配置
- 仅更新受管理的 `env` 字段，避免覆盖其他自定义配置

## 当前支持的供应商

| Provider | Base URL | 默认模型 |
| --- | --- | --- |
| `minimax-cn` | `https://api.minimaxi.com/anthropic` | `MiniMax-M2.7` |
| `minimax-global` | `https://api.minimax.io/anthropic` | `MiniMax-M2.7` |
| `openrouter` | `https://openrouter.ai/api` | `anthropic/claude-sonnet-4.6` |
| `opencode-go` | `https://opencode.ai/zen/go` | `minimax-m2.7` |

其中：

- `minimax-cn` 对应 MiniMax 中国区 Token Plan
- `minimax-global` 对应 MiniMax 国际区 Token Plan

MiniMax 中国区参考官方 CN 文档：

- 文本生成: https://platform.minimaxi.com/docs/guides/text-generation
- Claude Code: https://platform.minimaxi.com/docs/token-plan/claude-code

## 安装

要求：

- Go 1.20 或更高版本

推荐先克隆仓库，再执行安装脚本。

### macOS / Linux

```bash
chmod +x scripts/install.sh
./scripts/install.sh
```

默认会安装到：

```text
~/.local/bin/cs
```

安装完成后可验证：

```bash
cs list
```

如需自定义安装目录：

```bash
INSTALL_DIR=/usr/local/bin ./scripts/install.sh
```

### Windows PowerShell

```powershell
.\scripts\install.ps1
```

默认会安装到：

```text
$HOME\AppData\Local\Programs\claude-switch\bin\cs.exe
```

安装完成后可验证：

```powershell
cs.exe list
```

如需自定义安装目录：

```powershell
.\scripts\install.ps1 -InstallDir 'C:\Tools\claude-switch'
```

### 手动构建

如果你不想使用安装脚本，也可以直接构建：

```bash
go build -o cs .
```

如果只想临时运行，也可以直接：

```bash
go run .
```

## 首次使用

第一次使用建议按下面顺序执行：

### 1. 直接执行交互式配置

```bash
cs configure
```

命令会以 TUI 方式引导你：

- 用方向键选择供应商
- 在 TUI 内切换模型
- 首次为该供应商输入 API Key
- 显示当前已保存 API Key 的掩码摘要
- 自动保存配置
- 立即切换当前 Claude Code 到所选供应商

如果该供应商之前已经保存过 API Key，`cs configure` 会直接复用，不会要求你重复输入。

### 2. 确认当前生效配置

```bash
cs current
```

如果输出里能看到目标 `provider`、`base_url` 和 `model`，说明首次配置已完成。

## 命令说明

### 1. 查看可用供应商

```bash
cs list
```

输出包含供应商名称、Base URL 和默认模型。

### 2. 交互式配置

```bash
cs
```

或：

```bash
cs configure
```

TUI 操作方式：

- `↑` / `↓`：切换 provider
- `←` / `→`：切换模型
- `Enter`：确认并应用
- `q`：退出

如果你已经保存过某个供应商的 API Key，重新执行 `cs configure` 时会直接复用。

如果你想强制重填该供应商的 API Key：

```bash
cs configure --reset-key
```

### 3. 查看当前配置

```bash
cs current
```

默认读取：

```text
~/.claude/settings.json
```

也可以通过 `--claude-dir` 指定 Claude 配置目录：

```bash
cs current --claude-dir /path/to/.claude
```

### 4. 保存 API Key

```bash
cs set-key minimax-cn sk-xxx
cs set-key minimax-global sk-xxx
cs set-key openrouter sk-or-xxx
```

历史兼容别名：

```bash
cs set-key minimax sk-xxx
cs set-key minimax-cn-token sk-xxx
cs set-key minimax-global-token sk-xxx
```

保存后会写入：

```text
~/.claude-switch/config.json
```

如果你不想落盘保存，也可以在切换时临时传入 `--api-key`。

### 5. 切换供应商

```bash
cs switch minimax-cn
cs switch minimax-global
cs switch openrouter
cs switch opencode-go
```

MiniMax 中国区：

```bash
cs switch minimax-cn
```

MiniMax 国际区：

```bash
cs switch minimax-global
```

如果没有提前保存 API Key，也可以在切换时直接传入：

```bash
cs switch openrouter --api-key sk-or-xxx
```

如果本地还没有 `~/.claude/settings.json`，工具会自动创建它。

### 6. 覆盖默认模型

```bash
cs switch opencode-go --model minimax-m2.7
```

对 `opencode-go` 来说，这里应传 OpenCode Go 在 Anthropic 兼容接口下支持的实际模型 ID，例如：

```bash
cs switch opencode-go --model minimax-m2.7
cs switch opencode-go --model minimax-m2.5
```

不要传 `opencode-go/minimax-m2.7` 这类前缀形式；那是 OpenCode 自身配置里使用的格式，不是这里这个 Anthropic 兼容接口要的模型 ID。

这个工具当前把 `opencode-go` 作为 Anthropic 兼容供应商接入，因此应使用文档中对应 `https://opencode.ai/zen/go/v1/messages` 的模型，例如 `minimax-m2.7`、`minimax-m2.5`。

### 7. 指定 Claude 配置目录

```bash
cs switch minimax-cn --claude-dir /path/to/.claude
```

这对测试环境、多套 Claude 配置，或者首次调试很有用。

## 配置文件行为

默认情况下，工具会操作以下文件：

- Claude 配置：`~/.claude/settings.json`
- 本工具配置：`~/.claude-switch/config.json`

在执行 `switch` 时：

- 如果 `settings.json` 已存在，会先创建一个带时间戳的备份文件
- 只会清理并重写本工具管理的环境变量
- 其他字段和未受管理的环境变量会保留

当前受管理的环境变量包括：

- `ANTHROPIC_BASE_URL`
- `ANTHROPIC_API_KEY`
- `ANTHROPIC_MODEL`
- `ANTHROPIC_DEFAULT_HAIKU_MODEL`
- `ANTHROPIC_DEFAULT_SONNET_MODEL`
- `ANTHROPIC_DEFAULT_OPUS_MODEL`
- `API_TIMEOUT_MS`
- `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`

API Key 会写入 `ANTHROPIC_API_KEY`。工具也会清理旧的 `ANTHROPIC_AUTH_TOKEN`，避免 Claude Code 出现鉴权冲突提示。

## 示例

先保存 Key：

```bash
cs set-key minimax-cn sk-xxx
```

再切换：

```bash
cs switch minimax-cn
cs current
```

首次配置推荐直接使用交互模式：

```bash
cs configure
cs current
```

直接临时切换而不保存 Key：

```bash
cs switch openrouter --api-key sk-or-xxx
```

切换并覆盖模型：

```bash
cs switch opencode-go --api-key your-key --model minimax-m2.7
```

如果你在使用 `opencode-go` 时看到类似下面的错误：

```text
401 {"type":"error","error":{"type":"ModelError","message":"Model opencode-go/minimax-m2.7 not supported"}}
```

把模型名改成裸 ID 即可，例如 `minimax-m2.7`。

## 测试

运行单元测试：

```bash
go test ./...
```

## 常见问题

### 1. 安装后找不到 `cs`

说明安装目录还没加入 `PATH`。

macOS / Linux 通常需要把下面路径加入 shell 配置：

```text
~/.local/bin
```

Windows 通常需要把下面路径加入用户 `Path`：

```text
$HOME\AppData\Local\Programs\claude-switch\bin
```

### 2. 切换前会不会覆盖我原来的 Claude 配置

不会整体覆盖。工具只会更新它自己管理的供应商相关环境变量，并在写入前自动备份已有的 `settings.json`。

### 3. 必须先执行 `set-key` 吗

不是必须。你也可以在 `switch` 时通过 `--api-key` 临时传入。

## 适用场景

- 在不同 Claude 兼容后端之间快速切换
- 为本地 Claude Code 环境维护统一的供应商配置
- 减少手动编辑 `settings.json` 的出错概率
