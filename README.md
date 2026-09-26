# DeepSider2API

把 [DeepSider](https://www.deepsider.ai/) 账号变成 **OpenAI 兼容 API** 的多账号网关，内置 Web 管理面板。

> ⚠️ 本项目为非官方逆向网关，仅供**本人授权账号**的本机 / 私有环境研究学习使用。使用可能违反 DeepSider 服务条款，账号封禁等风险自负。

---

## 特性

- **OpenAI 兼容**：`GET /v1/models`、`POST /v1/chat/completions`（支持流式 SSE 与非流式）。
- **多协议**：`POST /v1/messages`（Anthropic Messages，Claude Code 等）、`POST /v1/responses`（OpenAI Responses），与 chat 共用同一套账号池与调度。
- **多账号池**：多个 DeepSider JWT 轮询、失败冷却熔断、积分/套餐状态刷新。
- **API 密钥分发**：面板签发 `sk-...` 子密钥，可停用 / 删除，仅存 SHA-256。
- **一键登录获取账号**：面板点「登录获取账号」→ 后台弹出**内嵌 WebView2 登录窗口**（独立 profile，不碰你的 Edge 数据）→ 你在官方登录页手动登录（Google / 邮箱）→ 自动拦截 `/user/login`、`/user/google-onetap-login` 响应抓取 `{token, refreshToken, email}` 并入池。登录页可配置。
- **Token 自动刷新**：账号保存 `refreshToken`，可通过 `/user/refreshtoken` 续期。
- **邀请码查询**：账号行「邀请」按钮 → 显示该账号专属邀请码、邀请链接、已邀请人数与奖励积分（`/api/invitation/create` + `/api/invitation/overview`，仅需 JWT）。
- **模型编排**：虚拟模型 `auto` 按昼夜自动切换主模型，并支持递归降级链 `model_fallback`（深度 ≤3、长度 ≤8）。
- **Web 管理面板**：仪表盘、账号池、密钥、模型列表、运行日志、在线改配置（液态玻璃风格，地址 `/panel/`）。
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
go run ./cmd/server -config config.json
```

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
| `login.enabled` | 是否允许面板「登录获取账号」 |
| `login.page` | 交互登录页地址（默认 `https://web.deepsider.online`） |
| `login.timeout_seconds` | 登录等待超时（默认 300 秒） |

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

## License

[MIT](LICENSE)
