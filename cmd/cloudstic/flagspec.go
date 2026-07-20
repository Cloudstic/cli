package main

import (
	"flag"
	"fmt"
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
	// bind registers the flag on a flag set, using only the built-in default.
	// Environment values are applied after parsing so that help output can
	// never render a live environment value (see applyEnvDefaults).
	bind func(fs *flag.FlagSet)
	// applyEnv sets the flag's target from a raw environment value, returning
	// an actionable error when the value cannot be parsed. Nil when the flag
	// has no environment binding.
	applyEnv func(raw string) error
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
		fs.StringVar(target, name, def, spec.bindUsage())
	}
	spec.applyEnv = func(raw string) error {
		*target = raw
		return nil
	}
	return spec
}

// boolFlag describes a boolean flag bound to target.
func boolFlag(target *bool, name string, def bool, usage string, opts ...flagOpt) flagSpec {
	s := applyOpts(&flagSpec{name: name, usage: usage, isBool: true}, opts)
	spec := *s
	spec.bind = func(fs *flag.FlagSet) {
		fs.BoolVar(target, name, def, spec.bindUsage())
	}
	spec.applyEnv = func(raw string) error {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("invalid boolean value %q: use true or false", raw)
		}
		*target = parsed
		return nil
	}
	return spec
}

// intFlag describes an integer flag bound to target.
func intFlag(target *int, name string, def int, usage string, opts ...flagOpt) flagSpec {
	s := applyOpts(&flagSpec{name: name, usage: usage}, opts)
	spec := *s
	spec.bind = func(fs *flag.FlagSet) {
		fs.IntVar(target, name, def, spec.bindUsage())
	}
	spec.applyEnv = func(raw string) error {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("invalid integer value %q", raw)
		}
		*target = parsed
		return nil
	}
	return spec
}

// valueFlag describes a flag backed by a custom flag.Value, such as the
// repeatable stringArrayFlags used by -tag and -exclude.
func valueFlag(target flag.Value, name, usage string, opts ...flagOpt) flagSpec {
	s := applyOpts(&flagSpec{name: name, usage: usage}, opts)
	spec := *s
	spec.bind = func(fs *flag.FlagSet) {
		fs.Var(target, name, spec.bindUsage())
	}
	return spec
}

// bindUsage returns the usage text shown in help output. When a flag reads an
// environment variable, the variable's *name* is appended so users can discover
// it — its value is never rendered, which is what keeps secrets out of -h.
func (s flagSpec) bindUsage() string {
	if s.env == "" {
		return s.usage
	}
	return s.usage + " [$" + s.env + "]"
}

// envDefault returns the environment value for key when set and non-empty,
// otherwise fallback. Used for the few defaults that are computed before
// binding rather than resolved after parsing.
func envDefault(key, fallback string) string {
	if key == "" {
		return fallback
	}
	if v := lookupEnv(key); v != "" {
		return v
	}
	return fallback
}

// flagOrigin records where a resolved flag value came from.
type flagOrigin int

const (
	originDefault flagOrigin = iota
	originEnv
	originFlag
)

func (o flagOrigin) String() string {
	switch o {
	case originFlag:
		return "flag"
	case originEnv:
		return "environment"
	default:
		return "default"
	}
}

// applyEnvDefaults fills in values from the environment for every flag the user
// did not pass explicitly, and reports where each resolved value came from.
//
// Precedence is unchanged from before — explicit flag, then environment, then
// built-in default — but the environment is consulted *after* parsing rather
// than baked into each flag's default. That is what allows help output to show
// the built-in default and the variable name instead of a live secret.
func applyEnvDefaults(fs *flag.FlagSet, specs []flagSpec) (map[string]flagOrigin, error) {
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	origins := make(map[string]flagOrigin, len(specs))
	for _, s := range specs {
		switch {
		case explicit[s.name]:
			origins[s.name] = originFlag
		case s.env != "" && s.applyEnv != nil:
			raw := lookupEnv(s.env)
			if raw == "" {
				origins[s.name] = originDefault
				continue
			}
			if err := s.applyEnv(raw); err != nil {
				return nil, fmt.Errorf("environment variable %s: %w", s.env, err)
			}
			origins[s.name] = originEnv
		default:
			origins[s.name] = originDefault
		}
	}
	return origins, nil
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

// boundFlagSet pairs a flag set with the specifications bound into it, split
// into the global groups a command opted into and its own flags. Parsing needs
// all of them (to resolve environment values); completion generation needs
// only the command's own.
type boundFlagSet struct {
	set    *flag.FlagSet
	global []flagSpec
	own    []flagSpec
}

// all returns every specification bound into the set.
func (b boundFlagSet) all() []flagSpec {
	specs := make([]flagSpec, 0, len(b.global)+len(b.own))
	specs = append(specs, b.global...)
	return append(specs, b.own...)
}
