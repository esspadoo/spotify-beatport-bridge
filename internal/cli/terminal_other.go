//go:build !windows

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"golang.org/x/term"
)

func isTerminal(file *os.File) bool {
	return file != nil && term.IsTerminal(int(file.Fd()))
}

func openURL(target string) error {
	program := "xdg-open"
	if runtime.GOOS == "darwin" {
		program = "open"
	}
	if err := exec.Command(program, target).Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	return nil
}
