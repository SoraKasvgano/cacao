# Cacao

Candy Server with WebUI

## Build

Requires Go 1.25 or newer and Node.js/npm for the frontend build.

```bash
# build a binary that runs natively
make

# build multiple platform binaries
make all
```

## Run

```bash
# loglevel=[info] listen=[:80] storage=[.]
cacao

# loglevel=[debug] listen=[127.0.0.1:8080] storage=[/var/lib/cacao]
cacao --loglevel=debug --listen=127.0.0.1:8080 --storage=/var/lib/cacao
```

## 公网访问加固

默认监听地址仍为 `:80`（存储目录存在证书与私钥时为 `:443`）。已有账号、网络密码、客户端报文格式及子网转发保持兼容；不需要重新注册或修改网络密钥。

- 登录、首次初始化和修改密码按来源 IP 与账号累计认证失败，达到每分钟 30 次后暂时返回 HTTP 429，带 `Retry-After`。登录/改密最多并行执行 8 次密码校验，超出时提示稍后重试，避免并发爆破耗尽 CPU。成功认证不累计失败；普通业务 API 和已建立的隧道不受该限速影响。
- Candy WebSocket 每 IP 每分钟最多容许 60 次失败认证；新连接需在 15 秒内完成认证，报文最大 64 KiB。正常转发与成功重连不累计失败次数。客户端与服务器应保持时间同步，签名时间允许前后 5 分钟偏差。
- 所有私有 API 在服务端校验登录与角色，并校验网络、设备、路由的归属。登录页和注册页可以公开访问，不会因此授予业务权限。
- Web 会话在服务端 24 小时后失效。升级时已有会话获得一次 24 小时宽限期，无需立即退出；旧密码在下一次成功登录时自动迁移为加盐的 bcrypt 哈希。退出、修改密码、管理员重置密码都会撤销对应旧会话。
- Cookie 使用 HttpOnly、SameSite=Lax；直接 HTTPS 访问自动加 Secure。API 响应不缓存，并拒绝跨站请求、非 JSON 的非空请求体及超过 1 MiB 的请求。

### 反向代理部署

**使用反向代理时必须显式设置 `--trusted-proxies` 为实际代理的 IP/CIDR**，并让代理覆盖客户端传来的转发头。默认不信任 `X-Forwarded-For` / `X-Real-IP`，防止攻击者伪造地址绕过限速。未配置时，同一代理后的用户共享来源 IP 的失败额度；不要将全部公网地址设为可信代理。

例如，同机 Nginx 终止 HTTPS 时：

```bash
cacao --listen=127.0.0.1:8080 --storage=/var/lib/cacao --trusted-proxies=127.0.0.1,::1 --secure-cookies=true
```

在已有 HTTPS `server` 中保留原域名和客户端路径，并配置：

```nginx
# Place this map in the http context.
map $http_upgrade $cacao_connection {
    default upgrade;
    ''      close;
}

# Place this location in the existing HTTPS server context.
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $http_host;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $cacao_connection;
    proxy_read_timeout 75s;
}
```

`--secure-cookies=true` 仅适用于浏览器确实通过 HTTPS 访问的部署，否则浏览器不会通过 HTTP 发送登录 Cookie。公网应使用 HTTPS/WSS，并在代理或防火墙限制后端端口直连。程序不自动改变已有 HTTP/WS 客户端地址；切换 TLS 前先确认现有客户端配置。时间窗口内的签名重放仍依赖 WSS 防止报文被截获，彻底防重放需要升级客户端协议。

### 首次安装

只有空数据库首次创建管理员时需要初始化密钥；已有安装不需要设置。启动前设置至少 32 字符的随机密钥，例如：

```bash
export CACAO_SETUP_TOKEN="$(openssl rand -hex 32)"
cacao --listen=127.0.0.1:8080 --storage=/var/lib/cacao
```

通过本机访问或配置好 HTTPS 后，在注册页填写此密钥以创建首个管理员。初始化后可从环境中移除该密钥。普通注册默认关闭，已有管理员设置的开放注册状态会保留。

升级前备份存储目录，并在使用相同代理与客户端的测试环境验证。身份认证限速在单进程内生效，重启后计数清空；多实例或大规模流量攻击需同时在入口代理实施防护。开发服务器默认只监听本机，不应作为公网生产入口。

安全回归检查：`go test ./...`、`go vet ./...`；前端在 `frontend` 下运行 `npm test`、`npm run build` 和 `npm audit --omit=dev`。
