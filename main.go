// Command xcx is a TUI SSH connection manager with an embedded interactive
// terminal and a dual-pane SFTP file manager. Host connection details are
// stored in an AES-256-GCM encrypted vault unlocked by a master password.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"xcx/internal/ui"
)

// configDir resolves the per-user configuration directory for xcx
// (~/.config/xcx on Unix, %AppData%\xcx on Windows), creating it on demand.
func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "xcx")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func main() {
	dir, err := configDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "xcx: cannot init config dir: %v\n", err)
		os.Exit(1)
	}
	app := ui.New(ui.Options{
		VaultPath:      filepath.Join(dir, "vault.bin"),
		KnownHostsPath: filepath.Join(dir, "known_hosts"),
	})

	p := tea.NewProgram(app, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "xcx: %v\n", err)
		os.Exit(1)
	}
}
