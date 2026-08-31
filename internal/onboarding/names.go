package onboarding

import (
	"fmt"
	"regexp"
)

// validRefName is the shape of a store, auth or profile reference name. The
// names become YAML keys and appear in -profile/-store-ref arguments, so they
// are kept to what is unambiguous in both.
var validRefName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// ValidateRefName rejects a name the profiles file cannot carry unambiguously.
//
// kind names what is being named ("store", "auth", "profile") and appears in
// the error, so a failure says which entry the user was creating.
func ValidateRefName(kind, name string) error {
	if !validRefName.MatchString(name) {
		return fmt.Errorf("invalid %s name %q: must start with a letter or digit and contain only letters, digits, dots, hyphens, or underscores", kind, name)
	}
	return nil
}
