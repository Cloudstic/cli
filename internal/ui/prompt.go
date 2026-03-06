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

// PromptPasswordConfirm prompts for a password and then a confirmation.
// Returns an error if they don't match or are empty.
func PromptPasswordConfirm(msg string) (string, error) {
	p1, err := PromptPassword(msg)
	if err != nil {
		return "", err
	}
	if p1 == "" {
		return "", fmt.Errorf("password cannot be empty")
	}

	p2, err := PromptPassword("Confirm password")
	if err != nil {
		return "", err
	}

	if p1 != p2 {
		return "", fmt.Errorf("passwords do not match")
	}

	return p1, nil
}
