// Command xcx is a TUI SSH connection manager with an embedded interactive
// terminal and a dual-pane SFTP file manager. Host connection details are
// stored in an AES-256-GCM encrypted vault unlocked by a master password.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"xcx/internal/ui"
)

// version is the build version, injected via -ldflags "-X main.version=..." in
// the release workflow. "dev" is the default for local builds.
var version = "dev"

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
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	dir, err := configDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "xcx: cannot init config dir: %v\n", err)
		os.Exit(1)
	}
	app := ui.New(ui.Options{
		VaultPath:      filepath.Join(dir, "vault.bin"),
		KnownHostsPath: filepath.Join(dir, "known_hosts"),
	})

	// Mouse reporting lets xcx own terminal-pane scrolling and selection.
	// Native host-terminal selection is intercepted while this is enabled, so
	// the app implements Windows Terminal-style left-select/right-copy-paste.
	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "xcx: %v\n", err)
		os.Exit(1)
	}
}
