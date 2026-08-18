# antimage

**Languages:** [English](README.md) · [فارسی](README.fa.md) · [Русский](README.ru.md) · **简体中文** · [العربية](README.ar.md)

自托管的控制平面，用于从单一面板管理一组 VPN/代理节点：多管理员角色、按范围
划分的访问权限、仅追加的审计轨迹，以及通过双向认证的 gRPC 实现的期望状态收敛。

> **状态：SP1 —— 控制平面主干。** 本次发布提供基础部分：认证、授权、审计、节点
> 注册表、mTLS 注册、引导安装、适配器契约、健康状态以及 UI 外壳。随附的唯一
> 适配器是一个**桩（stub）**，用于端到端地验证收敛。真正的协议适配器、订阅用户
> 管理、流量计量和配额明确不在 SP1 范围内 —— 参见[已知限制](#已知限制)。

---

## 目录

[这是什么](#这是什么) · [架构](#架构) · [功能特性](#功能特性) ·
[环境要求](#环境要求) · [支持的系统](#支持的操作系统) ·
[安装](#安装) · [配置](#配置) · [端口](#端口) ·
[TLS 与 mTLS](#tls-与-mtls) · [身份认证](#身份认证) ·
[授权](#授权) · [添加节点](#添加节点) ·
[二进制文件下载](#二进制文件下载) · [安全模型](#安全模型) ·
[CLI](#cli-用法) · [API](#api-用法) · [日志](#日志) ·
[健康检查](#健康检查) · [故障排查](#故障排查) ·
[升级](#升级流程) · [备份](#备份与恢复) ·
[卸载](#卸载) · [开发](#开发环境搭建) · [测试](#测试) ·
[部署](#部署) · [已知限制](#已知限制) · [许可证](#许可证)

---

## 这是什么

antimage 由两个程序和一个 CLI 组成：

- **`antimage-panel`** —— 控制平面。通过 HTTP 提供运维人员 API 和 Web UI，并提供
  一个 gRPC 控制平面，代理通过 mTLS 拨入其中。
- **`antimage-node`** —— 代理。运行在每台被管理的服务器上，自行注册，然后维持一条
  到面板的长连接流，并将主机收敛到面板发布的期望状态。
- **`antimage-ctl`** —— 本地管理与恢复工具，直接与面板的数据库对话。当 UI 无法访问
  或所有管理员都被锁在外面时，这是重新进入系统的途径。

核心的设计选择是**基于代理主动拨出的连接流进行期望状态收敛**，而不是命令式 RPC。
面板发布节点*应当*是什么样子；代理自行决定如何达到该状态，并报告它做了什么。
这意味着节点不需要任何入站端口，离线节点在恢复后会自愈，配置漂移会被检测到，
而不是被无声地覆盖。

## 架构

```
                    ┌──────────────────────────────┐
   operator ──HTTP──►  antimage-panel              │
   (browser/CLI)     │  ├─ HTTP API + embedded SPA │  :8080
                     │  ├─ gRPC control plane      │  :8443  (mTLS)
                     │  ├─ SQLite (WAL)            │
                     │  └─ private CA              │
                     └───────────▲──────────────────┘
                                 │ agent dials out; no inbound port on the node
                     ┌───────────┴──────────────────┐
                     │  antimage-node (agent)       │
                     │  ├─ enrol (one-time token)   │
                     │  ├─ control stream (mTLS)    │
                     │  └─ adapter: observe → plan  │
                     │              → apply → verify│
                     └──────────────────────────────┘
```

**修订版本。** 对节点期望状态的每一次变更都通过唯一的收敛点提交，该收敛点将文档
规范化（RFC 8785 JCS）、用 SHA-256 计算哈希，并将 `desired_revision` 恰好加一。
只有当代理报告已收敛**并且**它所应用的哈希与面板记录的哈希一致时，
`applied_revision` 才会推进。修订版本相符但哈希不符属于完整性故障，绝不是收敛。

**两层授权。** 每个请求都要经过显式的 `rbac.Check` 权限闸门，并且每个受范围限制的
查询都会独立地施加一条 SQL 范围谓词。漏掉其中任何一层都会单独暴露出来，因为两者
是分别测试的。

## 功能特性

- 多管理员，内置四种角色：`super_admin`、`admin`、`reseller`、`readonly`
- 按节点划分的访问范围在 SQL 中强制执行，而不仅仅在处理器中
- 仅追加的审计日志，覆盖特权操作、授权拒绝和校验拒绝
- 不透明的服务端会话（不是 JWT），因此吊销立即生效
- TOTP 双因素认证，配一次性恢复码
- 登录限速与账户锁定
- 使用一次性令牌和私有 CA 的节点注册
- 基于允许列表的吊销：删除节点会立刻将其证书拒之门外
- 期望状态收敛，带漂移检测和逐步骤的应用报告
- 通过 Server-Sent Events 提供实时节点状态
- SSH 引导安装，固定主机密钥，凭据从不持久化
- Web UI 强制支持从右至左书写方向与国际化

## 环境要求

**面板主机**
- Linux x86-64 或 ARM64
- 约 200 MB 磁盘空间用于二进制文件、数据库和审计日志；随集群规模增长
- 无需外部数据库、消息代理或缓存 —— 只用 SQLite

**被管理节点**
- Debian 11/12/13 或 Ubuntu 20.04/22.04/24.04，x86-64 或 ARM64
- `systemd`、`curl`
- 可出站 TCP 连接到面板的 gRPC 端口。**不需要任何入站端口。**

**从源码构建**
- Go 1.26 或更新版本
- Node.js 20+ 和 npm（仅用于构建 Web UI）

## 支持的操作系统

| 组件 | 支持范围 | 验证情况 |
|---|---|---|
| `antimage-node` | Debian 11/12/13、Ubuntu 20.04/22.04/24.04（amd64、arm64） | `install.sh` 按设计拒绝其他任何系统 |
| `antimage-panel` | 相同架构下的任意 Linux | 在 CI 中交叉编译并测试 |
| 构建主机 | Linux、macOS、Windows | 测试套件在三者上都会运行 |

`install.sh` 会刻意**拒绝**不受支持的发行版，而不是去猜测软件包名称。

## 安装

### 克隆并构建

```bash
git clone https://github.com/devprogrmer/antimage.git
cd antimage
```

先构建 Web UI —— 面板会将其嵌入自身：

```bash
cd web && npm ci && npm run build && cd ..
```

然后构建二进制文件：

```bash
make build
```

或者不用 `make`：

```bash
CGO_ENABLED=0 go build -trimpath -o bin/antimage-panel ./cmd/antimage-panel
CGO_ENABLED=0 go build -trimpath -o bin/antimage-node  ./cmd/antimage-node
CGO_ENABLED=0 go build -trimpath -o bin/antimage-ctl   ./cmd/antimage-ctl
```

`CGO_ENABLED=0` 是有意为之：SQLite 驱动是纯 Go 实现的，因此二进制文件是静态的，
在目标机器上不需要 libc。

### 启动面板

```bash
sudo mkdir -p /var/lib/antimage && sudo chmod 700 /var/lib/antimage
sudo ./bin/antimage-panel \
  --data-dir /var/lib/antimage \
  --http :8080 \
  --grpc :8443 \
  --grpc-hosts panel.example.com
```

首次启动时，面板会生成它的主密钥、私有 CA 和数据库。它会打印出你需要在节点上
固定的 CA 指纹：

```
level=INFO msg="antimage-panel listening" http=:8080 grpc=:8443 ca_fingerprint=… grpc_cert_hosts=[panel.example.com]
```

> **`--grpc-hosts` 必须列出代理实际拨号使用的名称。** 这些名称会成为面板 TLS
> 证书的 SAN。一旦不匹配，所有节点的握手会同时失败，而且在代理尝试连接之前
> 这个问题是不可见的。

### 创建第一个管理员

```bash
sudo ./bin/antimage-ctl --data-dir /var/lib/antimage \
  create-admin admin 'a-long-passphrase' super_admin
```

然后打开 `http://localhost:8080` 并登录。

### 将面板安装为服务

```bash
sudo cp bin/antimage-panel /usr/local/bin/
sudo useradd --system --home /var/lib/antimage --shell /usr/sbin/nologin antimage
sudo chown -R antimage:antimage /var/lib/antimage
sudo cp packaging/antimage-panel.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now antimage-panel
```

该单元以 `User=antimage` 运行，并启用 `NoNewPrivileges`、`ProtectSystem=strict`、
`ProtectHome` 和 `PrivateTmp`。**你必须自行创建该用户** —— 打包过程不会替你创建。

## 添加节点

### 一行命令引导安装

在 UI 中创建节点（或用 `antimage-ctl` 创建），取得注册令牌，然后**在节点上**运行：

```bash
curl -fsSL https://panel.example.com/install.sh | sudo bash -s -- \
  --panel https://panel.example.com \
  --token YOUR_ENROLMENT_TOKEN \
  --ca-fingerprint THE_PANEL_CA_FINGERPRINT
```

`install.sh` 会校验操作系统和架构，下载代理及其 SHA-256，**在安装前先验证校验和**，
以 0600 权限写入 `/etc/antimage/node.yaml`，安装 systemd 单元并启动它。重复运行会
就地升级，且不会消耗新的令牌。

通过带外渠道传入 `--ca-fingerprint` 是更强的做法。如果省略它，脚本会通过 HTTPS 从
面板获取该指纹 —— 这属于首次使用即信任，被劫持的 DNS 记录可以攻破它。

> **在这条一行命令能用之前，你必须先发布代理二进制文件。** 参见
> [二进制文件下载](#二进制文件下载)。

### 手动安装

```bash
sudo install -m 0755 antimage-node /usr/local/bin/antimage-node
sudo mkdir -p /etc/antimage /var/lib/antimage && sudo chmod 700 /var/lib/antimage
sudo tee /etc/antimage/node.yaml >/dev/null <<'YAML'
panel_url: https://panel.example.com:8443
token: YOUR_ENROLMENT_TOKEN
ca_fingerprint: THE_PANEL_CA_FINGERPRINT
state_dir: /var/lib/antimage
YAML
sudo chmod 600 /etc/antimage/node.yaml
sudo cp packaging/antimage-node.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now antimage-node
```

### 从面板发起的 SSH 引导安装

`POST /api/v1/nodes/{nodeID}/bootstrap-ssh` 分两个阶段通过 SSH 运行安装程序：第一次
调用返回主机的密钥指纹供人工确认，第二次调用只针对那个已固定的密钥执行。
**SSH 凭据从不持久化** —— 没有对应的表、没有列、没有序列化 —— 并且密钥材料会在请求
返回之前从内存中擦除。

## 配置

### `antimage-panel` 标志

| 标志 | 类型 | 默认值 | 是否必需 | 用途 |
|---|---|---|---|---|
| `--data-dir` | path | `/var/lib/antimage` | 否 | 数据库、主密钥、下载文件。权限必须是 0700。 |
| `--http` | listen addr | `:8080` | 否 | 运维人员 API 和 UI。请在前面放一个 TLS 终止层。 |
| `--grpc` | listen addr | `:8443` | 否 | 代理控制平面。直接提供 mTLS。 |
| `--grpc-hosts` | CSV | `localhost,127.0.0.1` | **实际上是** | 代理拨号使用的 DNS 名称和 IP；会成为证书的 SAN。默认值只适用于本地测试。 |
| `--version` | flag | — | 否 | 打印版本并退出。 |

### `antimage-node` 标志

| 标志 | 类型 | 默认值 | 用途 |
|---|---|---|---|
| `--config` | path | `/etc/antimage/node.yaml` | 代理配置文件。 |
| `--version` | flag | — | 打印版本并退出。 |

### `/etc/antimage/node.yaml`

参见 [`packaging/node.yaml.example`](packaging/node.yaml.example)。

| 键 | 类型 | 是否必需 | 默认值 | 用途 | 安全说明 |
|---|---|---|---|---|---|
| `panel_url` | string | **是** | — | 面板 gRPC 端点，`https://host:port` 或 `host:port`。 | 如果其中带有路径、查询串或非 https 协议，启动时会被拒绝。 |
| `token` | string | 仅首次运行 | — | 一次性注册令牌。 | 用掉之后会从文件中清除。请将文件权限保持在 0600。 |
| `ca_fingerprint` | string | **是** | — | 面板 CA 证书的 SHA-256，十六进制。 | 缺少它时启动会**拒绝**，而不是回退到系统信任存储。 |
| `state_dir` | path | 否 | `/var/lib/antimage` | 节点密钥、证书、被管理的状态。 | 以 0700 创建；密钥和证书为 0600。 |
| `node_id` | int | 否 | — | 注册完成后写入。 | 不要手工设置。 |

### 环境变量

| 变量 | 使用方 | 用途 | 安全说明 |
|---|---|---|---|
| `ANTIMAGE_MASTER_KEY` | panel | Base64 编码的 32 字节主密钥，用于替代密钥文件。 | 用于加密 TOTP 密钥和 CA 私钥。泄露的数据库若没有这把钥匙，两者都拿不到。除非你的平台通过环境注入机密，否则优先使用磁盘上权限为 0600 的文件。 |
| `ANTIMAGE_DEV_PROXY` | panel | 将 UI 请求代理到 Vite 开发服务器。 | **仅限开发用途。** 生产环境中绝不要设置。 |

## 端口

| 端口 | 组件 | 协议 | 暴露对象 |
|---|---|---|---|
| 8080 | panel | HTTP | 运维人员。请在其前面终止 TLS。 |
| 8443 | panel | gRPC over mTLS | 节点。必须能从每个被管理节点访问到。 |
| — | node | 无 | 代理主动向外拨号。**没有入站端口。** |

## TLS 与 mTLS

控制平面全程双向认证。

### 信任模型

面板运行它**自己的私有 CA**，在首次启动时创建，并以主密钥加密存储。它不是公共 Web
PKI 的 CA，只签发：

- 一张面板 gRPC 监听器用的**服务器证书**，SAN 来自 `--grpc-hosts`，有效期 90 天，
  面板每次启动时重新签发；
- 每个节点一张**客户端证书**，`CN = <node id>`，有效期一年。

### 证书位置

| 内容 | 位置 | 权限 |
|---|---|---|
| 主密钥 | `<data-dir>/master.key`（或 `ANTIMAGE_MASTER_KEY`） | 0600 |
| CA 证书 + 密封的私钥 | `<data-dir>/antimage.db` 中的 `panel_ca` 表 | — |
| 面板服务器证书 | 内存中，每次启动重新签发 | — |
| 节点私钥 | `<state-dir>/node.key` | 0600 |
| 节点证书 | `<state-dir>/node.crt` | 0600 |
| 已固定的面板 CA | `<state-dir>/panel-ca.crt` | 0600 |

### 注册流程

1. 代理在本地生成自己的密钥对。**私钥永不离开节点，面板也永远看不到它。**
2. 它拨号连接面板，并验证对方出示的证书链中包含一张 SHA-256 与 `ca_fingerprint`
   相符的证书。被劫持的 DNS 记录一无所获。
3. 它发送一次性令牌和一份 CSR。
4. 面板验证该令牌未被使用、未过期且绑定到该节点，随后签发一张客户端证书，记录其
   指纹，并作废该令牌。
5. 此后的一切都走 mTLS。

面板出示的是 `[leaf, CA]` 而不是单独的叶子证书，正是为了让还没有 CA 文件的注册中
代理能够在证书链里找到它固定的那个指纹。

### 校验与吊销

监听器使用 `VerifyClientCertIfGiven`，而**不是** `RequireAndVerifyClientCert`：注册
必然发生在节点持有任何证书之前。控制服务会按每个 RPC 强制该要求，并且额外将出示的
指纹与 `nodes.cert_fingerprint` 进行比对。

**吊销靠的是允许列表，不是 CRL。** 面板是唯一的校验方，所以删除节点会移除它的指纹，
并在下一次连接时把它挡在外面。没有需要分发的 CRL，也没有需要运行的 OCSP 响应器。

### 有效期与轮换

| 证书 | 有效期 | 轮换方式 |
|---|---|---|
| CA | 10 年 | 手动。更换它会导致整个集群重新注册。 |
| 面板服务器 | 90 天 | 自动 —— 面板每次启动都会重新签发。请至少每 90 天重启一次面板。 |
| 节点客户端 | 1 年 | **尚未自动化 —— 参见[已知限制](#已知限制)。** |

### 验证命令

获取运维人员应当固定的指纹：

```bash
curl -fsS https://panel.example.com/api/v1/ca-fingerprint
```

查看 gRPC 监听器出示了什么：

```bash
openssl s_client -connect panel.example.com:8443 -showcerts </dev/null 2>/dev/null | openssl x509 -noout -text
```

确认某个节点自身的证书：

```bash
sudo openssl x509 -in /var/lib/antimage/node.crt -noout -subject -dates
```

## 身份认证

- 密码用 **argon2id** 哈希（m=64 MB, t=3, p=4）。
- 会话是**服务端的不透明令牌**，不是 JWT，因此吊销立即生效。只存储令牌的 SHA-256。
- Cookie 带有 `HttpOnly`、`Secure`、`SameSite=Strict`。
- **空闲超时 4 小时；绝对生命周期 7 天。** 活动会延长空闲窗口；没有任何东西能延长
  绝对期限。
- 登录失败按账户和按 IP 限速，失败 5 次后锁定。未知用户名花费的时间与已知用户名
  相同，因此响应时间不会泄露某个账户是否存在。
- **TOTP** 对每个管理员是可选的。一旦启用，就必须提供有效的验证码，而且面板无法
  验证的每一条分支都会**拒绝**，而不是仅凭密码放行。
- 确认时会签发十个**一次性恢复码**，且只显示一次。

启用第二因素：

```bash
# returns {"secret":"…","provisioning_uri":"otpauth://…"}
curl -X POST https://panel.example.com/api/v1/auth/totp/enrol -b cookies.txt
# confirm with a code from your authenticator; returns the recovery codes once
curl -X POST https://panel.example.com/api/v1/auth/totp/confirm -b cookies.txt \
  -d '{"totp":"123456"}'
```

## 授权

四种内置角色：

| 角色 | 读取节点 | 写入节点 | 注册 | 写入服务 | 读取审计 | 会话 |
|---|---|---|---|---|---|---|
| `super_admin` | ✅ 全部 | ✅ | ✅ | ✅ | ✅ | ✅ |
| `admin` | ✅ 范围内 | ✅ | ✅ | ✅ | ✅ | 自己的 |
| `reseller` | ✅ 范围内 | — | — | ✅ | — | 自己的 |
| `readonly` | ✅ 范围内 | — | — | — | — | 自己的 |

授权会被独立地执行两次：

1. **权限闸门** —— 每个处理器在动手之前都调用 `rbac.Check`。
2. **SQL 范围谓词** —— 每个受范围限制的查询都按调用者的允许列表过滤，所以即使某个
   处理器忘了做检查，它仍然读不到另一个管理员的节点。

拒绝会连同被尝试的权限、方法和路径一起写入审计日志。

## 二进制文件下载

`install.sh` 会从面板获取代理。把二进制文件放进 `<data-dir>/downloads` 即可发布：

```bash
sudo mkdir -p /var/lib/antimage/downloads
sudo cp antimage-node-linux-amd64 /var/lib/antimage/downloads/
sha256sum antimage-node-linux-amd64 | awk '{print $1}' \
  | sudo tee /var/lib/antimage/downloads/antimage-node-linux-amd64.sha256
sudo chown -R antimage:antimage /var/lib/antimage/downloads
```

只有这四个名称会被提供，而且这份名单是**允许列表，不是净化器**：

- `antimage-node-linux-amd64` 和 `.sha256`
- `antimage-node-linux-arm64` 和 `.sha256`

其他任何请求都返回 404，包括目录中确实存在的文件。该端点按设计不需要认证 ——
二进制文件不是机密，真正授权加入的是注册令牌。

## 安全模型

| 属性 | 做法 |
|---|---|
| 节点密钥 | 在节点上生成；面板永远看不到私钥。 |
| 面板冒充 | 代理固定 CA 指纹；被劫持的 DNS 记录一无所获。 |
| 吊销 | 基于 `cert_fingerprint` 的允许列表；删除节点会立即将其挡在门外。 |
| 静态数据中的机密 | TOTP 密钥和 CA 私钥用 AES-256-GCM 密封，主密钥保存在数据库**之外**。 |
| SSH 凭据 | 从不持久化。没有表、没有列、没有序列化标签；用完后从内存擦除。 |
| 注册令牌 | 一次性使用，30 分钟过期，以哈希形式存储，用后即焚，在审计记录中被脱敏。 |
| 路径穿越 | 下载同时使用允许列表**和** `os.OpenInRoot`，后者即使经由符号链接也拒绝离开该目录。 |
| 审计完整性 | 仅追加；`audit_log` 没有指向 `nodes` 的外键，所以删除节点无法抹掉它自己的记录。 |
| 漂移 | 被管理的文件都有校验和；手工修改会被检测并纠正，而不是被无声覆盖。 |

请按照 [SECURITY.md](SECURITY.md) 报告漏洞。

## CLI 用法

```
antimage-ctl [--data-dir DIR] <command> [arguments]

  create-admin   USERNAME PASSWORD ROLE   create an admin
  reset-password USERNAME PASSWORD        set a new password, revoke their sessions
  list-admins                             list admins with roles and status
  enroll-token   NODE_ID                  print a single-use enrolment token
  backup         DEST.db                  write a consistent database copy
  version                                 print the version
```

`reset-password` 还会清除该账户的登录失败历史，这样导致运维人员被锁定的那些尝试
不会在之后继续把他挡在外面。

## API 用法

所有 API 路径都在 `/api/v1` 之下。认证方式是来自 `POST /auth/login` 的会话 Cookie。

```bash
# sign in
curl -c cookies.txt -X POST https://panel.example.com/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"…","totp":"123456"}'

# create a node, then mint a bootstrap command
curl -b cookies.txt -X POST https://panel.example.com/api/v1/nodes \
  -H 'Content-Type: application/json' \
  -d '{"name":"de-1","address":"203.0.113.10"}'

curl -b cookies.txt -X POST https://panel.example.com/api/v1/nodes/1/enroll-token
```

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/auth/login` `/auth/logout` | 会话生命周期 |
| GET | `/auth/me` | 当前主体及其权限 |
| POST | `/auth/totp/enrol` `/auth/totp/confirm` `/auth/totp/disable` | 第二因素 |
| GET/POST | `/nodes` | 列出和创建节点 |
| GET/DELETE | `/nodes/{id}` | 节点详情；删除会把该节点挡在门外 |
| POST | `/nodes/{id}/enroll-token` | 签发一次性令牌和引导安装命令 |
| POST | `/nodes/{id}/bootstrap-ssh` | 两阶段 SSH 引导安装 |
| GET | `/nodes/{id}/revisions` `/nodes/{id}/apply-runs` | 历史记录与逐步骤的应用结果 |
| POST | `/nodes/{id}/services` | 创建服务（会推进修订版本） |
| PUT/DELETE | `/services/{id}` | 更新或删除服务 |
| GET | `/audit` `/sessions` | 审计轨迹；自己的会话 |
| DELETE | `/sessions/{id}` | 吊销自己的某个会话 |
| GET | `/events` | 实时节点状态（SSE） |
| GET | `/ca-fingerprint` | 公开的信任锚（无需认证） |

错误使用统一的信封：`{"error":{"code":"…","message":"…"}}`。每个响应都带有
`X-Request-ID`，它同样会出现在审计日志中。

## 日志

通过 Go 的 `log/slog` 输出到 stderr 的结构化日志。在 systemd 下：

```bash
sudo journalctl -u antimage-panel -f
sudo journalctl -u antimage-node -f
```

运维事件进入**审计日志**而不是 stdout，可通过 `GET /api/v1/audit` 查询。机密永远
不会被记录：注册令牌在引导安装输出中被脱敏，TOTP 密钥和恢复码从不进入审计记录。

## 健康检查

实时视图是 `GET /api/v1/events`（SSE），它每 3 秒推送一次状态快照，并且
**在每一次心跳时重新校验会话**，因此登出或吊销会迅速终止该流。

节点状态取值：

| 状态 | 含义 |
|---|---|
| `pending` | 已创建，从未联系过 |
| `enrolling` | 令牌已签发，尚未完成注册 |
| `online` | 正在保持流连接并发送心跳 |
| `degraded` | 已连接，最近一次应用失败或只完成了一部分 |
| `integrity` | **该节点应用了一份哈希并非面板签发的文档。** 请调查。 |
| `offline` | 连续三个间隔（90 秒）没有心跳 |
| `disabled` | 被管理员禁用 |

## 故障排查

**面板迁移之后，每个节点的握手都失败。**
`--grpc-hosts` 不再与代理实际拨号的名称匹配。检查启动日志那一行
（`grpc_cert_hosts=[…]`），修正该标志，然后重启。面板每次启动都会重新签发证书。

**运行 install.sh 时出现 `bad interpreter: /bin/bash^M`。**
脚本被以 CRLF 行尾检出了。仓库通过 `.gitattributes` 把 `*.sh` 固定为 LF；请重新
克隆或运行 `dos2unix`。

**已启用 TOTP 的管理员无法登录，日志说 box 缺失。**
面板在存在加密机密的情况下启动却没有主密钥。这是刻意的失败即关闭行为 —— 请恢复
`master.key` 或 `ANTIMAGE_MASTER_KEY`。不要删除该密钥：没有它，TOTP 密钥和 CA
私钥都无法恢复。

**某个节点卡在 `integrity`。**
它所应用的文档哈希出来的值是面板从未签发过的。请检查
`GET /api/v1/nodes/{id}/apply-runs`。这个状态是刻意黏滞的 —— 心跳不会清除它。

**引导安装在下载步骤失败。**
没有发布任何二进制文件。参见[二进制文件下载](#二进制文件下载)。

**备份时出现 `cannot VACUUM from within a transaction`。**
已在本次发布中修复。请升级 `antimage-ctl`。

## 升级流程

```bash
# 1. Back up first — this is consistent and safe while the panel runs.
sudo antimage-ctl --data-dir /var/lib/antimage backup /var/backups/antimage-$(date +%F).db

# 2. Replace the panel binary and restart. Migrations run automatically.
sudo systemctl stop antimage-panel
sudo cp antimage-panel /usr/local/bin/
sudo systemctl start antimage-panel

# 3. Publish the new agent binaries.
sudo cp antimage-node-linux-amd64 /var/lib/antimage/downloads/
sha256sum antimage-node-linux-amd64 | awk '{print $1}' \
  | sudo tee /var/lib/antimage/downloads/antimage-node-linux-amd64.sha256

# 4. Upgrade a node in place — re-running is idempotent and consumes no token.
curl -fsSL https://panel.example.com/install.sh | sudo bash -s -- \
  --panel https://panel.example.com --token '' \
  --ca-fingerprint THE_PANEL_CA_FINGERPRINT
```

数据库迁移只向前进行，并在启动时执行。**回滚要靠恢复备份，而不是降级二进制文件。**

## 备份与恢复

```bash
sudo antimage-ctl --data-dir /var/lib/antimage backup /var/backups/antimage.db
sudo cp /var/lib/antimage/master.key /var/backups/master.key   # 0600, store separately
```

`backup` 使用 SQLite 的 `VACUUM INTO`，在面板持续运行的同时产出一份一致的副本。
它拒绝覆盖已存在的文件。

> **只有数据库是不够的。** 没有 `master.key`，CA 私钥和每一个 TOTP 密钥都无法恢复。
> 请单独备份它 —— 把它和数据库存放在一起，就抵消了让它待在数据库之外的初衷。

恢复：

```bash
sudo systemctl stop antimage-panel
sudo cp /var/backups/antimage.db /var/lib/antimage/antimage.db
sudo cp /var/backups/master.key  /var/lib/antimage/master.key
sudo chown antimage:antimage /var/lib/antimage/*
sudo systemctl start antimage-panel
```

## 卸载

在节点上：

```bash
sudo systemctl disable --now antimage-node
sudo rm -f /etc/systemd/system/antimage-node.service /usr/local/bin/antimage-node
sudo rm -rf /etc/antimage /var/lib/antimage
sudo systemctl daemon-reload
```

也要在面板里删除该节点 —— 这会把它的指纹从允许列表中移除。

在面板主机上：

```bash
sudo systemctl disable --now antimage-panel
sudo rm -f /etc/systemd/system/antimage-panel.service /usr/local/bin/antimage-panel
sudo rm -rf /var/lib/antimage      # destroys the database, CA, and master key
sudo userdel antimage
```

## 开发环境搭建

```bash
git clone https://github.com/devprogrmer/antimage.git && cd antimage
go mod download
cd web && npm ci && cd ..
```

以热重载方式针对一个运行中的面板启动 UI：

```bash
cd web && npm run dev          # terminal 1
ANTIMAGE_DEV_PROXY=http://localhost:5173 go run ./cmd/antimage-panel --data-dir ./tmp   # terminal 2
```

重新生成 protobuf 代码需要 `buf`，并且要安装**到你的 GOPATH 里，而不是装到全系统**：

```bash
go install github.com/bufbuild/buf/cmd/buf@latest
PATH="$PATH:$(go env GOPATH)/bin" make proto
```

## 测试

```bash
make test              # unit + integration, with -race
make e2e               # acceptance suite for the definition of done
make check-imports     # import boundaries and the SSH host-key policy
make check-rtl         # RTL and i18n gates
bash scripts/install_test.sh
cd web && npx vitest run && npm run lint
```

`make test` 使用 `-race`，这需要 cgo。没有 C 工具链时请改用
`go test ./... -count=1`，并注意竞态检测器并未运行。

验收套件会在环回接口上运行一个真实的面板和一个真实的代理，使用货真价实的 mTLS，
覆盖全部六项完成定义条目。它不需要 Docker。

## 部署

在 `:8080` 前面放一个 TLS 终止层；`:8443` 要直接暴露，因为面板自己在那里提供 mTLS，
而终止层会破坏客户端证书校验。

```
                 ┌── :443  → reverse proxy → :8080   (operators, HTTPS)
  panel host ────┤
                 └── :8443 → antimage-panel          (nodes, mTLS, direct)
```

检查清单：创建 `antimage` 用户；`/var/lib/antimage` 权限为 0700；单独备份
`master.key`；把 `--grpc-hosts` 设为公网名称；发布代理二进制文件；至少每 90 天重启
一次，以便重新签发服务器证书。

## 已知限制

以下这些是真实存在的，并且对 SP1 而言是刻意为之：

- **只随附桩适配器。** 它端到端地证明了收敛、漂移检测和上报，但不管理任何真实协议。
  真正的适配器属于后续的子项目。
- **没有订阅用户管理、流量计量、配额或订阅链接。** 不在 SP1 范围内。
- **节点证书自动续期尚未实现。** 证书有效期一年；在中点续期的方案已设计，但尚未
  实现。
- **没有 TOTP 启用界面。** 端点是可用的；SPA 目前还没有对应的界面。
- **没有全局的「super_admin 必须启用 TOTP」设置。** 这是刻意推迟的：一条禁止未启用
  TOTP 的超级管理员登录的策略，可能把运维人员彻底锁在系统外，它首先需要一个经过
  设计的逃生通道。
- **服务端提供的枚举值不会被翻译**（`converged`、`ok`、`restart`）。它们是数据，
  不是 UI 字符串。
- **TOTP 验证码不是一次性的。** 一个验证码在 ±30 秒的时间偏移窗口内一直有效。
- **审计视图没有按范围过滤。** 持有 `audit:read` 的人能看到所有行，包括其他管理员
  的登录 IP。`reseller` 不持有该权限。
- **没有指标端点**（Prometheus 或其他形式都没有）。
- **向下迁移未经测试。** 回滚请靠恢复备份。

## 许可证

**尚未声明任何许可证。** 按照项目规格说明，许可证的选择由维护者决定，并且是任何
公开发布的前提条件；在此之前仓库保持私有。在 `LICENSE` 文件存在之前，不授予任何
使用、复制、修改或分发的许可。

那些影响了 antimage 功能行为的参考项目 —— 3x-ui、Rebecca、PasarGuard、vpn-ui、
openvpn_webpanel_manager、L2tp-Gui-Panel —— **没有提供任何代码、资源、数据库结构或
文档**。这里的每一处实现都是原创的。
