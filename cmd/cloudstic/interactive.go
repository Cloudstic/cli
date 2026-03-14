package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/moby/term"
)

func (r *runner) canPrompt() bool {
	return !r.noPrompt && term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
}

func (r *runner) promptLine(label, defaultValue string) (string, error) {
	if defaultValue != "" {
		_, _ = fmt.Fprintf(r.errOut, "%s [%s]: ", label, defaultValue)
	} else {
		_, _ = fmt.Fprintf(r.errOut, "%s: ", label)
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultValue, nil
	}
	return line, nil
}

func (r *runner) promptConfirm(label string, defaultYes bool) (bool, error) {
	hint := "y/N"
	dflt := "n"
	if defaultYes {
		hint = "Y/n"
		dflt = "y"
	}
	answer, err := r.promptLine(fmt.Sprintf("%s [%s]", label, hint), dflt)
	if err != nil {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func (r *runner) promptSelect(label string, options []string) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("no options available")
	}
	_, _ = fmt.Fprintf(r.errOut, "%s\n", label)
	for i, opt := range options {
		_, _ = fmt.Fprintf(r.errOut, "  %d) %s\n", i+1, opt)
	}
	for {
		choice, err := r.promptLine("Select option number", "1")
		if err != nil {
			return "", err
		}
		n, err := strconv.Atoi(choice)
		if err != nil || n < 1 || n > len(options) {
			_, _ = fmt.Fprintln(r.errOut, "Invalid selection. Please choose a valid number.")
			continue
		}
		return options[n-1], nil
	}
}
