# sing-box 认证与管控功能说明

本文档介绍 sing-box 的 4 个自定义扩展功能：**用户提供者（user-provider）**、**速率限制（speed-limiter）**、**流量配额（traffic-quota）** 以及贯穿三者的 **动态用户管理**。

---

## 目录

1. [功能概述](#功能概述)
2. [编译要求](#编译要求)
3. [数据库准备](#数据库准备)
4. [完整服务端配置](#完整服务端配置)
5. [完整客户端配置](#完整客户端配置)
6. [功能详解](#功能详解)
7. [测试验证](#测试验证)

---

## 功能概述

| 服务类型 | 作用 |
|---------|------|
| `user-provider` | 从 PostgreSQL / Redis / HTTP / 文件 动态拉取用户列表，实时推送到所有 inbound，无需重启 |
| `speed-limiter` | 对已认证用户按姓名限制上传/下载速率（令牌桶算法），支持默认值、分组、按时间段调度 |
| `traffic-quota` | 对已认证用户统计流量消耗，超过每日/每月/自定义周期配额后立即断开所有连接，持久化到 Redis 或 PostgreSQL |

**数据流向：**
```
客户端连接
  → inbound 认证（获取用户名 metadata.User）
  → speed-limiter 包装连接（令牌桶限速）
  → traffic-quota 包装连接（字节统计 + 超额切断）
  → outbound 出站
```

---

## 编译要求

编译时需开启对应 build tag 才能启用 Redis / PostgreSQL 支持：

```bash
# 包含 Redis 和 PostgreSQL 支持的完整构建
make build
# 或手动指定 tags
go build -tags "with_redis,with_postgres,with_quic,with_gvisor,..." ./cmd/sing-box
```

默认构建标签文件位于 `release/DEFAULT_BUILD_TAGS_OTHERS`，已包含 `with_redis,with_postgres`。

---

## 数据库准备

### PostgreSQL 用户表

`user-provider` 从 postgres 读取用户，表结构如下：

```sql
-- 创建用户表
CREATE TABLE IF NOT EXISTS users (
    name       TEXT PRIMARY KEY,   -- 用户名（所有协议通用）
    password   TEXT,               -- 密码（socks/http/trojan/hy2/ss 等使用）
    uuid       TEXT,               -- UUID（vmess/vless/tuic 使用）
    alter_id   INT  DEFAULT 0,     -- VMess alterId（通常为 0）
    flow       TEXT DEFAULT ''     -- VLESS flow（如 xtls-rprx-vision，一般留空）
);

-- 创建自动通知触发器（用户变更时推送 NOTIFY，sing-box 实时感知）
CREATE OR REPLACE FUNCTION notify_user_changes() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('user_changes', 'update');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER user_changes_trigger
AFTER INSERT OR UPDATE OR DELETE ON users
FOR EACH STATEMENT EXECUTE FUNCTION notify_user_changes();

-- 插入示例用户
INSERT INTO users(name, password, uuid) VALUES
    ('alice', 'alice_pass_123', '11111111-1111-1111-1111-111111111111'),
    ('bob',   'bob_pass_456',   '22222222-2222-2222-2222-222222222222');
```

> **提示**：只要执行 `INSERT / UPDATE / DELETE`，触发器自动发送 `LISTEN/NOTIFY`，  
> sing-box 的 user-provider 会在 1 秒内感知变更并推送到所有 inbound，无需重启。

### 运行时动态配额 / 限速字段

如果要在 **不重启 sing-box** 的情况下动态调整 `traffic-quota` / `speed-limiter`，需要给 `users` 表增加下面这些动态字段：

```sql
ALTER TABLE users ADD COLUMN IF NOT EXISTS upload_mbps INT DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS download_mbps INT DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS quota_gb DOUBLE PRECISION DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS quota_period TEXT DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS quota_period_start TEXT DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS quota_period_days INT DEFAULT 0;

CREATE OR REPLACE FUNCTION notify_user_changes() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify(
        'user_changes',
        CASE TG_OP
            WHEN 'DELETE' THEN 'del:' || OLD.name
            ELSE COALESCE(NEW.name, OLD.name)
        END
    );
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS user_changes_trigger ON users;
CREATE TRIGGER user_changes_trigger
AFTER INSERT OR UPDATE OR DELETE ON users
FOR EACH ROW EXECUTE FUNCTION notify_user_changes();
```

也可以直接使用 `cmd/dbsetup` 提供的迁移 / 运维命令：

```bash
pgmgr.exe migrate
pgmgr.exe speed alice 5 10
pgmgr.exe speed-clear alice
pgmgr.exe quota alice 0.04 daily
pgmgr.exe quota-clear alice
```

### Redis 流量配额存储

Redis 存储格式：
```
key:   <key_prefix>:<用户名>:<周期key>
value: <已用字节数（整数）>

# 示例：
tq:sb:alice:2026-04-07  = 5246308   # alice 今日已用 ~5MB
tq:sb:bob:2026-04       = 104857600 # bob 本月已用 100MB
```

---

## 完整服务端配置

```jsonc
// server.json
// 说明：一个服务端实例同时支持 9 种协议，共用同一套用户管理体系
{
  "log": {
    "level": "info"   // 日志级别，debug 可查看每条连接和 user-provider 推送详情
  },

  // ============================================================
  // inbounds：各协议监听入口
  // user-provider 会在启动后自动替换这里的占位用户
  // ============================================================
  "inbounds": [

    // --- SOCKS5 ---
    // 支持用户名/密码认证，metadata.User = username
    {
      "type": "socks",
      "tag":  "socks-in",
      "listen": "0.0.0.0",
      "listen_port": 1080,
      "users": [
        // 占位用户，user-provider 启动后会替换为 postgres 中的真实用户
        {"username": "placeholder", "password": "placeholder"}
      ]
    },

    // --- HTTP 代理 ---
    // 支持 Basic Auth，metadata.User = username
    {
      "type": "http",
      "tag":  "http-in",
      "listen": "0.0.0.0",
      "listen_port": 1081,
      "users": [
        {"username": "placeholder", "password": "placeholder"}
      ]
    },

    // --- Mixed（同时支持 SOCKS5 + HTTP 代理）---
    {
      "type": "mixed",
      "tag":  "mixed-in",
      "listen": "0.0.0.0",
      "listen_port": 1082,
      "users": [
        {"username": "placeholder", "password": "placeholder"}
      ]
    },

    // --- Shadowsocks 多用户 ---
    // 必须使用 users 数组格式（非单 password 格式）才能支持 user-provider
    // metadata.User = users[].name
    {
      "type": "shadowsocks",
      "tag":  "ss-in",
      "listen": "0.0.0.0",
      "listen_port": 8388,
      "method": "aes-128-gcm",
      "users": [
        {"name": "placeholder", "password": "placeholder"}
      ]
    },

    // --- VMess ---
    // metadata.User = users[].name（通过 UUID 匹配用户，返回 name）
    {
      "type": "vmess",
      "tag":  "vmess-in",
      "listen": "0.0.0.0",
      "listen_port": 4433,
      "users": [
        {"name": "placeholder", "uuid": "00000000-0000-0000-0000-000000000000"}
      ]
    },

    // --- VLESS + TLS ---
    {
      "type": "vless",
      "tag":  "vless-in",
      "listen": "0.0.0.0",
      "listen_port": 4434,
      "users": [
        {"name": "placeholder", "uuid": "00000000-0000-0000-0000-000000000000"}
      ],
      "tls": {
        "enabled": true,
        "server_name": "your.domain.com",
        "certificate_path": "/path/to/cert.pem",
        "key_path": "/path/to/key.pem"
      }
    },

    // --- Trojan + TLS ---
    // metadata.User = users[].name（通过 password 匹配）
    {
      "type": "trojan",
      "tag":  "trojan-in",
      "listen": "0.0.0.0",
      "listen_port": 4435,
      "users": [
        {"name": "placeholder", "password": "placeholder"}
      ],
      "tls": {
        "enabled": true,
        "server_name": "your.domain.com",
        "certificate_path": "/path/to/cert.pem",
        "key_path": "/path/to/key.pem"
      }
    },

    // --- Hysteria2 + TLS（UDP）---
    // metadata.User = users[].name（通过 password 匹配）
    {
      "type": "hysteria2",
      "tag":  "hy2-in",
      "listen": "0.0.0.0",
      "listen_port": 4436,
      "users": [
        {"name": "placeholder", "password": "placeholder"}
      ],
      "tls": {
        "enabled": true,
        "server_name": "your.domain.com",
        "certificate_path": "/path/to/cert.pem",
        "key_path": "/path/to/key.pem"
      }
    },

    // --- TUIC v5 + TLS（UDP）---
    // metadata.User = users[].name（通过 UUID + password 匹配）
    // 注意：TUIC 用户必须同时提供 uuid 和 password
    {
      "type": "tuic",
      "tag":  "tuic-in",
      "listen": "0.0.0.0",
      "listen_port": 4437,
      "users": [
        {"name": "placeholder", "uuid": "00000000-0000-0000-0000-000000000000", "password": "placeholder"}
      ],
      "tls": {
        "enabled": true,
        "server_name": "your.domain.com",
        "certificate_path": "/path/to/cert.pem",
        "key_path": "/path/to/key.pem"
      }
    }

  ],

  "outbounds": [
    {"type": "direct"}
  ],

  // ============================================================
  // services：4 个核心功能服务
  // ============================================================
  "services": [

    // ----------------------------------------------------------
    // [1] user-provider：动态用户管理
    // ----------------------------------------------------------
    // 作用：从 PostgreSQL 拉取用户列表，推送到所有指定 inbound。
    //       支持 LISTEN/NOTIFY 实时推送（INSERT/UPDATE/DELETE 立即生效）
    //       以及定时轮询作为兜底。
    // 适用协议：所有支持 ManagedUserServer 接口的 inbound（见上方完整列表）
    // 特别说明：shadowsocks 如需配合 user-provider 使用，
    // 必须显式设置 managed: true
    {
      "type": "user-provider",
      "tag":  "up",

      // 需要管理用户的 inbound 标签列表（必须和上方 inbound tag 一致）
      "inbounds": [
        "socks-in", "http-in", "mixed-in", "ss-in",
        "vmess-in", "vless-in", "trojan-in", "hy2-in", "tuic-in"
      ],

      // PostgreSQL 数据源配置
      "postgres": {
        // 连接字符串，格式：postgres://用户:密码@地址:端口/数据库
        "connection_string": "postgres://postgres:your_password@172.28.254.103:5432/postgres",

        // 用户表名，默认 "users"
        // 表必须包含列：name, password, uuid, alter_id, flow
        "table": "users",

        // LISTEN/NOTIFY 频道名，默认 "user_changes"
        // 当 postgres 触发器执行 pg_notify('user_changes', ...) 时，
        // sing-box 立即重新拉取用户并推送，无需等待 update_interval
        "notify_channel": "user_changes",

        // 轮询间隔（NOTIFY 的兜底保障），默认 1m
        "update_interval": "30s"
      }

      // 也可以使用 Redis 数据源：
      // "redis": {
      //   "address": "172.28.254.103:6379",
      //   "password": "your_redis_password",
      //   "key": "singbox:users",        // 存储 JSON 用户列表的 Redis key
      //   "channel": "singbox:notify",   // 变更通知频道
      //   "update_interval": "30s"
      // }
    },

    // ----------------------------------------------------------
    // [2] speed-limiter：按用户限速
    // ----------------------------------------------------------
    // 作用：对已认证用户的 TCP/UDP 连接进行令牌桶限速。
    //       upload   = 客户端→代理（上传）方向
    //       download = 代理→客户端（下载）方向
    //       限速在 traffic-quota 统计之前应用，不影响字节计数准确性。
    {
      "type": "speed-limiter",
      "tag":  "sl",

      // default：未在 users/groups 中匹配的用户使用此默认值
      // 单位：Mbps（1 Mbps = 125000 bytes/s）
      // 不填或为 0 表示不限速
      "default": {
        "upload_mbps": 1,
        "download_mbps": 1
      },

      // groups：按组设置限速，用户可通过 users[].group 引用组
      "groups": [
        {
          "name": "vip",
          "upload_mbps": 10,
          "download_mbps": 10
        },
        {
          "name": "trial",
          "upload_mbps": 0.5,
          "download_mbps": 0.5
        }
      ],

      // users：针对特定用户的个性化限速（优先级高于 groups 和 default）
      // name 必须与 postgres users 表中的 name 完全一致
      "users": [
        // alice 使用 vip 组的速率（10 Mbps）
        {"name": "alice", "group": "vip"},
        // bob 单独设置为 2 Mbps，忽略 default 和 groups
        {"name": "bob", "upload_mbps": 2, "download_mbps": 2}
      ],

      // schedules：按时间段动态调整限速（可选）
      // 场景：夜间低峰期给所有用户加速，白天限速
      "schedules": [
        {
          // 22:00-08:00 夜间不限速（对 vip 组）
          "time_range": "22:00-08:00",
          "groups": ["vip"],
          "upload_mbps": 0,    // 0 = 本时段不限速
          "download_mbps": 0
        }
      ]
    },

    // ----------------------------------------------------------
    // [3] traffic-quota：流量配额控制
    // ----------------------------------------------------------
    // 作用：统计每个已认证用户的双向流量（upload + download）。
    //       当累计用量超过 quota_gb 后，立即关闭该用户的所有活动连接，
    //       新连接请求也会被立即拒绝，直到下一个计费周期重置。
    //
    // 持久化策略：
    //   - 内存中实时累加（每字节都被统计）
    //   - 每隔 flush_interval 将增量写入 Redis/PostgreSQL
    //   - 服务重启时从 Redis/PostgreSQL 恢复用量（防止重启绕过配额）
    //   - 多实例场景：flush 后从 Redis LoadAll 加载权威值，保证一致性
    {
      "type": "traffic-quota",
      "tag":  "tq",

      // groups：配额分组（用户可通过 group 字段引用）
      "groups": [
        {
          "name": "monthly_10g",
          "quota_gb": 10,        // 每周期限额 10 GB
          "period": "monthly"    // 每月重置：daily / weekly / monthly / custom
        }
      ],

      // users：每个需要配额控制的用户
      // 必须与 postgres users 表中的 name 完全一致
      "users": [
        // alice：每天 5 MB（约等于 0.00488 GiB）
        // quota_gb 单位为 GiB（1 GiB = 1073741824 bytes）
        // 5 MB = 5*1024*1024 / 1073741824 ≈ 0.00488 GiB
        {
          "name": "alice",
          "quota_gb": 0.00488,
          "period": "daily"      // 每天 0 点（UTC）重置
        },
        // bob：引用 monthly_10g 组配置（每月 10GB）
        {
          "name": "bob",
          "group": "monthly_10g"
        },
        // carol：自定义周期，从 2026-01-01 起每 30 天重置一次
        {
          "name": "carol",
          "quota_gb": 50,
          "period": "custom",
          "period_start": "2026-01-01",
          "period_days": 30
        }
      ],

      // persistence：流量数据持久化后端
      // 服务重启后自动从这里恢复用量，防止绕过配额
      "persistence": {
        "redis": {
          "address":    "172.28.254.103:6379",
          "password":   "your_redis_password",
          "db":         0,
          // Redis key 前缀，实际 key 格式：<key_prefix>:<用户名>:<周期key>
          // 例：tq:sb:alice:2026-04-07
          "key_prefix": "tq:sb:"
        }

        // 也可以使用 PostgreSQL 持久化：
        // "postgres": {
        //   "connection_string": "postgres://postgres:password@host:5432/dbname",
        //   "table": "traffic_quota"  // 默认表名，自动创建
        // }
      },

      // flush_interval：将内存中的增量写入持久化存储的间隔
      // 越短越实时，但会增加 Redis/Postgres 写入频率
      // 建议：5s~60s，默认 30s
      "flush_interval": "30s"
    }

  ]
}
```

### 动态更新配置示例

`traffic-quota` 与 `speed-limiter` 均新增了 `dynamic` 配置段，同时可以额外启用一个 `admin-api` 服务作为实时运维入口：

```json
{
  "services": [
    {
      "type": "traffic-quota",
      "tag": "tq",
      "users": [
        {"name": "alice", "quota_gb": 0.01, "period": "daily"}
      ],
      "dynamic": {
        "postgres": {
          "connection_string": "postgres://postgres:password@127.0.0.1:5432/postgres",
          "table": "users",
          "notify_channel": "user_changes"
        },
        "redis": {
          "address": "127.0.0.1:6379",
          "channel": "sing-box:config:updates",
          "hash_key": "sing-box:config:updates:hash"
        }
      }
    },
    {
      "type": "speed-limiter",
      "tag": "sl",
      "users": [
        {"name": "alice", "upload_mbps": 2, "download_mbps": 2}
      ],
      "dynamic": {
        "postgres": {
          "connection_string": "postgres://postgres:password@127.0.0.1:5432/postgres"
        },
        "redis": {
          "address": "127.0.0.1:6379"
        }
      }
    },
    {
      "type": "admin-api",
      "tag": "admin-api",
      "listen": "127.0.0.1:9090",
      "path": "/admin/v1"
    }
  ]
}
```

### 动态更新操作方式

#### PostgreSQL LISTEN / NOTIFY

```sql
UPDATE users
SET quota_gb = 0.04,
    quota_period = 'daily'
WHERE name = 'alice';

UPDATE users
SET upload_mbps = 5,
    download_mbps = 10
WHERE name = 'alice';
```

#### HTTP Admin API

```bash
curl -X PUT http://127.0.0.1:9090/admin/v1/quota/alice \
  -H 'Content-Type: application/json' \
  -d '{"quota_gb":0.04,"period":"daily"}'

curl http://127.0.0.1:9090/admin/v1/quota/alice

curl -X PUT http://127.0.0.1:9090/admin/v1/speed/alice \
  -H 'Content-Type: application/json' \
  -d '{"upload_mbps":10,"download_mbps":10}'

curl -X DELETE http://127.0.0.1:9090/admin/v1/quota/alice
curl -X DELETE http://127.0.0.1:9090/admin/v1/speed/alice
```

#### Redis Pub/Sub

```bash
redis-cli PUBLISH sing-box:config:updates \
  '{"user":"alice","quota_gb":0.04,"period":"daily"}'

redis-cli PUBLISH sing-box:config:updates \
  '{"user":"alice","upload_mbps":5,"download_mbps":10}'

redis-cli PUBLISH sing-box:config:updates \
  '{"user":"alice","delete":true}'
```

---

## 完整客户端配置

```jsonc
// client.json
// 说明：客户端示例，演示如何通过各协议连接到上方服务端
// 实际使用时选择一种 outbound 协议即可
{
  "log": {
    "level": "warn"
  },

  "inbounds": [
    // 本地代理入口，浏览器/应用配置为 SOCKS5 或 HTTP 代理到此端口
    {
      "type": "mixed",       // 同时支持 SOCKS5 和 HTTP 代理
      "tag": "local-proxy",
      "listen": "127.0.0.1",
      "listen_port": 7890    // 本地监听端口
    }
  ],

  "outbounds": [

    // ----------------------------------------------------------
    // 以下选择一种协议连接服务端（对应服务端 inbound）
    // 将所用的 tag 填写到下方 route.rules 的 outbound 字段
    // ----------------------------------------------------------

    // [方案1] SOCKS5（最简单，无加密，内网使用）
    // 用户名/密码 = postgres users 表中的 name/password
    {
      "type": "socks",
      "tag": "out-socks",
      "server": "your.server.ip",
      "server_port": 1080,
      "username": "alice",
      "password": "alice_pass_123"
    },

    // [方案2] HTTP 代理（无加密，内网使用）
    {
      "type": "http",
      "tag": "out-http",
      "server": "your.server.ip",
      "server_port": 1081,
      "username": "alice",
      "password": "alice_pass_123"
    },

    // [方案3] Shadowsocks（加密，推荐 aes-128-gcm 或 chacha20-ietf-poly1305）
    // password = postgres users 表中的 password 字段
    {
      "type": "shadowsocks",
      "tag": "out-ss",
      "server": "your.server.ip",
      "server_port": 8388,
      "method": "aes-128-gcm",
      "password": "alice_pass_123"
    },

    // [方案4] VMess（使用 UUID 认证）
    // uuid = postgres users 表中的 uuid 字段
    {
      "type": "vmess",
      "tag": "out-vmess",
      "server": "your.server.ip",
      "server_port": 4433,
      "uuid": "11111111-1111-1111-1111-111111111111",
      "security": "auto"    // 加密方式：auto / aes-128-gcm / chacha20-poly1305 / none
    },

    // [方案5] VLESS + TLS（推荐生产使用）
    // uuid = postgres users 表中的 uuid 字段
    {
      "type": "vless",
      "tag": "out-vless",
      "server": "your.server.ip",
      "server_port": 4434,
      "uuid": "11111111-1111-1111-1111-111111111111",
      "tls": {
        "enabled": true,
        "server_name": "your.domain.com"
        // 如果服务端证书是自签名，添加：
        // "insecure": true
      }
    },

    // [方案6] Trojan + TLS（伪装为 HTTPS 流量）
    // password = postgres users 表中的 password 字段
    {
      "type": "trojan",
      "tag": "out-trojan",
      "server": "your.server.ip",
      "server_port": 4435,
      "password": "alice_pass_123",
      "tls": {
        "enabled": true,
        "server_name": "your.domain.com"
      }
    },

    // [方案7] Hysteria2（基于 QUIC/UDP，抗丢包）
    // password = postgres users 表中的 password 字段
    {
      "type": "hysteria2",
      "tag": "out-hy2",
      "server": "your.server.ip",
      "server_port": 4436,
      "password": "alice_pass_123",
      "tls": {
        "enabled": true,
        "server_name": "your.domain.com"
      }
    },

    // [方案8] TUIC v5（基于 QUIC/UDP，低延迟）
    // uuid + password 必须同时与 postgres 中的值一致
    {
      "type": "tuic",
      "tag": "out-tuic",
      "server": "your.server.ip",
      "server_port": 4437,
      "uuid": "11111111-1111-1111-1111-111111111111",
      "password": "alice_pass_123",
      "tls": {
        "enabled": true,
        "server_name": "your.domain.com"
      }
    },

    // 直连（不走代理）
    {"type": "direct", "tag": "direct"}
  ],

  "route": {
    "rules": [
      // 中国大陆 IP/域名直连，其余走代理
      {
        "geoip": "cn",
        "outbound": "direct"
      },
      {
        "geosite": "cn",
        "outbound": "direct"
      }
    ],

    // 默认走代理，修改此处选择使用上方哪个 outbound
    // 例：使用 Hysteria2 将此处改为 "out-hy2"
    "final": "out-hy2"
  }
}
```

---

## 功能详解

### 1. user-provider（用户提供者）

**解决的问题**：sing-box 原版需要在配置文件中写死用户列表，新增/删除用户必须重启服务。

**工作原理**：
- 启动时从 PostgreSQL 拉取全量用户，调用各协议 inbound 的 `ReplaceUsers()` 接口原子替换用户列表
- 同时监听 PostgreSQL 的 `LISTEN/NOTIFY`，数据库有任何变更（INSERT/UPDATE/DELETE）立即重新拉取并推送，**延迟通常 < 1 秒**
- `update_interval` 作为兜底轮询，即使 NOTIFY 失效也能在指定间隔内同步

**支持的数据源**（可同时配置多个，结果合并去重）：
- `users`：配置文件内联用户（静态）
- `file`：本地 JSON 文件
- `http`：HTTP 接口
- `redis`：Redis 中的 JSON 列表（支持 Pub/Sub 通知）
- `postgres`：PostgreSQL 表（支持 LISTEN/NOTIFY）

**支持的协议 inbound**：`socks`, `http`, `mixed`, `shadowsocks`, `vmess`, `vless`, `trojan`, `hysteria2`, `tuic`, `shadowtls`, `anytls`, `naive`

**补充说明**：
- `shadowsocks`：当 inbound 被 `user-provider` 引用且未内联 `users` 时，会在启动前自动升级为 managed 多用户模式；不需要手动写 `managed: true`
- `shadowsocks`：如需配合 `user-provider` 使用，必须显式设置 `managed: true`；否则会继续按单用户入站创建，并在 `user-provider` 启动时因不支持 `ManagedUserServer` 而报错
- `shadowsocks`：`2022` 系列方法下，`password` 仍作为主 PSK 使用；legacy AEAD（如 `aes-256-gcm`）一旦进入 user-provider managed 模式，将完全以下发的 per-user 密钥鉴权
- `naive` / `shadowtls`：允许以空 `users` 启动；在 `user-provider` 首次下发用户前，请求会被正常拒绝，不会导致进程启动失败或出现未初始化 panic

---

### 2. speed-limiter（速率限制）

**解决的问题**：对不同用户/套餐实施差异化带宽控制。

**工作原理**：使用 `golang.org/x/time/rate` 令牌桶算法包装连接：
- **upload**（客户端→代理）：控制 `Read()` 速率
- **download**（代理→客户端）：控制 `Write()` 速率
- Burst（突发）= 1 秒数据量，允许短暂突发后恢复到限速值
- 大数据块自动分片，避免超过令牌桶上限

**优先级（高→低）**：`用户独立设置` > `组设置` > `默认值`

**时间调度**：`schedules` 支持跨午夜时间段（如 `23:00-06:00`），每分钟检查一次，进入/退出调度时段自动应用/恢复速率。

---

### 3. traffic-quota（流量配额）

**解决的问题**：按周期限制用户总流量，超额自动封锁，支持多实例部署下的一致性。

**工作原理**：
1. 每条连接被 `QuotaConn` 包装，每次 `Read/Write` 都原子累加计数器
2. 累计值超过 `quota_gb` 时调用 `tripExceeded()`，关闭该用户所有活动连接
3. 新连接到来时检查 `IsExceeded()`，若已超额则返回预关闭的连接（立即断开）
4. 每隔 `flush_interval` 将增量（`pendingDelta`）写入 Redis/PostgreSQL，然后 `LoadAll` 加载全局权威值（支持多实例）
5. 服务重启时调用 `restoreUsage()` 从 Redis 恢复历史用量

**周期类型**：

| 类型 | 说明 | 重置时间 |
|------|------|---------|
| `daily` | 每天 | UTC 0 点 |
| `weekly` | 每周 | 周一 UTC 0 点 |
| `monthly` | 每月 | 每月 1 日 UTC 0 点 |
| `custom` | 自定义天数 | 从 `period_start` 起每 `period_days` 天 |

**Redis key 格式**：
```
<key_prefix>:<用户名>:<周期key>
例：tq:sb:alice:2026-04-07    (daily)
    tq:sb:alice:2026-04       (monthly)
    tq:sb:alice:2026-04-01    (custom, 起始日)
```

---

## 测试验证

### 验证用户动态新增

```bash
# 1. 确认 user_new 尚未被允许
curl -x socks5h://user_new:pass_new@server:1080 http://httpbin.org/ip
# 预期：连接失败

# 2. 在 PostgreSQL 中新增用户（自动触发 NOTIFY）
psql "postgres://postgres:password@server:5432/postgres" \
  -c "INSERT INTO users(name, password, uuid) VALUES('user_new', 'pass_new', 'aaaaaaaa-0000-0000-0000-000000000001')"

# 3. 等待 1-3 秒后测试（NOTIFY 实时推送）
curl -x socks5h://user_new:pass_new@server:1080 http://httpbin.org/ip
# 预期：{"origin": "..."}  成功
```

### 验证限速

```bash
# 下载 5MB 文件，观察速度是否接近 1 Mbps（≈ 125 KB/s）
curl -x socks5h://alice:pass@server:1080 \
  -o /dev/null \
  -w "Speed: %{speed_download} B/s, Time: %{time_total}s\n" \
  http://ipv4.download.thinkbroadband.com/5MB.zip
# 预期：Speed ≈ 125000 B/s，Time ≈ 42s
```

### 验证流量配额

```bash
# 下载超过配额的数据
curl -x socks5h://alice:pass@server:1080 -o /dev/null \
  http://ipv4.download.thinkbroadband.com/5MB.zip

# 等待 flush_interval（默认 30s）后再次请求
sleep 35
curl -x socks5h://alice:pass@server:1080 http://httpbin.org/ip
# 预期：连接被立即关闭（empty reply / connection reset）

# 查看 Redis 中的用量
redis-cli -h server -a password GET "tq:sb:alice:$(date +%Y-%m-%d)"
# 输出：5246308 (字节)
```

### 服务端日志关键字

```
# 用户提供者推送
INFO service/user-provider[up]: pushed 2 users to 9 inbound(s)

# 流量配额超限
ERROR connection: connection upload closed: traffic quota exceeded

# Redis 连接失败（自动降级为内存模式）
WARN service/traffic-quota[tq]: traffic-quota redis unavailable, falling back to memory mode: ...
```
