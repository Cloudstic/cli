package main

import (
	"flag"
	"os"
	"strconv"
)

// lookupEnv reads an environment variable. Indirected through a variable so
// tests can supply a fake environment without mutating the process.
var lookupEnv = os.Getenv

// flagSpec describes a single CLI flag as data rather than as a bare
// fs.StringVar call. Keeping the description declarative lets one definition
// drive flag registration, help text, shell completion, and — via env and
// secret — environment-variable resolution and redaction.
type flagSpec struct {
	// name is the flag as typed, without the leading dash.
	name string
	// usage is the one-line help text.
	usage string
	// env names the environment variable consulted for this flag's default.
	// Empty when the flag has no environment binding.
	env string
	// secret marks a flag whose value must never be rendered back to the user
	// (help output, logs, error messages). See #266.
	secret bool
	// repeatable marks a flag that may be given more than once, which shells
	// need to know to keep offering it (zsh renders these as '*-tag').
	repeatable bool
	// completer names the shell completion function used for this flag's
	// value, e.g. "_files" or "_cloudstic_auth_names". Empty means no
	// value-specific completion.
	completer string
	// placeholder is the value name shown in usage output, e.g. "<path>".
	placeholder string
	// shortUsage is a concise description for shell completion menus, where the
	// full usage text is often too long to be readable. Optional; completion
	// falls back to usage. Kept on the spec so there is still one declaration
	// per flag rather than a separately maintained completion description.
	shortUsage string
	// isBool reports whether the flag takes no value. Shells must not consume
	// the following word for boolean flags.
	isBool bool
	// bind registers the flag on a flag set.
	bind func(fs *flag.FlagSet)
}

// flagOpt customises a flagSpec at construction time.
type flagOpt func(*flagSpec)

// withEnv binds an environment variable as the flag's default source.
func withEnv(name string) flagOpt {
	return func(s *flagSpec) { s.env = name }
}

// asSecret marks the flag's value as sensitive.
func asSecret() flagOpt {
	return func(s *flagSpec) { s.secret = true }
}

// asRepeatable marks the flag as accepting repeated occurrences.
func asRepeatable() flagOpt {
	return func(s *flagSpec) { s.repeatable = true }
}

// withCompleter attaches a shell completion function for the flag's value.
func withCompleter(name string) flagOpt {
	return func(s *flagSpec) { s.completer = name }
}

// withShortUsage sets a concise description used in shell completion menus.
func withShortUsage(text string) flagOpt {
	return func(s *flagSpec) { s.shortUsage = text }
}

// completionUsage returns the description shells should display.
func (s flagSpec) completionUsage() string {
	if s.shortUsage != "" {
		return s.shortUsage
	}
	return s.usage
}

// withPlaceholder sets the value name shown in usage output.
func withPlaceholder(name string) flagOpt {
	return func(s *flagSpec) { s.placeholder = name }
}

func applyOpts(s *flagSpec, opts []flagOpt) *flagSpec {
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// stringFlag describes a string flag bound to target. When the spec declares
// an environment variable, its value takes precedence over def.
func stringFlag(target *string, name, def, usage string, opts ...flagOpt) flagSpec {
	s := applyOpts(&flagSpec{name: name, usage: usage}, opts)
	spec := *s
	spec.bind = func(fs *flag.FlagSet) {
		fs.StringVar(target, name, envDefault(spec.env, def), usage)
	}
	return spec
}

// boolFlag describes a boolean flag bound to target.
func boolFlag(target *bool, name string, def bool, usage string, opts ...flagOpt) flagSpec {
	s := applyOpts(&flagSpec{name: name, usage: usage, isBool: true}, opts)
	spec := *s
	spec.bind = func(fs *flag.FlagSet) {
		fs.BoolVar(target, name, envBoolDefault(spec.env, def), usage)
	}
	return spec
}

// intFlag describes an integer flag bound to target.
func intFlag(target *int, name string, def int, usage string, opts ...flagOpt) flagSpec {
	s := applyOpts(&flagSpec{name: name, usage: usage}, opts)
	spec := *s
	spec.bind = func(fs *flag.FlagSet) {
		fs.IntVar(target, name, envIntDefault(spec.env, def), usage)
	}
	return spec
}

// valueFlag describes a flag backed by a custom flag.Value, such as the
// repeatable stringArrayFlags used by -tag and -exclude.
func valueFlag(target flag.Value, name, usage string, opts ...flagOpt) flagSpec {
	s := applyOpts(&flagSpec{name: name, usage: usage}, opts)
	spec := *s
	spec.bind = func(fs *flag.FlagSet) {
		fs.Var(target, name, usage)
	}
	return spec
}

// envDefault returns the environment value for key when set and non-empty,
// otherwise fallback. An empty key means the flag has no environment binding.
func envDefault(key, fallback string) string {
	if key == "" {
		return fallback
	}
	if v := lookupEnv(key); v != "" {
		return v
	}
	return fallback
}

// envBoolDefault resolves a boolean default from the environment.
func envBoolDefault(key string, fallback bool) bool {
	if key == "" {
		return fallback
	}
	v := lookupEnv(key)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return parsed
}

// envIntDefault resolves an integer default from the environment.
func envIntDefault(key string, fallback int) int {
	if key == "" {
		return fallback
	}
	v := lookupEnv(key)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return parsed
}

// bindFlags registers every spec on fs, in declaration order.
func bindFlags(fs *flag.FlagSet, specs []flagSpec) {
	for _, s := range specs {
		s.bind(fs)
	}
}

// specNames returns the flag names described by specs.
func specNames(specs []flagSpec) []string {
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		names = append(names, s.name)
	}
	return names
}
