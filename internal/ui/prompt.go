package ui

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// PromptPassword prompts the user for a password on the terminal without echoing.
func PromptPassword(prompt string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", prompt)

	// We use os.Stdin.Fd() to read from the terminal.
	// golang.org/x/term.ReadPassword handles disabling echo.
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr) // Move to the next line after the password is entered
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(pw)), nil
}
