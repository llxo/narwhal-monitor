# Narwhal Monitor Bot

轻量高效的 **Narwhal Cloud (独角鲸云)** Telegram 库存监控与补货推送机器人。单静态二进制交付，常驻物理内存仅需 ~20MB (RSS)，零外部运行时依赖。

---

## ✨ 核心特性

- 🐳 **宿主机聚合通知**：同母机多套餐补货/上新时自动合并为单条卡片，告别刷屏；紧凑整洁的无缝排版，内置 NQ/TQ 测速链接与直达下单按钮。
- 📦 **原生可折叠引用**：基于 Telegram `<blockquote expandable>` 紧凑折叠展示同机其他可选套餐与机器简介，排版清晰美观。
- 🎛️ **可视化控制面板 (`/menu`)**：
  - 核心 9 大区即点即切（🇭🇰中国香港、🇯🇵日本、🇺🇸美国、🇸🇬新加坡、🇰🇷韩国、🇹🇼中国台湾、🇲🇴中国澳门、🇩🇪德国、🇬🇧英国）；
  - 30+ 冷门与特色地区二级扁平菜单点选，支持一键全选/全清，自动渲染 200+ 国家国旗 Emoji；
  - 快捷价格上限档位切换（`≤$0.2`、`≤$0.5`、`≤$1`、`≤$3`、`不限`）；
  - 配备【🗑️ 关闭面板】主动销毁按钮及 1 分钟未操作自动滑动清屏。
- 🧹 **两档防刷屏与无痕自毁机制**：
  - **控制台面板**：60 秒滑动自毁，每次按钮点击自动刷新计时，配置完毕可随时一键关闭；
  - **常规命令与回显**：`/filter` 帮助与列表、`/help` 指南、`/sub` 与 `/mute` 回执统一 30 秒后自动清屏；
  - **保留用户原指令**：完整留存操作历史记录，并符合 Telegram 协议规范，消除无效 API 报错。
- 👥 **群聊原生解耦与多 Bot 防抢答**：
  - 群聊文本指令自由响应（支持直接输入命令，亦兼容携带 `@Bot` 用户名），显式指向其他 Bot 时自动静默忽略防止抢答冲突；
  - 智能清洗命令参数末尾附带的 `@Bot` 后缀，确保群聊带参命令无缝执行且不污染过滤规则；
  - 支持 Telegram 匿名管理员（Anonymous Admin）与群身份发言直接配置群监控；
  - 群管理员列表具备本地 2 分钟读写锁内存缓存与容灾机制，杜绝高频点击引发 Telegram 429 频控封禁，实现零延迟即时交互。
- 🔍 **多维过滤与规格智能解析**：
  - 自动提取并注入硬件规格关键词（`64m`、`128m`、`1g`、`1c`、`0.2$` 等）；
  - 支持价格、CPU、内存阈值及正向匹配 (`regex`)、反向排除 (`exclude`)，规则毫秒级热更新无需重启；
  - 轻松捕获 64MB/128MB 极低成本 NAT 玩具小鸡与 CN2/CMI/9929 三网精品线路。
- 👥 **分层权限与游客体验模式 (Guest Mode)**：
  - **公开游客体验**：外部用户私聊发送 `/register` 即可一键加入游客模式，接收平台全量补货推送；
  - **正式白名单会员**：专属独立配置，解锁 39 个地区自选、价格上限与高级正则过滤，享有最高优先级抢占推送通道；
  - **单一超级管理员**：掌控全景控制台、实时库存扫描、服务运行状态、白名单/游客生命周期管理与全员广播。
- 🔒 **严格安全门禁与会话隔离**：
  - 采用声明式分组门禁中间件，私聊与群组严格隔离；
  - 群组内联按钮鉴权防越权、防上下文混淆；
  - 敏感管理指令与用户隐私列表仅限在私聊中执行与查看。
- ⚡ **API 限流防洪与高可用自愈**：
  - 平台 429 自动退避与 `/check` 双重检查瞬时缓存防穿透锁；
  - 推送严格遵循 Telegram 频控（全局 25 msg/s、单 Chat ≥1s 保护）；
  - 遭遇 Telegram 429 Flood Wait 智能全局熔断退避；
  - 死信自愈机制：用户拉黑或注销时自动注销游客或暂停订阅，释放名额。
- 📊 **系统运行状态透视**：
  - `/status` 实时查看服务运行状态，智能读取 Linux `/proc/self/statm` 显示真实物理驻留内存 (RSS) 及 Go 堆分配与协程数、Narwhal 账户余额等。

---

## 🚀 性能指标与并发容量 (Performance & Capacity)

本项目针对抢购级库存监控的高并发、低延迟与高频控要求进行了专门的架构优化：

### 1. 硬件资源开销
- **内存占用**：单进程物理常驻内存 (RSS) 仅 **~19MB**，堆分配极低，零 GC 停顿压力；
- **CPU 开销**：日常轮询与事件处理 CPU 占用率 **< 0.2%**，1核 1GB 云服务器即可长期免维护静默运行。

### 2. Telegram 429 频控免疫与并发分发
- **全局漏桶保护**：发信管道锁死在 **25 msg/s**（严格卡在 Telegram 官方 30 msg/s 的安全红线之内，留足安全缓冲区）；
- **单会话安全锁**：对同一 Chat ID 强制施加 **≥ 1.0s** 的安全冷却间隔；
- **全量会话快速投递**：无论挂接多少订阅端（个人/群组/游客），均可在秒级内完成并发分发，且绝无触发 Telegram 429 风险。

---

## 🛠️ 快速开始

### 1. 编译二进制 (以 Linux amd64 为例)
```bash
# Windows PowerShell
$env:CGO_ENABLED="0"; $env:GOOS="linux"; $env:GOARCH="amd64"; go build -ldflags="-s -w" -o narwhal-bot ./cmd/bot

# Linux / macOS
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o narwhal-bot ./cmd/bot
```

### 2. 配置文件 (`.env`)
参考 `.env.example` 在程序同级目录下创建 `.env`：
```ini
# [必须] Telegram Bot Token (向 @BotFather 申请)
BOT_TOKEN=123456789:ABCdefGhIJKlmNoPQRstuVWXyz

# [必须] Narwhal 平台 API Key (在用户中心 Profile 获取，格式为 rnm_xxxx)
API_KEY=your_api_key_here

# [必须] 超级管理员 Telegram ID (唯一的系统掌控者)
ADMIN_ID=123456789

# [可选] 监控轮询间隔秒数 (默认 15，建议 >= 15)
POLL_INTERVAL=15

# [可选] 数据持久化存储目录 (默认 ./data)
DATA_DIR=./data

# [可选] 启动上线时是否向超级管理员发送就绪通知 (默认 true)
STARTUP_NOTIFY=true
```

### 3. 使用 Systemd 运行（推荐）
```bash
# 复制二进制与服务配置到部署目录 (如 /opt/narwhal-bot)
mkdir -p /opt/narwhal-bot
cd /opt/narwhal-bot
chmod +x narwhal-bot

# 安装服务并设置开机自启
cp narwhal-bot.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now narwhal-bot

# 查看状态与日志
systemctl status narwhal-bot
journalctl -u narwhal-bot -f
```

---

## 🤖 指令速查

### 1. 公共基础指令 (全员可用，无需身份门槛)

| 命令 | 说明 | 示例 |
| :--- | :--- | :--- |
| `/start` | 开启监控向导与欢迎信息（自适应角色呈现） | `/start` |
| `/register` | 登记加入游客体验模式（限私聊，秒级激活全量推送） | `/register` |
| `/guest` | 游客模式控制台（查看个人状态 / 退出：`/guest leave`） | `/guest` |
| `/id` | 查看当前会话的 Chat ID（用于向管理员申请授权） | `/id` |
| `/help` | 查看详细命令语法与使用指南 | `/help` |

### 2. 通知开关与免打扰 (正式白名单与游客通用)

| 命令 | 说明 | 示例 |
| :--- | :--- | :--- |
| `/sub` | 一键快速切换通知推送（开启 / 暂停） | `/sub` (直接 Toggle)，亦支持 `/sub on` / `/sub off` |
| `/mute <时长>` | 开启临时免打扰 | `/mute 1h`、`/mute 30m`、`/mute 0` (解除免打扰) |

### 3. 正式白名单专属指令 (需管理员授权)

> 💡 *注：群聊中使用命令支持直接输入或带 Bot 用户名（如 `/menu` 或 `/menu@narwhal_monitor_bot`），指向其他 Bot 时自动静默防抢答。*

| 命令 | 说明 | 示例 |
| :--- | :--- | :--- |
| `/menu` | 调出可视化交互菜单（切换 39 地区、限价、订阅开关，1分钟滑动自毁） | `/menu` |
| `/filter list` | 查看当前已生效的高级过滤规则（30 秒自毁） | `/filter list` |
| `/filter regex <正则>` | 快速添加正向正则（满足才推） | `/filter regex (?i)cn2\|香港` |
| `/filter exclude <正则>` | 快速添加反向正则（满足则丢弃） | `/filter exclude (?i)nat\|ipv6` |
| `/filter add <参数>` | 添加多维度复合规则 | `/filter add price<=5 region=HK,JP` |
| `/filter del <ID>` | 删除指定过滤规则 | `/filter del r1001` |
| `/filter clear` | 清空所有自定义规则 | `/filter clear` |

### 4. 超级管理员专属指令 (私聊执行)

| 命令 | 说明 | 示例 |
| :--- | :--- | :--- |
| `/admin` | 打开超级管理员控制面板 | `/admin` |
| `/check` | 立即扫描当前在售库存与命中套餐（优先读取内存瞬时缓存） | `/check` |
| `/status` | 查看服务运行状态（运行时长、RSS 内存、协程数、平台余额等） | `/status` |
| `/user add <ID> [备注]` | 授权用户或群组为正式白名单并激活订阅 | `/user add 123456789 张三` |
| `/user upgrade <ID>` | 将游客一键转正为正式白名单 | `/user upgrade 123456789` |
| `/user downgrade <ID>` | 将正式白名单降级为游客模式 | `/user downgrade 123456789` |
| `/user del <ID>` | 移除授权并停用通知推送 | `/user del 123456789` |
| `/user list` | 查看完整授权白名单列表与各会话规则状态 | `/user list` |
| `/user guests` | 查看当前所有游客白名单列表 | `/user guests` |
| `/guest on` / `/guest off` | 开启 / 关闭游客体验注册通道 | `/guest on` |
| `/guest limit <数量>` | 设置游客名额上限 (0为不限制) | `/guest limit 20`、`/guest 20` |
| `/broadcast <内容>` | 向所有活跃订阅会话全员群发维护通知 | `/broadcast 系统维护通知` |

---

## 💡 实用过滤语法示例

| 场景 | 推荐命令 | 说明 |
| :--- | :--- | :--- |
| **低成本特价机** | `/filter regex 64m\|128m` | 监控 64MB 或 128MB 特价玩具机 |
| **三网优质线路** | `/filter regex 优化\|cn2\|9929\|cmi` | 仅推送含精品线路标签的节点 |
| **NAT 端口机** | `/filter regex 双栈\|v4\|端口` | 监控包含 IPv4 端口映射的 NAT 节点 |
| **排除纯 IPv6** | `/filter exclude 实验\|无v4\|纯v6` | 排除无 IPv4 的纯 IPv6 或实验节点 |
| **小内存超低价** | `/filter add ram<=128m price<=0.3` | 内存 ≤128MB 且月付 ≤$0.3 |
| **高配置低价** | `/filter add price<=0.5 ram>=256m` | 月付 ≤$0.5 且内存 ≥256MB |
| **特定热门地区** | `/filter add price<=1 region=HK,JP` | 限定香港或日本且月付 ≤$1 |

---

## 📜 开源协议
[MIT License](LICENSE)
