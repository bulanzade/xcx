# xcx — TUI SSH Connection Manager

[中文](README.md) | English

A terminal (TUI) SSH connection manager with a persistent host tree on the left and a terminal/SFTP/placeholder panel on the right. Host configurations are stored AES-256-GCM encrypted and unlocked with a master password.

## Installation

### Manual Installation

Download the latest build for your platform from [Releases](https://github.com/bulanzade/xcx/releases) and install it.

### One-line Install / Upgrade

**Linux / macOS** (installs to `~/.local/bin`, or `/usr/local/bin` when run as root):

```bash
curl -fsSL https://raw.githubusercontent.com/bulanzade/xcx/main/install.sh | sh
```

**Windows** (PowerShell, installs to `%LOCALAPPDATA%\Programs\xcx`, no administrator privileges required):

```powershell
iwr -useb https://raw.githubusercontent.com/bulanzade/xcx/main/install.ps1 | iex
```

### Uninstall

Uninstalling only removes the binary placed by the install script and rolls back backups; it does not remove the vault and known_hosts in `~/.config/xcx/` or `%AppData%\xcx\`.

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/bulanzade/xcx/main/install.sh | sh -s -- --uninstall
# Windows
& ([scriptblock]::Create((iwr -useb https://raw.githubusercontent.com/bulanzade/xcx/main/install.ps1).Content)) -Uninstall
```

### Version

```bash
xcx -version    # local builds show dev, release builds show the tag (e.g. v1.0.0)
```

## Build & Run

```bash
# Build (requires Go 1.25+)
go build -o xcx .

# Run (enters the full-screen TUI)
./xcx
```

The first run prompts you to set a master password (creating the encrypted vault); afterwards you enter the master password each time you start to unlock it.
Configuration directory: `~/.config/xcx/` (`%AppData%\xcx\` on Windows), containing `vault.bin` and `known_hosts`.

## Testing

```bash
go test ./...              # all
go test ./internal/vault/  # a single package
go test ./... -count=1     # skip cache
go test -v ./... | grep -c PASS
```

## Keyboard Shortcuts

### Host Tree
| Key | Action |
|---|---|
| `↑`/`↓` or `k`/`j` | Move selection |
| `Enter` | Connect to/resume the host terminal; collapse/expand on a group |
| `Space` | Collapse/expand group |
| `s` | Open SFTP |
| `e` | Edit host |
| `n` | New host (under the current group) |
| `N` | New group |
| `x` | Delete |

### Terminal
| Key | Action |
|---|---|
| (any key) | Send to the remote PTY |
| `Tab` | Send to the remote shell (for command completion) |
| `Shift+Tab` | Move focus back to the host tree |
| `Ctrl+S` | Open the SFTP panel for the current connection |
| `Ctrl+\` | Disconnect the current terminal |

### SFTP Dual Pane
| Key | Action |
|---|---|
| `Tab` | Cycle between the host tree, local pane, and remote pane |
| `Enter` | Enter directory |
| `Backspace`/`h` | Go to parent directory |
| `Space` | Multi-select the current file |
| `F5`/`t` | Copy to the opposite pane (download/upload) |
| `F7` | New directory |
| `F8`/`Del` | Delete |
| `r` | Refresh |
| `Esc` | Return to the terminal (if any), otherwise return to the right placeholder pane |

### Edit Host
| Key | Action |
|---|---|
| `Tab`/`↓` | Next field |
| `Shift+Tab`/`↑` | Previous field |
| `←`/`→`/`Space` | Toggle `password`/`key` in the `auth` field |
| `Enter` | Save |
| `Esc` | Cancel |

### Global
| Key | Action |
|---|---|
| `Ctrl+Q` / `Ctrl+C` | Quit when not focused on the terminal, closing all background SSH/SFTP/terminal connections |
