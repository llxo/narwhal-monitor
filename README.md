# Narwhal Monitor Bot

轻量高效的 **Narwhal Cloud (独角鲸云)** Telegram 库存监控与补货推送机器人。单静态二进制交付，常驻内存仅需 ~10MB，零外部运行时依赖。

---

## ✨ 核心特性

- 🐳 **宿主机聚合通知**：同母机多套餐补货/上新时自动合并为单条卡片，告别刷屏；内置 NQ/TQ 测速链接与直达下单按钮。
- 📦 **原生可折叠引用**：基于 Telegram `<blockquote expandable>` 紧凑折叠展示同机其他可选套餐与机器简介。
- 🎛️ **可视化控制面板 (`/menu`)**：
  - 核心 9 大区即点即切（🇭🇰中国香港、🇯🇵日本、🇺🇸美国、🇸🇬新加坡、🇰🇷韩国、🇹🇼中国台湾、🇲🇴中国澳门、🇩🇪德国、🇬🇧英国）；
  - 30+ 冷门与稀缺地区二级菜单点选，支持一键全选/全清，自动渲染 200+ 国家国旗 Emoji；
  - 快捷价格上限档位切换（`≤$0.2`、`≤$0.5`、`≤$1`、`≤$3`、`不限`）。
- 🔍 **多维正则过滤引擎**：支持价格、CPU、内存阈值及正向匹配 (`regex`)、反向排除 (`exclude`)，规则毫秒级热更新无需重启。
- 🛡️ **严格白名单与隐私保护**：采用单一超级管理员 (`ADMIN_ID`) 白名单授权制，敏感管理指令仅限私聊；会话配置相互隔离。
- ⚡ **API 限流与防洪保护**：内置平台 429 自动退避与 `/check` 防穿透锁；发信严格遵循 Telegram 频控（全局 25 msg/s、单 Chat ≥1s 保护）。

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

# [必须] Narwhal 平台 API Key (用户中心 Profile 生成，格式为 rnm_xxxx)
API_KEY=your_api_key_here

# [必须] 超级管理员 Telegram ID (唯一的系统掌控者)
ADMIN_ID=123456789

# [可选] 轮询间隔秒数 (默认 15，建议 >= 15)
POLL_INTERVAL=15

# [可选] 数据持久化目录 (默认 ./data)
DATA_DIR=./data

# [可选] 启动上线时是否发送问候通知 (默认 true)
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

### 用户与群组指令 (需白名单授权)

| 命令 | 说明 | 示例 |
| :--- | :--- | :--- |
| `/menu` | 调出可视化交互菜单（切换地区、限价、订阅开关） | `/menu` |
| `/check` | 立即查询当前在售库存与规则命中情况（优先读取瞬时缓存） | `/check` |
| `/filter list` | 查看当前已生效的高级过滤规则 | `/filter list` |
| `/filter regex <正则>` | 快速添加正向正则（满足才推） | `/filter regex (?i)cn2\|香港` |
| `/filter exclude <正则>` | 快速添加反向正则（满足则丢弃） | `/filter exclude (?i)nat\|ipv6` |
| `/filter add <参数>` | 添加多维度复合规则 | `/filter add price<=5 region=HK,JP` |
| `/filter del <ID>` | 删除指定过滤规则 | `/filter del r1001` |
| `/filter clear` | 清空所有自定义规则 | `/filter clear` |
| `/sub on` / `/sub off` | 开启 / 暂停接收推送 | `/sub on` |
| `/mute <时长>` | 开启临时免打扰 | `/mute 1h`、`/mute 0` (解除) |
| `/id` | 查看当前会话的 Chat ID（用于向管理员申请授权） | `/id` |
| `/status` | 查看服务运行状态与鉴权健康度 | `/status` |
| `/help` | 查看详细语法与使用指南 | `/help` |

### 超级管理员专属指令 (私聊执行)

| 命令 | 说明 | 示例 |
| :--- | :--- | :--- |
| `/admin` | 打开管理员控制台 | `/admin` |
| `/user add <ID> [备注]` | 授权用户或群组并激活订阅 | `/user add 123456789 张三` |
| `/user del <ID>` | 移除授权并停用通知推送 | `/user del 123456789` |
| `/user list` | 查看完整授权白名单与各会话规则状态 | `/user list` |
| `/stats` | 查看服务大盘统计（内存占用、协程数、平台余额等） | `/stats` |
| `/broadcast <内容>` | 向所有白名单订阅会话群发广播 | `/broadcast 系统维护通知` |

---

## 📜 开源协议
[MIT License](LICENSE)
