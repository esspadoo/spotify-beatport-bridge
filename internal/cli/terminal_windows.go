//go:build windows

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/term"
)

func isTerminal(file *os.File) bool {
	return file != nil && term.IsTerminal(int(file.Fd()))
}

func openURL(target string) error {
	command := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", target)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := command.Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	return nil
}
