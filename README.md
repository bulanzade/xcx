# xcx — TUI SSH 连接管理工具

一个终端(TUI)SSH 连接管理器,带内嵌交互式终端和双面板 SFTP 文件管理器。主机配置以 AES-256-GCM 加密保存,用主密码解锁。

## 特性(MVP)

- 🔐 **加密配置** — 主机信息存于 `vault.bin`,AES-256-GCM 加密,主密码经 Argon2id 派生密钥
- 🌲 **分组式主机树** — 折叠/展开分组,快速检索连接
- 🖥️ **内嵌交互式终端** — 在 TUI 内直接打开 SSH shell(自研 xterm 状态机),`Ctrl+\` 退出
- 📁 **双面板 SFTP** — Midnight Commander 风格,本地↔远程,多选批量传输
- 📊 **传输队列** — 后台顺序执行,状态栏实时显示进度/队列深度
- 🔑 **主机指纹校验** — 首次连接交互式询问信任,记录到 `known_hosts`
- 认证支持:**密码** 与 **SSH 密钥(含 passphrase)**

## 构建 & 运行

```bash
# 构建(需 Go 1.21+)
go build -o xcx .

# 运行(进入全屏 TUI)
./xcx
```

首次运行会让你设置主密码(创建加密 vault);之后每次启动输入主密码解锁。
配置目录:`~/.config/xcx/`(Windows 为 `%AppData%\xcx\`),包含 `vault.bin` 与 `known_hosts`。

## 测试

```bash
go test ./...              # 全部
go test ./internal/vault/  # 单个包
go test ./... -count=1     # 跳过缓存
go test -v ./... | grep -c PASS
```

## 快捷键

### 主机树
| 键 | 动作 |
|---|---|
| `↑`/`↓` 或 `k`/`j` | 移动选择 |
| `Enter` | 连接(主机)/ 折叠(分组) |
| `Space` | 折叠/展开分组 |
| `s` | 打开 SFTP |
| `e` | 编辑主机 |
| `n` | 新建主机(当前分组下) |
| `N` | 新建分组 |
| `x` | 删除 |

### 终端
| 键 | 动作 |
|---|---|
| (任意键) | 发送到远端 PTY |
| `Ctrl+\` | 退出终端,回到主机树 |

### SFTP 双面板
| 键 | 动作 |
|---|---|
| `Tab` | 切换左右面板焦点 |
| `Enter` | 进入目录 |
| `Backspace`/`h` | 返回上级 |
| `Space` | 多选当前文件 |
| `F5` | 复制到对侧(下载/上传) |
| `F7` | 新建目录 |
| `F8`/`Del` | 删除 |
| `r` | 刷新 |
| `Esc` | 返回主机树 |

### 全局
| 键 | 动作 |
|---|---|
| `Ctrl+Q` / `Ctrl+C` | 退出 |

## 架构

```
Bubble Tea (alt-screen)
  ├─ Host Tree View        (internal/ui/hosttree.go)
  ├─ Terminal View         (internal/ui/terminal_view.go)
  ├─ SFTP Dual-Pane View   (internal/ui/sftp_view.go)
  └─ Unlock/Edit/HostKey   (internal/ui/{unlock,edit,hostkey}.go)
        │
        ▼
Session Manager            (internal/session)  — 认证 + known_hosts
        │
  ┌─────┴──────┬────────────┬──────────────┐
  │ vault      │ sshterm    │ sftp         │ transfer
  │ (AES-GCM)  │ (PTY+xterm)│ (Backend)    │ (Queue)
  └────────────┴────────────┴──────────────┘
```

**核心原则**: UI 层只发命令、收消息;所有网络/磁盘 I/O 在后台 goroutine 执行,通过 `tea.Cmd` + `tea.Msg` 通信,UI 永不阻塞。

### 包职责
- `internal/vault` — 加密配置存储(AES-256-GCM + Argon2id)
- `internal/session` — SSH 连接、认证(password/key)、主机指纹校验
- `internal/sshterm` — 内嵌终端:PTY 驱动 + 轻量 xterm 状态机(Screen + Parser)
- `internal/sftp` — `Backend` 接口,本地(os)与远程(pkg/sftp)对称实现 + `Copy` 原语
- `internal/transfer` — 顺序传输队列,进度节流 + 失败重试
- `internal/ui` — Bubble Tea 视图层与全局路由
