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
- 支持自定义供应商名称和 Base URL
- 支持为任意供应商保存自定义模型名
- 支持 TUI 交互式选择供应商并配置 API Key
- 支持方向键选择 provider 和模型
- 支持显示已保存 API Key 的掩码摘要
- 支持一键切换 `~/.claude/settings.json`
- 切换前自动备份原始配置
- 仅更新受管理的 `env` 字段，避免覆盖其他自定义配置

## 当前支持的供应商

| Provider | Base URL | 默认模型 |
| --- | --- | --- |
| `deepseek` | `https://api.deepseek.com/anthropic` | `deepseek-v4-pro[1m]` |
| `minimax-cn` | `https://api.minimaxi.com/anthropic` | `MiniMax-M2.7` |
| `minimax-global` | `https://api.minimax.io/anthropic` | `MiniMax-M2.7` |
| `openrouter` | `https://openrouter.ai/api` | `anthropic/claude-sonnet-4.6` |
| `opencode-go` | `https://opencode.ai/zen/go` | `minimax-m2.7` |

此外也支持自定义供应商。自定义供应商会保存：

- 显示名称
- `ANTHROPIC_BASE_URL`
- API Key
- 默认模型名

其中：

- `minimax-cn` 对应 MiniMax 中国区 Token Plan
- `minimax-global` 对应 MiniMax 国际区 Token Plan
- `openrouter` 默认使用 OpenRouter 官方 Claude 映射：haiku、sonnet、opus 会分别写入对应的官方模型；如果输入自定义模型名，则三档都会使用这个自定义模型
- `deepseek` 使用 DeepSeek Anthropic 兼容接口，API Key 会写入 `ANTHROPIC_AUTH_TOKEN`

MiniMax 中国区参考官方 CN 文档：

- 文本生成: https://platform.minimaxi.com/docs/guides/text-generation
- Claude Code: https://platform.minimaxi.com/docs/token-plan/claude-code

## 一键安装（推荐）

直接复制对应平台的命令到终端执行：

### macOS Intel

```bash
curl -fsSL https://github.com/doublepi123/claude_switch/releases/latest/download/claude-switch-darwin-amd64.tar.gz | tar xz && mv cs ~/.local/bin/cs && chmod +x ~/.local/bin/cs
```

### macOS Apple Silicon

```bash
curl -fsSL https://github.com/doublepi123/claude_switch/releases/latest/download/claude-switch-darwin-arm64.tar.gz | tar xz && mv cs ~/.local/bin/cs && chmod +x ~/.local/bin/cs
```

### Linux x86_64

```bash
curl -fsSL https://github.com/doublepi123/claude_switch/releases/latest/download/claude-switch-linux-amd64.tar.gz | tar xz && mv cs ~/.local/bin/cs && chmod +x ~/.local/bin/cs
```

### Linux ARM64

```bash
curl -fsSL https://github.com/doublepi123/claude_switch/releases/latest/download/claude-switch-linux-arm64.tar.gz | tar xz && mv cs ~/.local/bin/cs && chmod +x ~/.local/bin/cs
```

### Windows x86_64 (PowerShell)

```powershell
Invoke-WebRequest -Uri "https://github.com/doublepi123/claude_switch/releases/latest/download/claude-switch-windows-amd64.zip" -OutFile "$env:TEMP\claude-switch.zip"; Expand-Archive -Path "$env:TEMP\claude-switch.zip" -DestinationPath "$env:TEMP\claude-switch" -Force; New-Item -Path "$env:LOCALAPPDATA\Programs\claude-switch\bin" -ItemType Directory -Force | Out-Null; Move-Item -Path "$env:TEMP\claude-switch\cs.exe" -Destination "$env:LOCALAPPDATA\Programs\claude-switch\bin\" -Force; Remove-Item -Path "$env:TEMP\claude-switch.zip","$env:TEMP\claude-switch" -Force -ErrorAction SilentlyContinue
```

安装完成后验证：

```bash
cs list
cs --version
```

## 从源码安装

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

### 从源码构建

需要 Go 1.20+。

```bash
go build -o cs .
```

如需自定义安装目录：

```bash
INSTALL_DIR=/usr/local/bin ./scripts/install.sh
```

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
cs
```

命令会以 TUI 方式引导你：

- 用方向键选择供应商
- 首次为该供应商保存 API Key
- 再进入模型选择页
- 在 TUI 内切换模型
- 也可以创建自定义供应商
- 也可以输入任意自定义模型名
- 显示当前已保存 API Key 的掩码摘要
- 自动保存配置
- 立即切换当前 Claude Code 到所选供应商

如果该供应商之前已经保存过 API Key，`cs` 会直接复用，不会要求你重复输入。

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

查看当前版本：

```bash
cs --version
```

### 2. 交互式配置

直接运行 `cs` 即可进入 TUI：

```bash
cs
```

TUI 操作方式：

- 一级 `Providers`
  - `↑` / `↓`：切换 provider
  - `Enter` / `→`：进入 provider 详情页
  - 选择 `custom...` 可创建自定义供应商
- 二级 `Provider details`
  - `Enter` / `→`：进入下一步
  - 如果当前 provider 还没有已保存 key，会先要求输入并保存 API Key，再进入模型页
  - `k`：立即修改 API Key
  - `←` / `q`：返回 provider 列表
- 三级 `Models`
  - `↑` / `↓`：切换模型
  - `c`：输入任意自定义模型名，并保存为该 provider 的默认模型
  - `Enter`：确认并应用
  - `k`：立即修改 API Key
  - `←` / `q`：返回 provider 详情页
- `q`：退出

如果你已经保存过某个供应商的 API Key，重新执行 `cs` 时会直接复用。

如果想强制重新输入某个供应商的 API Key：

```bash
cs --reset-key
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
cs set-key deepseek sk-xxx
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

自定义供应商建议通过 `cs` TUI 创建，因为它需要同时保存名称、Base URL、API Key 和模型名。

### 5. 切换供应商

```bash
cs switch minimax-cn
cs switch minimax-global
cs switch openrouter
cs switch deepseek
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

对于预设 provider，如果你想使用未内置的模型名，也可以在 TUI 的模型页按 `c` 直接输入任意模型名，后续会作为该 provider 的默认模型保存。

对 `openrouter` 来说，选择内置的 Claude 模型时会保留官方三档映射：

- `ANTHROPIC_DEFAULT_HAIKU_MODEL=anthropic/claude-haiku-4.5`
- `ANTHROPIC_DEFAULT_SONNET_MODEL=anthropic/claude-sonnet-4.6`
- `ANTHROPIC_DEFAULT_OPUS_MODEL=anthropic/claude-opus-4.7`

如果通过 `--model` 或 TUI 自定义模型输入未内置的模型名，则 `ANTHROPIC_MODEL`、haiku、sonnet、opus 都会写成这个自定义模型。

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
- `CLAUDE_CODE_SUBAGENT_MODEL`
- `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`
- `CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK`
- `CLAUDE_CODE_EFFORT_LEVEL`

大多数 provider 的 API Key 会写入 `ANTHROPIC_API_KEY`。`deepseek` 会写入 `ANTHROPIC_AUTH_TOKEN`。工具会在切换时清理另一种旧鉴权字段，避免 Claude Code 出现鉴权冲突提示。

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
cs
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
