package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// ReadPassword hides input when the source is a terminal. Redirected input is
// accepted for test automation but is never echoed by BPBridge.
func ReadPassword(in *os.File, fallback *bufio.Reader, out io.Writer, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	if in != nil && term.IsTerminal(int(in.Fd())) {
		value, err := term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(out)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(value)), nil
	}
	if fallback == nil {
		return "", fmt.Errorf("password input is unavailable")
	}
	value, err := fallback.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(value), nil
}
