# 模型映射与快速切换设计

日期：2026-07-04

## 目标

新增两层模型能力：

1. **Provider 默认模型快速切换**：为 provider 持久保存默认 model，影响 `switch`、`run` 和 proxy route 默认模型。
2. **Proxy 模型映射**：把客户端请求中的模型名映射为上游 provider 的真实模型，支持 `default` fallback。

## 配置

`AppConfig` 新增：

```go
ModelMappings map[string]map[string]string `json:"modelMappings,omitempty"`
```

语义：

```json
{
  "modelMappings": {
    "zhipu-cn": {
      "default": "glm-5.2",
      "sonnet": "glm-5.2",
      "gpt-5": "glm-5.2"
    }
  }
}
```

## CLI

新增：

```bash
cs model get <provider>
cs model set <provider> <model>
cs model list <provider>

cs model-map set <provider> <client-model> <upstream-model>
cs model-map get <provider> [client-model]
cs model-map list <provider>
cs model-map remove <provider> <client-model>

cs use-model <provider> <model>
```

`cs use-model <provider> <model>` 等价于：

```bash
cs model set <provider> <model>
cs model-map set <provider> default <model>
```

## Proxy 映射规则

在 `newProxyHandler` 中解析 client request 得到 IR 后，设置上游模型时按以下顺序：

1. `cfg.ModelMappings[provider][incomingModel]`
2. `cfg.ModelMappings[provider]["default"]`
3. `route.Model`

如果映射命中，覆盖 `ir.Model`。如果没有映射，沿用现有 `route.Model` 行为。

## MVP 不做

- TUI 入口
- per-agent 独立映射
- profile
- 自动测速或健康检查选模型
- 多级 provider fallback

## 验证

- CLI 配置读写测试
- proxy 映射测试：incoming `sonnet` -> upstream `glm-5.2`
- `default` fallback 测试
- `go vet ./... && go test ./... && go build -o cs .`
