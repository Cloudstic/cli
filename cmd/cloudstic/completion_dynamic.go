package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/cloudstic/cli/pkg/profile"
)

var completionLoadProfilesFile = profile.LoadOrEmpty

type completionQueryArgs struct{ values []string }

func declareCompletionQueryArgs(_ *globalFlags) (*completionQueryArgs, commandInput) {
	a := &completionQueryArgs{}
	return a, commandInput{positionals: []positionalSpec{remainingPositionals(&a.values, "query argument")}}
}

func runCompletionQuery(r *runner, ctx context.Context, a *completionQueryArgs) int {
	if len(a.values) < 1 {
		return 0
	}
	kind := a.values[0]
	current := ""
	if len(a.values) > 1 {
		current = a.values[1]
	}
	var commandArgs []string
	if len(a.values) > 2 {
		commandArgs = a.values[2:]
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

func completionLoadProfilesConfig(path string) (*profile.Config, error) {
	cfg, err := completionLoadProfilesFile(path)
	if err != nil {
		return nil, err
	}
	ensureProfilesMaps(cfg)
	return cfg, nil
}

func completionProfilesPath(args []string) string {
	fs := flag.NewFlagSet("__complete", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	defaultPath := envDefault("CLOUDSTIC_PROFILES_FILE", defaultProfilesPathFallback())
	profilesFile := fs.String("profiles-file", defaultPath, "")
	_ = fs.Parse(filterCompletionFlags(args, map[string]bool{
		"profiles-file": true,
	}))
	return *profilesFile
}

func filterCompletionFlags(args []string, specs map[string]bool) []string {
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

// completeCommand is the internal dynamic-completion helper.
func completeCommand() command {
	return leaf("__complete", "Internal dynamic completion helper",
		nil, declareCompletionQueryArgs, runCompletionQuery, asHidden())
}
