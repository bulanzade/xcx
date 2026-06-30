package ui

import (
	"fmt"
	"os"

	"github.com/atotto/clipboard"
	osc52 "github.com/aymanbagabas/go-osc52/v2"
)

type appClipboard interface {
	Copy(text string) (string, error)
	Paste() (string, error)
}

type systemClipboard struct{}

func (systemClipboard) Copy(text string) (string, error) {
	if err := clipboard.WriteAll(text); err == nil {
		return "clipboard", nil
	} else if _, oscErr := osc52.New(text).WriteTo(os.Stderr); oscErr != nil {
		return "", fmt.Errorf("clipboard: %v; osc52: %w", err, oscErr)
	}
	return "OSC52", nil
}

func (systemClipboard) Paste() (string, error) {
	return clipboard.ReadAll()
}
