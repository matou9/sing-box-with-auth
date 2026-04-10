# sing-box-with-auth 配置说明 & Admin API 测试手册

## 配置文件说明

| 文件 | 用途 |
|------|------|
| `server.json` | 服务端完整功能模板（含注释，**不可直接运行**） |
| `server-test.json` | 服务端本地测试配置（可直接运行） |
| `client.json` | 客户端完整功能模板（含注释，**不可直接运行**） |
| `client-test.json` | 客户端本地测试配置（可直接运行） |

## Shadowsocks 与 user-provider 的显式配置要求

当 `shadowsocks` 入站要交给 `user-provider` 管理用户时，必须在对应 inbound 上显式写：

```json
{
  "type": "shadowsocks",
  "tag": "ss-in",
  "managed": true,
  "method": "2022-blake3-aes-128-gcm",
  "password": "SERVER_BASE64_PASSWORD_HERE"
}
```

说明：
- `managed: true` 是必填项；不写时，`shadowsocks` 会按普通单用户入站创建
- 一旦 `services.user-provider.inbounds` 引用了这个 tag，而 inbound 又没有写 `managed: true`，启动时会报 `does not support user-provider`
- `users` 字段在这种模式下不需要内联，由 `user-provider` 在启动后通过 `ReplaceUsers` 下发
- `2022` 系列方法下，`password` 仍是服务端主 PSK；客户端实际连接口令是 `主 PSK:用户 PSK`
- legacy AEAD 方法下，启用 `managed: true` 后，实际鉴权以下发的每用户 `password` 为准；不要再把 inbound 上的 `password` 当作最终用户密码使用

`server.json` 里已经按这个要求补了 `ss-in` 示例，可直接参考。

## 快速启动（本地测试）

```bash
# 服务端（端口 15001 VLESS，9091 Admin API）
./sing-box.exe run -c config/server-test.json

# 客户端（端口 1080 SOCKS5/HTTP，1081 HTTP）
./sing-box.exe run -c config/client-test.json

# Linux
chmod +x sing-box-linux-amd64
./sing-box-linux-amd64 run -c config/server-test.json
```

---

## Admin API 接口测试

**基础变量（按实际配置修改）：**

```bash
BASE="http://127.0.0.1:9091/admin/v1"
TOKEN="test-token"                        # 静态 Bearer Token
H="Authorization: Bearer $TOKEN"
CT="Content-Type: application/json"
PROXY="socks5://127.0.0.1:1080"
```

---

## 一、认证（Auth）

### 1.1 登录获取 JWT Token

```bash
curl -s -X POST "$BASE/auth/login" \
  -H "$CT" \
  -d '{"username":"admin","password":"admin123"}'
```

**响应：**
```json
{"token":"eyJ...","expires_at":"2026-04-09T06:18:29Z"}
```

```bash
# 提取 token 赋值给变量
JWT=$(curl -s -X POST "$BASE/auth/login" \
  -H "$CT" \
  -d '{"username":"admin","password":"admin123"}' \
  | grep -o '"token":"[^"]*"' | cut -d'"' -f4)

# 之后可用 JWT 替代静态 token
H_JWT="Authorization: Bearer $JWT"
```

### 1.2 错误密码 → 401

```bash
curl -s -o /dev/null -w "HTTP %{http_code}" \
  -X POST "$BASE/auth/login" \
  -H "$CT" \
  -d '{"username":"admin","password":"wrong"}'
# HTTP 401
```

### 1.3 无认证头 → 401

```bash
curl -s -o /dev/null -w "HTTP %{http_code}" "$BASE/quota/alice"
# HTTP 401
```

### 1.4 Basic Auth（账号密码直接访问接口）

```bash
curl -s -u admin:admin123 "$BASE/quota/alice"
```

---

## 二、用户管理（User）

> 所有用户接口均为 POST，需要认证。

### 2.1 列出所有用户

```bash
curl -s -X POST -H "$H" -H "$CT" \
  -d '{}' \
  "$BASE/user/list"
```

### 2.2 获取单个用户

```bash
curl -s -X POST -H "$H" -H "$CT" \
  -d '{"name":"alice"}' \
  "$BASE/user/get"
```

### 2.3 创建用户（同时绑定配额和限速）

```bash
curl -s -X POST -H "$H" -H "$CT" \
  -d '{
    "name": "dave",
    "uuid": "dddddddd-dddd-dddd-dddd-dddddddddddd",
    "password": "dave-password",
    "quota_gb": 10,
    "period": "monthly",
    "period_start": "2026-04-01",
    "upload_mbps": 10,
    "download_mbps": 20
  }' \
  "$BASE/user/create" \
  -w "\nHTTP %{http_code}"
# HTTP 204
```

### 2.4 更新用户

```bash
curl -s -X POST -H "$H" -H "$CT" \
  -d '{
    "name": "dave",
    "password": "new-password"
  }' \
  "$BASE/user/update" \
  -w "\nHTTP %{http_code}"
# HTTP 204
```

### 2.5 删除用户（级联清理配额和限速）

```bash
curl -s -X POST -H "$H" -H "$CT" \
  -d '{"name":"dave"}' \
  "$BASE/user/delete" \
  -w "\nHTTP %{http_code}"
# HTTP 204
```

---

## 三、流量配额（Quota）

### 3.1 查看用户配额状态

```bash
curl -s -H "$H" "$BASE/quota/alice"
```

**响应：**
```json
{"usage_bytes":1464009,"quota_bytes":1073741824000,"exceeded":false}
```

### 3.2 设置/更新用户配额

```bash
curl -s -X PUT -H "$H" -H "$CT" \
  -d '{
    "quota_gb": 50,
    "period": "monthly",
    "period_start": "2026-04-01"
  }' \
  "$BASE/quota/bob" \
  -w "\nHTTP %{http_code}"
# HTTP 204
```

**period 可选值：**
- `"monthly"` — 每月重置，`period_start` 为重置日期（格式 `2006-01-02`）
- `"custom"` — 自定义周期天数，需配合 `period_days`

### 3.3 触发超限测试（缩小配额）

```bash
# 设置极小配额（≈10KB）触发 exceeded=true
curl -s -X PUT -H "$H" -H "$CT" \
  -d '{"quota_gb":0.00001,"period":"monthly","period_start":"2026-04-01"}' \
  "$BASE/quota/bob" \
  -w "\nHTTP %{http_code}"

# 查看状态（exceeded=true，新连接将被拒绝）
curl -s -H "$H" "$BASE/quota/bob"
# {"usage_bytes":0,"quota_bytes":10737,"exceeded":true}
```

### 3.4 删除用户配额（恢复无限制）

```bash
curl -s -X DELETE -H "$H" "$BASE/quota/bob" \
  -w "\nHTTP %{http_code}"
# HTTP 204（幂等，不存在也返回 204）
```

---

## 四、速率限制（Speed）

### 4.1 查看用户限速配置

```bash
curl -s -H "$H" "$BASE/speed/charlie"
# {"upload_mbps":50,"download_mbps":100}
```

### 4.2 设置/更新用户限速（立即生效）

```bash
curl -s -X PUT -H "$H" -H "$CT" \
  -d '{"upload_mbps":1,"download_mbps":1}' \
  "$BASE/speed/charlie" \
  -w "\nHTTP %{http_code}"
# HTTP 204
```

> **注：** 限速设为 0 表示不限速。

### 4.3 实测限速效果

```bash
# 限速前（高速）
curl -s -x "$PROXY" -o /dev/null \
  -w "速率: %{speed_download} B/s\n" \
  --max-time 8 http://speedtest.tele2.net/10MB.zip

# 通过 admin API 下调到 1Mbps
curl -s -X PUT -H "$H" -H "$CT" \
  -d '{"upload_mbps":1,"download_mbps":1}' \
  "$BASE/speed/charlie"

# 限速后（约 125KB/s）
curl -s -x "$PROXY" -o /dev/null \
  -w "速率: %{speed_download} B/s\n" \
  --max-time 8 http://speedtest.tele2.net/10MB.zip
```

### 4.4 删除用户限速（回落到组/默认配置）

```bash
curl -s -X DELETE -H "$H" "$BASE/speed/charlie" \
  -w "\nHTTP %{http_code}"
# HTTP 204
```

---

## 五、速率调度（Speed Schedules）

> per-user 调度优先级高于全局调度，设置后覆盖 config 里的 schedules。

### 5.1 查看用户调度

```bash
curl -s -H "$H" "$BASE/speed/charlie/schedules" \
  -w "\nHTTP %{http_code}"
# 未设置时 HTTP 404
```

### 5.2 设置用户调度

```bash
# 多个时间段：夜间不限速，高峰期 2Mbps
curl -s -X PUT -H "$H" -H "$CT" \
  -d '{
    "schedules": [
      {"time_range":"00:00-08:00","upload_mbps":0,"download_mbps":0},
      {"time_range":"18:00-23:59","upload_mbps":2,"download_mbps":2}
    ]
  }' \
  "$BASE/speed/charlie/schedules" \
  -w "\nHTTP %{http_code}"
# HTTP 204
```

**time_range 格式：** `"HH:MM-HH:MM"`，支持跨午夜（如 `"22:00-06:00"`）

### 5.3 查看调度（设置后）

```bash
curl -s -H "$H" "$BASE/speed/charlie/schedules"
# [{"time_range":"00:00-08:00","upload_mbps":0,"download_mbps":0},...]
```

### 5.4 删除用户调度（回落到全局调度）

```bash
curl -s -X DELETE -H "$H" "$BASE/speed/charlie/schedules" \
  -w "\nHTTP %{http_code}"
# HTTP 204
```

---

## 六、常见 HTTP 状态码

| 状态码 | 含义 |
|--------|------|
| `200` | 成功，返回 JSON 数据 |
| `204` | 成功，无返回体（PUT/DELETE/POST 写操作） |
| `400` | 请求参数错误（空 user、JSON 格式错误、校验失败） |
| `401` | 未认证或 token 无效/过期 |
| `404` | 用户不存在或未配置该功能 |
| `405` | HTTP 方法不允许 |
| `503` | 对应服务（quota/speed）未在配置中启用 |
