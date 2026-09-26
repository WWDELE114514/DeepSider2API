# DeepSider2API

把 [DeepSider](https://www.deepsider.ai/) 账号变成 **OpenAI 兼容 API** 的多账号网关，内置 Web 管理面板。

> ⚠️ 本项目为非官方逆向网关，仅供**本人授权账号**的本机 / 私有环境研究学习使用。使用可能违反 DeepSider 服务条款，账号封禁等风险自负。

---

## 特性

- **OpenAI 兼容**：`GET /v1/models`、`POST /v1/chat/completions`（支持流式 SSE 与非流式）。
- **多协议**：`POST /v1/messages`（Anthropic Messages，Claude Code 等）、`POST /v1/responses`（OpenAI Responses），与 chat 共用同一套账号池与调度。
- **图片生成**：`POST /v1/images/generations`（OpenAI 兼容），复用 conversation 链路，从 202 帧的 markdown 里抠出图片 URL；`size` 自动映射 `imageOptions.resolution/ratio`。
- **图片转存 + 下载链接**：默认把图片下载到本地（`data/images/`）并返回网关永久链接（`url` + `download_url`），规避 DeepSider 24h 失效；可用 `image.persist=false` 关闭。
- **多账号池**：多个 DeepSider JWT 轮询、失败冷却熔断、积分/套餐状态刷新。
- **API 密钥分发**：面板签发 `sk-...` 子密钥，可停用 / 删除，仅存 SHA-256。
- **一键登录获取账号**：面板点「登录获取账号」→ 后台弹出**内嵌 WebView2 登录窗口**（独立 profile，不碰你的 Edge 数据）→ 你在官方登录页手动登录（Google / 邮箱）→ 自动拦截 `/user/login`、`/user/google-onetap-login` 响应抓取 `{token, refreshToken, email}` 并入池。登录页可配置。
- **Token 自动刷新**：账号保存 `refreshToken`，可通过 `/user/refreshtoken` 续期。
- **邀请码查询**：账号行「邀请」按钮 → 显示该账号专属邀请码、邀请链接、已邀请人数与奖励积分（`/api/invitation/create` + `/api/invitation/overview`，仅需 JWT）。
- **模型编排**：虚拟模型 `auto` 按昼夜自动切换主模型，并支持递归降级链 `model_fallback`（深度 ≤3、长度 ≤8）。
- **对话测试页**：面板内置「对话」，可选文本模型（流式）或图片模型（直接出图）快速验证，无需外部客户端。
- **详细日志**：控制台与面板「日志」页都会输出每次调用：`key=<密钥> #<该密钥累计次数> model=<模型> in=<输入> out=<输出>`，覆盖 chat / messages / responses / images。
- **Web 管理面板**：仪表盘、账号池、对话、密钥、模型列表、运行日志、在线改配置（白色液态玻璃风格，地址 `/panel/`）。
- **MCP server**：附送 `deepsider2api-mcp.exe`（stdio），让 AI 客户端直接查账号/总积分/邀请码/模型，并选择账号+模型生成图片。
- **签名复用**：直接复用 DeepSider 扩展的 `sign_wasm`（wasm-bindgen + wazero），生成 `i-sign`，无需逆向哈希算法。
- **零配置构建**：GitHub Actions 自动构建 Docker 镜像与 Windows 单文件。

---

## 快速开始（Docker）

```bash
git clone https://github.com/WWDELE114514/DeepSider2API.git
cd DeepSider2API

mkdir -p config data auths
cp config.example.json config/config.json

# 编辑 config/config.json，至少把 api_key 改成你自己的管理密钥
docker compose up -d
```

浏览器打开 `http://<你的机器IP>:7863/panel/`，用 `api_key` 登录面板，点「登录获取账号」即可弹出内嵌登录窗口完成登录。

> 「登录获取账号」基于 WebView2（`webview_go`），**仅 Windows 构建可用**，需要系统有 WebView2 运行时（Win10/11 自带 Edge 即具备）。Docker/Linux 构建不含该功能（其余功能正常）。登录页在 `config.json` 的 `login.page` 配置，默认 `https://web.deepsider.online`。

### 获取 DeepSider token

1. 登录 [DeepSider](https://www.deepsider.ai/)，F12 打开开发者工具。
2. Network 里随便找一个 `api*.deepsider.me` 请求，复制请求头 `Authorization: Bearer <token>` 中 `<token>` 部分。
3. 粘贴到面板「账号池」。

---

## 本地运行

需要 Go 1.22+：

```bash
cp config.example.json config.json
go build -o deepsider2api ./cmd/server
./deepsider2api -config config.json
```

> `config.json` 的相对路径是**相对可执行文件所在目录**解析的（不是工作目录），所以配置和数据不会因启动方式 / 工作目录不同而"丢失"。
> 用 `go run` 时请传绝对路径（否则会落到临时构建目录），例如 `go run ./cmd/server -config "$PWD/config.json"`。

Windows 单文件：见 GitHub Actions 的 `Build Windows Package` 产物。

---

## 调用

```bash
curl http://localhost:7863/v1/chat/completions \
  -H "Authorization: Bearer <你的密钥>" \
  -H "Content-Type: application/json" \
  -d '{"model":"auto","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

`model` 填 DeepSider 的 `botId`（可在面板「模型」页查看），或使用虚拟模型 `auto`。

### Anthropic Messages（Claude Code 等）

```bash
curl http://localhost:7863/v1/messages \
  -H "Authorization: Bearer <你的密钥>" \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -d '{"model":"auto","max_tokens":1024,"messages":[{"role":"user","content":"你好"}]}'
```

### OpenAI Responses

```bash
curl http://localhost:7863/v1/responses \
  -H "Authorization: Bearer <你的密钥>" \
  -H "Content-Type: application/json" \
  -d '{"model":"auto","input":"你好"}'
```

### 图片生成

```bash
curl http://localhost:7863/v1/images/generations \
  -H "Authorization: Bearer <你的密钥>" \
  -H "Content-Type: application/json" \
  -d '{"model":"openai/gpt-image-2","prompt":"一只戴帽子的橘猫","size":"1024x1024"}'
```

`model` 填图片类 `botId`（`isDrawing:true`，可在面板「模型」页查看）；不填或填非 botId 时用 `image.default_model`。`size` 支持 `1024x1024` / `1792x1024` / `1024x1792` 等，也可直接传 `resolution`(`1k`/`2k`/`4k`) 和 `ratio`(`1:1`/`16:9`/`9:16`…)。

---

## 配置说明（config.json）

| 字段 | 说明 |
| :--- | :--- |
| `listen` | 监听地址，默认 `:7863` |
| `api_key` | 管理密钥（面板登录 + 管理员鉴权），**务必修改** |
| `data_dir` / `auth_dir` | 数据目录（账号池、密钥、日志） |
| `upstream.base_urls` | DeepSider API 主机列表，自动轮换 |
| `upstream.version` / `lang` | 请求头 `i-version` / `i-lang` |
| `pool.breaker_threshold` | 连续失败多少次后冷却账号 |
| `pool.breaker_cooldown` | 冷却时长，如 `30m` |
| `auto_model.enabled` | 是否启用 `auto` 虚拟模型昼夜切换 |
| `auto_model.fallback` | 降级链（模型名数组） |
| `image.default_model` | 图片接口未指定 botId 时用的默认图片模型（默认 `openai/gpt-image-2`） |
| `image.persist` | 是否把生成图片转存到本地并返回网关链接（默认 `true`） |
| `image.persist_ttl_hours` | 转存图片保留小时数（默认 72） |
| `public_base_url` | 返回的图片链接使用的外部地址（反代/公网部署时填，留空按请求 Host 推断） |
| `login.enabled` | 是否允许面板「登录获取账号」 |
| `login.page` | 交互登录页地址（默认 `https://web.deepsider.online`） |
| `login.timeout_seconds` | 登录等待超时（默认 300 秒） |

---

## 管理面板

访问 `http://<host>:7863/panel/`，用 `api_key` 登录。左侧页面：

| 页面 | 作用 |
| :--- | :--- |
| 仪表盘 | 总请求 / 成功 / 失败、按模型统计、运行时长 |
| 账号池 | 添加 / 启停 / 删除账号、刷新积分、查看邀请码、一键登录获取账号 |
| 对话 | 选模型直接对话（文本流式 / 图片出图），用于测试 |
| API 密钥 | 签发 / 停用 / 删除 `sk-...` 子密钥（仅存 SHA-256） |
| 模型 | 浏览 DeepSider 全量模型（botId / 类型 / 积分） |
| 日志 | 查看 / 清空运行日志（与控制台一致） |
| 配置 | 在线修改监听地址、管理密钥、auto 编排与降级链 |

## 端点一览

| 端点 | 说明 |
| :--- | :--- |
| `GET /v1/models` | 模型列表（OpenAI 格式） |
| `POST /v1/chat/completions` | 对话（流式 / 非流式） |
| `POST /v1/messages` | Anthropic Messages（Claude Code 等） |
| `POST /v1/responses` | OpenAI Responses |
| `POST /v1/images/generations` | 图片生成 |
| `GET /files/{name}` | 转存图片（`?download=1` 触发下载） |
| `GET /healthz` | 健康检查 |
| `GET /panel/` | 管理面板 |

鉴权：请求头 `Authorization: Bearer <api_key 或 sk- 子密钥>`。

---

## MCP（让 AI 直接用）

`deepsider2api-mcp.exe` 是一个 **MCP stdio server**，让支持 MCP 的 AI 客户端直接查询账号 / 积分 / 邀请码 / 模型并生成图片。它通过 HTTP 调用正在运行的网关。

配置（环境变量，均可选）：
- `DEEPSIDER_BASE_URL`：网关地址，默认 `http://127.0.0.1:7863`
- `DEEPSIDER_API_KEY`：管理密钥，默认读取**同目录 `config.json`** 的 `api_key`

提供的工具：

| 工具 | 作用 |
| :--- | :--- |
| `list_accounts` | 各账号邮箱、剩余积分、套餐、启用状态、失败次数 |
| `total_credits` | 启用账号的剩余积分总和 |
| `list_models` | 模型列表（`type` 可按 `chat`/`image`/`video` 过滤） |
| `get_invitation` | 指定账号的邀请码 / 链接 / 邀请统计 |
| `generate_image` | 生成图片（可指定 `account` 与 `model`），返回图片 URL 与下载链接 |
| `chat` | 文本对话（非流式） |

客户端配置示例（opencode / Claude Desktop 等）：

```json
{
  "mcp": {
    "deepsider": {
      "type": "local",
      "command": ["E:\\DeepSider2api\\run\\deepsider2api-mcp.exe"],
      "environment": {
        "DEEPSIDER_BASE_URL": "http://127.0.0.1:7863",
        "DEEPSIDER_API_KEY": "change_me"
      },
      "enabled": true
    }
  }
}
```

> 需先启动网关（`deepsider2api.exe`），且把 `deepsider2api-mcp.exe` 放在与 `config.json` 相同的目录（或显式设置环境变量）。

---

## 工作原理

```
客户端(OpenAI 协议) → 本网关
  ├─ 选账号(JWT)
  ├─ 组装 DeepSider body
  ├─ 用 sign_wasm 生成 i-sign = base64({nonce,timestamp,sign})
  ├─ POST /api/v2/chat/conversation
  └─ 解析 SSE(201/202) → 转回 OpenAI SSE
```

签名输入为 `origin + pathname + "?" + qs.stringify({...query, ...body, timestamp, nonce}, {arrayFormat:"indices", allowDots:true, sort})`，由内嵌 wasm 计算，完全复刻官方扩展行为。

---

## 致谢 / License

- **模型编排**（虚拟模型 `auto` 的昼夜切换 + 递归 `model_fallback` 降级链）以及部分网关 / 面板结构，参考了 [JACKY199503/workbuddy2api-panel-plus](https://github.com/JACKY199503/workbuddy2api-panel-plus)（及其上游 [linguo2625469/workbuddy2api-panel](https://github.com/linguo2625469/workbuddy2api-panel)、[Sliverkiss/workbuddy2api](https://github.com/Sliverkiss/workbuddy2api)）。这些项目均为 **MIT License**，其版权与许可声明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
- `i-sign` 复用 DeepSider 的 `sign_wasm`；DeepSider 及其资产归其所有者，本仓库不授予任何 DeepSider 服务权利。
- 本项目自身：[MIT](LICENSE)。
