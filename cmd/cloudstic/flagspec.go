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
	//
	// It takes the name and usage to register under rather than closing over
	// them, so that a spec can be renamed after construction — which is what
	// lets `copy` derive its -from-* mirrors from the existing groups instead
	// of restating every repository flag. See prefixed.
	bind func(fs *flag.FlagSet, name, usage string)
	// setValue sets the flag's target from a raw string, returning an
	// actionable error when the value cannot be parsed. It serves both of the
	// after-parsing resolution steps — environment values and late defaults —
	// since both arrive as text and both need the same parse. Nil for a flag
	// backed by a custom flag.Value, which owns its own parsing.
	setValue func(raw string) error
	// lateDefault computes the flag's default value after parsing, for a
	// default that depends on another flag. Nil for the ordinary case of a
	// constant default passed to bind. See withLateDefault.
	lateDefault func() (string, error)
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

// withLateDefault supplies a default computed after parsing, for a flag whose
// default depends on another flag's resolved value — -profiles-file, which
// lives inside whatever -config-dir names.
//
// It runs only when the flag was left at its built-in default: an explicit
// flag and an environment value both still win, and neither pays for the
// computation. Resolving late is also what keeps the computation off the paths
// that merely *describe* the flag. Help and completion build a command's flag
// set without parsing it, so a default computed at declaration time runs on
// `-h` — which is how resolving the profiles path used to create the config
// directory as a side effect of being asked for help.
func withLateDefault(fn func() (string, error)) flagOpt {
	return func(s *flagSpec) { s.lateDefault = fn }
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
	spec.bind = func(fs *flag.FlagSet, name, usage string) {
		fs.StringVar(target, name, def, usage)
	}
	spec.setValue = func(raw string) error {
		*target = raw
		return nil
	}
	return spec
}

// boolFlag describes a boolean flag bound to target.
func boolFlag(target *bool, name string, def bool, usage string, opts ...flagOpt) flagSpec {
	s := applyOpts(&flagSpec{name: name, usage: usage, isBool: true}, opts)
	spec := *s
	spec.bind = func(fs *flag.FlagSet, name, usage string) {
		fs.BoolVar(target, name, def, usage)
	}
	spec.setValue = func(raw string) error {
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
	spec.bind = func(fs *flag.FlagSet, name, usage string) {
		fs.IntVar(target, name, def, usage)
	}
	spec.setValue = func(raw string) error {
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
	spec.bind = func(fs *flag.FlagSet, name, usage string) {
		fs.Var(target, name, usage)
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
		case s.env != "" && s.setValue != nil:
			raw := lookupEnv(s.env)
			if raw == "" {
				origins[s.name] = originDefault
				continue
			}
			if err := s.setValue(raw); err != nil {
				return nil, fmt.Errorf("environment variable %s: %w", s.env, err)
			}
			origins[s.name] = originEnv
		default:
			origins[s.name] = originDefault
		}
	}
	return origins, nil
}

// applyLateDefaults computes the default of every flag that declares one and
// is still sitting at its built-in value.
//
// It runs as a separate pass after applyEnvDefaults rather than inside it,
// because a late default reads other flags: -profiles-file's default is a path
// inside -config-dir, which is only correct once every flag has taken its
// final value. Folding the two together would make the result depend on the
// order specs happen to be declared in.
//
// The origin stays originDefault, which is what it is — the user chose
// nothing, so a profile is still free to override the value.
func applyLateDefaults(specs []flagSpec, origins map[string]flagOrigin) error {
	for _, s := range specs {
		if s.lateDefault == nil || s.setValue == nil || origins[s.name] != originDefault {
			continue
		}
		value, err := s.lateDefault()
		if err != nil {
			return fmt.Errorf("resolve default for -%s: %w", s.name, err)
		}
		if err := s.setValue(value); err != nil {
			return fmt.Errorf("resolve default for -%s: %w", s.name, err)
		}
	}
	return nil
}

// prefixed returns specs renamed with the given prefix and re-described with
// the given label, for a command that addresses a second instance of something
// the global flags already describe — `copy`, which needs a whole second
// repository (RFC 0017 §2).
//
// The label is appended to each usage string because the original text is
// written for the only repository a command usually has: left alone, the mirror
// of -store-sftp-password would read "SFTP store password" next to a -source-*
// flag that means something else entirely.
//
// Deriving the mirrors rather than restating them is what keeps the two sets
// from drifting: a repository flag added later is mirrored without a matching
// edit here, which is not true of a hand-written list. The original specs are
// left untouched; flagSpec is copied by value and its closures write to the
// targets bound at construction, so a caller mirrors a group built over a
// *different* destination struct.
//
// Environment bindings are deliberately dropped. CLOUDSTIC_PASSWORD means "the
// repository I am operating on", and silently applying one ambient value to
// both repositories of a two-repository command is how an operator unlocks the
// wrong one — or believes they did. Ambient variables configure the
// destination; the source is named explicitly, or through a profile whose
// entry may carry env:// secret references like any other.
//
// Late defaults are dropped for the same reason they exist: one computes a path
// inside -config-dir, which is not mirrored, so a mirrored late default would
// resolve against a struct that has no config directory set.
func prefixed(prefix, label string, specs []flagSpec) []flagSpec {
	out := make([]flagSpec, 0, len(specs))
	for _, s := range specs {
		s.name = prefix + s.name
		s.env = ""
		s.lateDefault = nil
		s.usage = s.usage + " (" + label + ")"
		if s.shortUsage != "" {
			s.shortUsage = s.shortUsage + " (" + label + ")"
		}
		out = append(out, s)
	}
	return out
}

// bindFlags registers every spec on fs, in declaration order.
func bindFlags(fs *flag.FlagSet, specs []flagSpec) {
	for _, s := range specs {
		s.bind(fs, s.name, s.bindUsage())
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

// commandFlags owns the parser state assembled from one command declaration.
// The underlying *flag.FlagSet is deliberately unexported so the flag package
// stays an implementation detail rather than leaking into commands.
type commandFlags struct {
	set         *flag.FlagSet
	globals     *globalFlags
	global      []flagSpec
	own         []flagSpec
	positionals []positionalSpec
}

// specs returns every specification bound into the set, globals first.
func (c commandFlags) specs() []flagSpec {
	all := make([]flagSpec, 0, len(c.global)+len(c.own))
	all = append(all, c.global...)
	return append(all, c.own...)
}

// ownSpecs returns only the command's own flags, excluding global groups.
func (c commandFlags) ownSpecs() []flagSpec { return c.own }

// names returns every flag name registered, globals included.
func (c commandFlags) names() []string { return flagNames(c.set) }

// lookup returns the registered flag with the given name, or nil.
func (c commandFlags) lookup(name string) *flag.Flag { return c.set.Lookup(name) }
