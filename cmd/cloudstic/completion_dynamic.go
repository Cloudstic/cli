package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	cloudstic "github.com/cloudstic/cli"
)

var completionLoadProfilesFile = cloudstic.LoadProfilesFile

func completionQueryCommandSpec() *commandSpec {
	return leaf("__complete", "", "", runCompletionQuery, profilesFileFlag()).hide().withoutFlagProbe()
}

func runCompletionQuery(r *runner, ctx context.Context) int {
	if len(r.args) < 1 {
		return 0
	}
	kind := r.args[0]
	current := ""
	if len(r.args) > 1 {
		current = r.args[1]
	}
	var commandArgs []string
	if len(r.args) > 2 {
		commandArgs = r.args[2:]
	}
	candidates, err := completionCandidates(ctx, kind, current, commandArgs)
	if err != nil {
		return 0
	}
	for _, candidate := range candidates {
		_, _ = fmt.Fprintln(r.out, candidate)
	}
	return 0
}

func completionCandidates(_ context.Context, kind, _ string, args []string) ([]string, error) {
	switch kind {
	case "profile-names":
		return completionProfileNames(args)
	case "auth-names":
		return completionAuthNames(args)
	default:
		return nil, nil
	}
}

func completionProfileNames(args []string) ([]string, error) {
	cfg, err := completionLoadProfilesConfig(completionProfilesPath(args))
	if err != nil {
		return nil, err
	}
	return sortedKeys(cfg.Profiles), nil
}

func completionAuthNames(args []string) ([]string, error) {
	cfg, err := completionLoadProfilesConfig(completionProfilesPath(args))
	if err != nil {
		return nil, err
	}
	return sortedKeys(cfg.Auth), nil
}

func completionLoadProfilesConfig(path string) (*cloudstic.ProfilesConfig, error) {
	cfg, err := completionLoadProfilesFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &cloudstic.ProfilesConfig{Version: 1}, nil
		}
		return nil, err
	}
	ensureProfilesMaps(cfg)
	return cfg, nil
}

func completionProfilesPath(args []string) string {
	command := completionQueryCommandSpec()
	fs := flag.NewFlagSet("__complete", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	profilesFile := fs.String("profiles-file", defaultProfilesPathFallback(), "")
	_ = parseFlags(fs, filterCompletionFlags(args, command.flags), command)
	return *profilesFile
}

func filterCompletionFlags(args []string, flagSpecs []flagSpec) []string {
	specs := make(map[string]bool, len(flagSpecs))
	for _, spec := range flagSpecs {
		specs[spec.name] = spec.takesValue()
	}
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if len(arg) == 0 || arg[0] != '-' {
			continue
		}
		name, hasValue, value := splitCompletionFlag(arg)
		takesValue, ok := specs[name]
		if !ok {
			continue
		}
		if hasValue {
			filtered = append(filtered, arg)
			continue
		}
		filtered = append(filtered, arg)
		if takesValue && i+1 < len(args) {
			filtered = append(filtered, args[i+1])
			i++
		}
		if !takesValue && value != "" {
			continue
		}
	}
	return filtered
}

func splitCompletionFlag(arg string) (name string, hasValue bool, value string) {
	trimmed := arg
	for len(trimmed) > 0 && trimmed[0] == '-' {
		trimmed = trimmed[1:]
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] == '=' {
			return trimmed[:i], true, trimmed[i+1:]
		}
	}
	return trimmed, false, ""
}
