# code-switch

`code-switch` 是一个用于切换 Claude Code、Codex 和 OpenCode 后端供应商的 Go 命令行工具，安装后的命令名是 `cs`。

直接输入 `cs` 就会进入 TUI 配置界面。

它会更新 Claude Code 使用的 `settings.json`、Codex 使用的 `config.toml` 和 OpenCode 使用的 `opencode.json`，让你在兼容供应商之间快速切换，同时保留无关配置。未指定 `--agent` 时默认操作 Claude Code，兼容原有 `cs switch openrouter`、`cs current` 等命令。

配置以 API 协议为中心：每个供应商声明它暴露的协议端点（`anthropic-messages` / `openai-chat` / `openai-responses`），每个 agent 声明自己的原生协议。agent 与供应商有共同协议时直接写配置直连；没有共同协议时 `cs switch` 会自动通过内置的本地代理做协议转换（跨协议必须路由），详见「[直连与跨协议代理路由](#6-直连与跨协议代理路由---via)」。

仓库地址：

```bash
git clone git@github.com:doublepi123/code_switch.git
cd code_switch
```

## 功能

- 支持列出当前内置供应商
- 支持查看当前 Claude Code、Codex 或 OpenCode 指向的供应商
- 支持保存各供应商 API Key
- 支持自定义供应商名称、Base URL 和协议
- 支持为任意供应商保存自定义模型名
- 支持 TUI 交互式选择供应商并配置 API Key
- 支持方向键选择 provider 和模型
- 支持显示已保存 API Key 的掩码摘要
- 支持一键切换 `~/.claude/settings.json`
- 供应商按协议声明端点（`anthropic-messages` / `openai-chat` / `openai-responses`），同协议直连、跨协议自动路由
- 内置本地代理 daemon：三协议互转（非流式 + SSE 流式 + 工具调用），多 agent 路由按随机 token 分发
- `cs switch --via auto|direct|proxy` 控制直连或强制走代理
- 支持 Codex 直连 OpenAI 兼容端点（DeepSeek / Kimi / Ollama Cloud / OpenRouter），并可通过代理使用任意 Anthropic 兼容供应商
- 支持 Codex command-backed auth，API Key 不明文写入 TOML
- 支持恢复 Claude Code、Codex 或 OpenCode 官方配置
- 支持从旧 `~/.claude-switch/config.json` 迁移到 `~/.code-switch/config.json`
- 切换前自动备份原始配置
- 仅更新受管理的 `env` 字段，避免覆盖其他自定义配置

## 当前支持的供应商

Claude Code 支持：

| Provider | Base URL | 默认模型 |
| --- | --- | --- |
| `deepseek` | `https://api.deepseek.com/anthropic` | `deepseek-v4-pro[1m]` |
| `minimax-cn` | `https://api.minimaxi.com/anthropic` | `MiniMax-M2.7` |
| `minimax-global` | `https://api.minimax.io/anthropic` | `MiniMax-M2.7` |
| `openrouter` | `https://openrouter.ai/api` | `anthropic/claude-sonnet-4.6` |
| `opencode-go` | `https://opencode.ai/zen/go` | `minimax-m2.7` |
| `xiaomimimo-cn` | `https://token-plan-cn.xiaomimimo.com/anthropic` | `mimo-v2.5-pro` |
| `zhipu-cn` | `https://open.bigmodel.cn/api/anthropic` | `glm-5.2` |
| `ollama` | `http://localhost:11434` | `qwen3-coder` |
| `ollama-cloud` | `https://ollama.com` | `qwen3-coder:480b` |

此外也支持自定义供应商。自定义供应商会保存：

- 显示名称
- `ANTHROPIC_BASE_URL`
- API Key
- 默认模型名

其中：

- `minimax-cn` 对应 MiniMax 中国区 Token Plan，API Key 会写入 `ANTHROPIC_AUTH_TOKEN`
- `minimax-global` 对应 MiniMax 国际区 Token Plan，API Key 会写入 `ANTHROPIC_AUTH_TOKEN`
- `openrouter` 默认使用 OpenRouter 官方 Claude 映射：haiku、sonnet、opus 会分别写入对应的官方模型；如果输入自定义模型名，则三档都会使用这个自定义模型
- `deepseek` 使用 DeepSeek Anthropic 兼容接口，API Key 会写入 `ANTHROPIC_AUTH_TOKEN`
- `kimi-coding` 使用 Kimi Coding Anthropic 兼容接口，API Key 会写入 `ANTHROPIC_AUTH_TOKEN`
- `zhipu-cn` 对应智谱（BigModel）国内 GLM Coding Plan（`open.bigmodel.cn`），API Key 会写入 `ANTHROPIC_AUTH_TOKEN`；国际端点（`z.ai`）使用 `zai` 预设，可用别名 `zhipu` / `bigmodel` 切到国内端点
- `ollama` 使用本地 Ollama Anthropic 兼容接口，不要求 API Key；如果本地已经 `ollama signin`，也可以使用 `:cloud` 后缀模型
- `ollama-cloud` 直接连接 `https://ollama.com`，需要在 Ollama settings 里创建 API Key；Claude Code 切换会把 Key 写入 `ANTHROPIC_AUTH_TOKEN`
- Claude Code 使用 `ollama-cloud` 时，haiku、sonnet、opus 和 subagent 模型都会写成所选主模型，不会混用其他模型

Codex 可直连以下拥有 OpenAI 兼容端点的供应商：

| Provider | Base URL | 协议 |
| --- | --- | --- |
| `deepseek` | `https://api.deepseek.com/v1` | `openai-responses` |
| `kimi-coding` | `https://api.kimi.com/coding/v1` | `openai-responses` |
| `ollama-cloud` | `https://ollama.com/v1` | `openai-responses` |
| `openrouter` | `https://openrouter.ai/api/v1` | `openai-responses` |

仅提供 Anthropic 兼容端点的供应商（如 `zai`、`zhipu-cn`、`minimax-cn`、`volcengine`）同样可以给 Codex 使用：`cs switch <provider> --agent codex` 会自动通过本地代理做协议转换，详见「直连与跨协议代理路由」。

Codex 直连时写入 `~/.codex/config.toml`，按端点协议使用 `wire_api = "responses"` 或 `wire_api = "chat"`，并使用 command-backed auth。API Key 只保存到 `~/.code-switch/config.json`，不会明文写入 Codex TOML；Codex 运行时会通过 `cs token <provider> --agent codex` 读取已保存的 key。同时会写入 `approvals_reviewer = "user"`，避免自动审批 reviewer 的内部模型被路由到第三方供应商。

MiniMax 中国区参考官方 CN 文档：

- 文本生成: https://platform.minimaxi.com/docs/guides/text-generation
- Claude Code: https://platform.minimaxi.com/docs/token-plan/claude-code

OpenCode Go 参考文档：

- https://opencode.ai/docs/zh-cn/go/

DeepSeek API 参考文档：

- https://api-docs.deepseek.com/zh-cn/

Ollama / Ollama Cloud 参考文档：

- https://docs.ollama.com/integrations/claude-code
- https://docs.ollama.com/cloud

## 一键安装（推荐）

macOS / Linux 直接执行：

```bash
curl -fsSL https://raw.githubusercontent.com/doublepi123/code_switch/main/scripts/install-release.sh | sh
```

脚本会自动识别 macOS / Linux 和 CPU 架构，默认安装到 `~/.local/bin/cs`。如果安装目录不在 `PATH`，脚本会写入当前 shell 的 profile，并打印当前终端立即生效所需的 `export` 命令。

如需自定义安装目录：

```bash
curl -fsSL https://raw.githubusercontent.com/doublepi123/code_switch/main/scripts/install-release.sh | INSTALL_DIR=/usr/local/bin sh
```

### Windows x86_64 (PowerShell)

```powershell
$installPath = "$env:LOCALAPPDATA\Programs\code-switch\bin"; [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12; $ProgressPreference = 'SilentlyContinue'; Invoke-WebRequest -UseBasicParsing -Uri "https://github.com/doublepi123/code_switch/releases/latest/download/code-switch-windows-amd64.zip" -OutFile "$env:TEMP\code-switch.zip"; Expand-Archive -Path "$env:TEMP\code-switch.zip" -DestinationPath "$env:TEMP\code-switch" -Force; New-Item -Path $installPath -ItemType Directory -Force | Out-Null; Move-Item -Path "$env:TEMP\code-switch\cs.exe" -Destination "$installPath\" -Force; Remove-Item -Path "$env:TEMP\code-switch.zip","$env:TEMP\code-switch" -Recurse -Force -ErrorAction SilentlyContinue; if ($env:Path -split ';' -notcontains $installPath) { [Environment]::SetEnvironmentVariable('Path', "$installPath;$([Environment]::GetEnvironmentVariable('Path','User'))", 'User'); $env:Path = "$installPath;$env:Path" }; cs --version
```

### Windows ARM64 (PowerShell)

```powershell
$installPath = "$env:LOCALAPPDATA\Programs\code-switch\bin"; [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12; $ProgressPreference = 'SilentlyContinue'; Invoke-WebRequest -UseBasicParsing -Uri "https://github.com/doublepi123/code_switch/releases/latest/download/code-switch-windows-arm64.zip" -OutFile "$env:TEMP\code-switch.zip"; Expand-Archive -Path "$env:TEMP\code-switch.zip" -DestinationPath "$env:TEMP\code-switch" -Force; New-Item -Path $installPath -ItemType Directory -Force | Out-Null; Move-Item -Path "$env:TEMP\code-switch\cs.exe" -Destination "$installPath\" -Force; Remove-Item -Path "$env:TEMP\code-switch.zip","$env:TEMP\code-switch" -Recurse -Force -ErrorAction SilentlyContinue; if ($env:Path -split ';' -notcontains $installPath) { [Environment]::SetEnvironmentVariable('Path', "$installPath;$([Environment]::GetEnvironmentVariable('Path','User'))", 'User'); $env:Path = "$installPath;$env:Path" }; cs --version
```

安装完成后验证：

```bash
cs list
cs --version
```

如果脚本提示当前终端需要执行 `export PATH=...`，先执行提示中的命令，或者重新打开一个终端。

后续升级到 GitHub Release 最新版本：

```bash
cs upgrade
```

`cs upgrade` 会先检查当前版本，只有 GitHub Release 存在更新版本时才会下载并替换本地 `cs`。

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

需要 Go 1.22+。

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

从 GitHub Release 安装的二进制会显示对应 release tag。从源码直接构建时，如果当前提交没有 tag，版本会显示为 `dev`；发布构建会通过 `-ldflags` 注入 tag。

升级到最新 GitHub Release：

```bash
cs upgrade
```

命令会先比较当前版本和最新 release tag；如果已经是最新版，会直接退出，不会重新下载。

安装指定版本：

```bash
cs upgrade --tag v1.2.3
```

### 2. 交互式配置

直接运行 `cs` 即可进入 TUI：

```bash
cs
```

TUI 操作方式：

- 一级 `Agents`
  - `Claude Code`
  - `Codex`
  - `OpenCode`
- 二级 `Providers`
  - `↑` / `↓`：切换 provider
  - `Enter` / `→`：进入 provider 详情页
  - 列表会显示每个 provider 的协议徽标（anthropic / chat / responses）
  - 选择 `custom...` 可创建自定义供应商（可选择协议）
  - 选择 `Restore official config...` 可恢复所选 agent 的官方配置
- 三级 `Provider details`
  - `Enter` / `→`：进入下一步
  - 如果当前 provider 还没有已保存 key，会先要求输入并保存 API Key，再进入模型页
  - `k`：立即修改 API Key
  - `←` / `q`：返回 provider 列表
- 四级 `Models`
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
cs current --agent codex
cs current --agent opencode
```

默认读取：

```text
~/.claude/settings.json
```

Codex 默认读取：

```text
~/.codex/config.toml
```

OpenCode 默认读取：

```text
~/.config/opencode/opencode.json
```

如果某个 agent 当前指向本地代理，`cs current` 会显示对应路由的上游供应商、上游协议以及代理 daemon 的健康状态。

也可以通过 `--claude-dir` 指定 Claude 配置目录：

```bash
cs current --claude-dir /path/to/.claude
cs current --agent codex --codex-dir /path/to/.codex
```

### 4. 保存 API Key

```bash
cs set-key minimax-cn sk-xxx
cs set-key minimax-global sk-xxx
cs set-key openrouter sk-or-xxx
cs set-key deepseek sk-xxx
cs set-key ollama-cloud ollama-sk-xxx
```

历史兼容别名：

```bash
cs set-key minimax sk-xxx
cs set-key minimax-cn-token sk-xxx
cs set-key minimax-global-token sk-xxx
```

保存后会写入：

```text
~/.code-switch/config.json
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
cs switch ollama-cloud
cs switch ollama-cloud --agent codex --api-key ollama-sk-xxx
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
cs switch ollama-cloud --api-key ollama-sk-xxx
```

如果本地还没有 `~/.claude/settings.json`，工具会自动创建它。

如果本地还没有 `~/.codex/config.toml`，使用 `--agent codex` 时工具会自动创建它，并写入 Ollama Cloud 的 Responses API provider 配置：

```toml
model = "qwen3-coder:480b"
model_provider = "ollama-cloud"
approvals_reviewer = "user"

[model_providers.ollama-cloud]
name = "Ollama Cloud"
base_url = "https://ollama.com/v1"
wire_api = "responses"

[model_providers.ollama-cloud.auth]
command = "cs"
args = ["token", "ollama-cloud", "--agent", "codex"]
```

### 6. 直连与跨协议代理路由 (--via)

三个 agent 的原生协议：

| Agent | 客户端协议 | 可直连的供应商端点协议 |
| --- | --- | --- |
| `claude` | `anthropic-messages` | `anthropic-messages` |
| `codex` | `openai-responses` | `openai-responses`、`openai-chat` |
| `opencode` | `openai-chat` | `anthropic-messages`、`openai-chat` |

`cs switch` 会自动判定连接方式：

- agent 与供应商有共同协议 → **直连**（`mode: direct`），把供应商端点直接写进 agent 配置，与旧版本行为一致
- 没有共同协议 → **代理路由**（`mode: proxy`）：自动写入代理路由、为该 agent 生成随机 token、确保本地代理 daemon 运行，并把 agent 配置指向 `http://127.0.0.1:<port>`，由代理完成协议转换

`--via` 可以控制该判定：

```bash
cs switch deepseek --agent codex               # 同协议 → 直连
cs switch zai --agent codex                    # zai 只有 Anthropic 端点 → 自动走本地代理
cs switch deepseek --agent codex --via proxy   # 强制走代理
cs switch zai --agent codex --via direct       # 报错：跨协议必须通过代理路由
```

走代理时的输出示例：

```text
switched codex to Z.AI GLM Coding Plan via proxy
mode: proxy
client_protocol: openai-responses
upstream_protocol: anthropic-messages
local_proxy: http://127.0.0.1:18080/v1
provider: zai
model: glm-5.1
```

本地代理说明：

- 代理只监听 `127.0.0.1`，支持三协议互转的非流式、SSE 流式和工具调用请求；同协议请求走 passthrough 快路径
- 一个 daemon 同时服务多个 agent 的路由，按每个 agent 的随机 Bearer token 分发；token 保存在 `~/.code-switch/config.json`（权限 0600）
- 上游供应商的 API Key 只保存在 `~/.code-switch/config.json`，不会写入 agent 配置；agent 配置里只有本地代理地址和本地 token
- 切换会重写路由并重启 daemon，可能短暂中断其他正在走代理的 agent
- daemon 运行状态记录在 `~/.code-switch/proxy-state.json`，`cs current` 和 `cs doctor` 会显示代理路由与 daemon 健康状态

也可以用底层命令手动管理代理：

```bash
cs proxy configure codex --provider zai        # 只写路由，不启动 daemon
cs proxy preview codex                         # 查看解析后的路由
cs proxy start --agent codex                   # 启动 daemon
cs proxy status                                # 查看运行状态与全部路由
cs proxy stop                                  # 停止 daemon
```

### 7. 覆盖默认模型

```bash
cs switch opencode-go --model minimax-m2.7
```

对 `opencode-go` 来说，这里应传 OpenCode Go 在 Anthropic 兼容接口下支持的实际模型 ID，例如：

```bash
cs switch opencode-go --model minimax-m2.7
cs switch opencode-go --model minimax-m2.5
cs switch opencode-go --model deepseek-v4-pro
cs switch opencode-go --model deepseek-v4-flash
```

不要传 `opencode-go/minimax-m2.7` 这类前缀形式；那是 OpenCode 自身配置里使用的格式，不是这里这个 Anthropic 兼容接口要的模型 ID。

这个工具当前把 `opencode-go` 作为 Anthropic 兼容供应商接入，因此应使用文档中对应 `https://opencode.ai/zen/go/v1/messages` 的模型，例如 `minimax-m2.7`、`minimax-m2.5`、`deepseek-v4-pro`、`deepseek-v4-flash`。

OpenCode Go 文档里的 GLM、Kimi、MiMo、Qwen 等模型走的是 `https://opencode.ai/zen/go/v1/chat/completions`，不适合作为 Claude Code 的 Anthropic Base URL 直接使用。

对于预设 provider，如果你想使用未内置的模型名，也可以在 TUI 的模型页按 `c` 直接输入任意模型名，后续会作为该 provider 的默认模型保存。

对 `openrouter` 来说，选择内置的 Claude 模型时会保留官方三档映射：

- `ANTHROPIC_DEFAULT_HAIKU_MODEL=anthropic/claude-haiku-4.5`
- `ANTHROPIC_DEFAULT_SONNET_MODEL=anthropic/claude-sonnet-4.6`
- `ANTHROPIC_DEFAULT_OPUS_MODEL=anthropic/claude-opus-4.7`

如果通过 `--model` 或 TUI 自定义模型输入未内置的模型名，则 `ANTHROPIC_MODEL`、haiku、sonnet、opus 都会写成这个自定义模型。

### 8. 指定 Claude 配置目录

```bash
cs switch minimax-cn --claude-dir /path/to/.claude
cs switch ollama-cloud --agent codex --codex-dir /path/to/.codex
```

这对测试环境、多套 Claude 配置，或者首次调试很有用。

### 9. 进阶命令（脚本化 / 可观测 / 迁移）

```bash
# 机器可读输出，便于脚本集成
cs current --json                       # 当前各 agent 配置（JSON）
cs current --json --agent claude
cs list --json                          # 供应商列表（JSON）
cs models [provider] [--json]           # 某供应商的模型列表（无 provider 则用当前）
                                        #   ollama / openrouter 会动态发现模型

# 多 shell 导出（默认 bash/POSIX）
cs env deepseek --shell fish            # set -gx ...
cs env deepseek --shell pwsh            # $env:KEY = '...'

# 默认供应商 + 快捷切换
cs default deepseek                     # 设置默认
cs default                              # 查看默认
cs switch                               # 不带参数 → 切换到默认供应商
cs default --clear

# 批量连通性测试
cs test --all                           # 并发测试本 agent 全部供应商，打印汇总表
cs test --all --agent codex

# 变更预览（只读）
cs diff deepseek                        # 预览 switch 将对 settings.json env 产生的增/改/删
                                        #   API key 自动脱敏

# 健康检查
cs doctor                               # 检查 config 可解析、权限 0600、模型漂移、残留临时文件
cs doctor --json

# 备份管理（每次 switch 前会自动生成 settings.json.bak-*）
cs backups list                         # 列出各 agent 目录下的备份
cs backups prune --keep 1               # 每个源文件仅保留最新 1 份
cs backups prune --days 30              # 删除 30 天前的备份
cs backups prune --all                  # 删除全部（可用 --dry-run 预览）

# 配置迁移（多台机器）
cs export > my-cs.json                  # 导出完整配置（含密钥）
cs export --redact-keys > template.json # 导出但抹除密钥，便于分享供应商配置
cs import my-cs.json                    # 合并导入（同名供应商会被覆盖，需确认或 --force）
```

## 配置文件行为

默认情况下，工具会操作以下文件：

- Claude 配置：`~/.claude/settings.json`
- Codex 配置：`~/.codex/config.toml`
- OpenCode 配置：`~/.config/opencode/opencode.json`
- 本工具配置：`~/.code-switch/config.json`（含 API Key、代理路由和 per-agent token，权限 0600）
- 代理运行状态：`~/.code-switch/proxy-state.json`（daemon 的 pid、端口、实例 id）

启动时如果新的 `~/.code-switch/config.json` 不存在，但旧的 `~/.claude-switch/config.json` 存在，工具会迁移一次到新路径。旧文件不会被删除或修改。

在执行 `switch` 时：

- 如果 `settings.json` 已存在，会先创建一个带时间戳的备份文件
- 只会清理并重写本工具管理的环境变量
- 其他字段和未受管理的环境变量会保留

当前受管理的环境变量包括：

- `ANTHROPIC_BASE_URL`
- `ANTHROPIC_API_KEY`
- `ANTHROPIC_AUTH_TOKEN`
- `ANTHROPIC_MODEL`
- `ANTHROPIC_DEFAULT_HAIKU_MODEL`
- `ANTHROPIC_DEFAULT_SONNET_MODEL`
- `ANTHROPIC_DEFAULT_OPUS_MODEL`
- `API_TIMEOUT_MS`
- `CLAUDE_CODE_AUTO_COMPACT_WINDOW`
- `CLAUDE_CODE_SUBAGENT_MODEL`
- `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`
- `CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK`
- `CLAUDE_CODE_EFFORT_LEVEL`
- `MAX_THINKING_TOKENS`

大多数 provider 的 API Key 会写入 `ANTHROPIC_API_KEY`，包括 `opencode-go` 的 MiniMax 和 DeepSeek 模型。`minimax-cn`、`minimax-global`、`deepseek`、`xiaomimimo-cn`、`ollama`、`ollama-cloud` 和 `kimi-coding` provider 会写入 `ANTHROPIC_AUTH_TOKEN`。工具会在切换时清理另一种旧鉴权字段，避免 Claude Code 出现鉴权冲突提示。

Codex 的 API Key 不写入 TOML；Codex 运行时通过 `[model_providers.ollama-cloud.auth]` 调用 `cs token ollama-cloud --agent codex` 获取已保存的 key。

恢复官方配置：

```bash
cs restore --agent claude
cs restore --agent codex
cs restore --agent opencode
```

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
cs switch opencode-go --api-key your-key --model deepseek-v4-pro
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
$HOME\AppData\Local\Programs\code-switch\bin
```

### 2. 切换前会不会覆盖我原来的 Claude 配置

不会整体覆盖。工具只会更新它自己管理的供应商相关环境变量，并在写入前自动备份已有的 `settings.json`。

### 3. 必须先执行 `set-key` 吗

不是必须。你也可以在 `switch` 时通过 `--api-key` 临时传入。

## 适用场景

- 在不同 Claude 兼容后端之间快速切换
- 为本地 Claude Code 环境维护统一的供应商配置
- 减少手动编辑 `settings.json` 的出错概率
