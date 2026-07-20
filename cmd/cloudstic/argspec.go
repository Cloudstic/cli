package main

import "fmt"

// positionalSpec describes one positional command argument as data. Like
// flagSpec, it binds parsed input into the command's args struct and also
// carries the metadata needed by help and shell completion.
type positionalSpec struct {
	name       string
	required   bool
	repeatable bool
	completer  string
	bind       func([]string)
}

// requiredPositional binds exactly one required positional argument.
func requiredPositional(target *string, name string, completer ...string) positionalSpec {
	return positionalSpec{
		name:      name,
		required:  true,
		completer: firstCompleter(completer),
		bind:      func(values []string) { *target = values[0] },
	}
}

// optionalPositional binds at most one positional argument. When omitted, the
// target retains def.
func optionalPositional(target *string, name, def string, completer ...string) positionalSpec {
	*target = def
	return positionalSpec{
		name:      name,
		completer: firstCompleter(completer),
		bind: func(values []string) {
			if len(values) > 0 {
				*target = values[0]
			}
		},
	}
}

// requiredPositionals binds one or more trailing positional arguments.
func requiredPositionals(target *[]string, name string, completer ...string) positionalSpec {
	return positionalSpec{
		name:       name,
		required:   true,
		repeatable: true,
		completer:  firstCompleter(completer),
		bind:       func(values []string) { *target = values },
	}
}

// remainingPositionals binds zero or more trailing positional arguments.
func remainingPositionals(target *[]string, name string, completer ...string) positionalSpec {
	return positionalSpec{
		name:       name,
		repeatable: true,
		completer:  firstCompleter(completer),
		bind:       func(values []string) { *target = values },
	}
}

func firstCompleter(completers []string) string {
	if len(completers) == 0 {
		return ""
	}
	return completers[0]
}

// bindPositionals validates positional arity and binds values in declaration
// order. A repeatable positional must be last because it consumes the rest.
func bindPositionals(specs []positionalSpec, values []string) error {
	index := 0
	for i, spec := range specs {
		if spec.repeatable {
			if i != len(specs)-1 {
				return fmt.Errorf("positional %q must be last because it is repeatable", spec.name)
			}
			remaining := values[index:]
			if spec.required && len(remaining) == 0 {
				return fmt.Errorf("at least one %s is required", spec.name)
			}
			spec.bind(remaining)
			index = len(values)
			continue
		}

		if index >= len(values) {
			if spec.required {
				return fmt.Errorf("%s is required", spec.name)
			}
			spec.bind(nil)
			continue
		}
		spec.bind(values[index : index+1])
		index++
	}

	if index < len(values) {
		return fmt.Errorf("unexpected positional argument %q", values[index])
	}
	return nil
}

// zshSpec returns the _arguments entry for this positional.
func (s positionalSpec) zshSpec() string {
	prefix := ":"
	if s.repeatable {
		prefix = "*:"
	}
	return prefix + s.name + ":" + s.completer
}
