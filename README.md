# xcx — TUI SSH 连接管理工具

一个终端(TUI)SSH 连接管理器,左侧常驻主机树,右侧显示终端/SFTP/占位面板。主机配置以 AES-256-GCM 加密保存,用主密码解锁。

## 安装

### 手动安装

从 [Releases](https://github.com/bulanzade/xcx/releases) 下载对应平台的最新构建并安装。

### 一键安装 / 升级

**Linux / macOS**(装到 `~/.local/bin`,root 用户装到 `/usr/local/bin`):

```bash
curl -fsSL https://raw.githubusercontent.com/bulanzade/xcx/main/install.sh | sh
```

**Windows**(PowerShell,装到 `%LOCALAPPDATA%\Programs\xcx`,无需管理员权限):

```powershell
iwr -useb https://raw.githubusercontent.com/bulanzade/xcx/main/install.ps1 | iex
```

### 升级 / 强制重装

重新运行上面的安装命令即可升级。已是最新版本会自动跳过;强制重装(忽略版本对比):

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/bulanzade/xcx/main/install.sh | sh -s -- --force
# Windows
& ([scriptblock]::Create((iwr -useb https://raw.githubusercontent.com/bulanzade/xcx/main/install.ps1).Content)) -Force
```

### 卸载

卸载只删除安装脚本放置的二进制和回滚备份,不会删除 `~/.config/xcx/` 或 `%AppData%\xcx\` 中的 vault 与 known_hosts。

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/bulanzade/xcx/main/install.sh | sh -s -- --uninstall
# Windows
& ([scriptblock]::Create((iwr -useb https://raw.githubusercontent.com/bulanzade/xcx/main/install.ps1).Content)) -Uninstall
```

### 版本查看

```bash
xcx -version    # 本地构建显示 dev,release 构建显示 tag(如 v1.0.0)
```

## 构建 & 运行

```bash
# 构建(需 Go 1.25+)
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
| `Enter` | 连接/恢复主机终端;在分组上折叠/展开 |
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
| `Tab` | 发送到远端 shell(用于命令补全) |
| `Shift+Tab` | 焦点切回主机树 |
| `Ctrl+S` | 打开当前连接的 SFTP 面板 |
| `Ctrl+\` | 断开当前终端连接 |

### SFTP 双面板
| 键 | 动作 |
|---|---|
| `Tab` | 在主机树、本地面板、远程面板之间循环 |
| `Enter` | 进入目录 |
| `Backspace`/`h` | 返回上级 |
| `Space` | 多选当前文件 |
| `F5` | 复制到对侧(下载/上传) |
| `F6` | 复制到对侧(下载/上传) |
| `F7` | 新建目录 |
| `F8`/`Del` | 删除 |
| `r` | 刷新 |
| `Esc` | 返回终端(若存在),否则返回右侧占位面板 |

### 编辑主机
| 键 | 动作 |
|---|---|
| `Tab`/`↓` | 下一个字段 |
| `Shift+Tab`/`↑` | 上一个字段 |
| `←`/`→`/`Space` | 在 `auth` 字段切换 `password`/`key` |
| `Enter` | 保存 |
| `Esc` | 取消 |

### 全局
| 键 | 动作 |
|---|---|
| `Ctrl+Q` / `Ctrl+C` | 在非终端焦点下退出并关闭所有后台 SSH/SFTP/终端连接 |
