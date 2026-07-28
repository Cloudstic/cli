package backends

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/cloudstic/cli/pkg/secretref"
)

type EnvLookup func(string) (string, bool)

// EnvBackend resolves env://VAR references.
type EnvBackend struct {
	lookup EnvLookup
}

func NewEnvBackend(lookup EnvLookup) *EnvBackend {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return &EnvBackend{lookup: lookup}
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (b *EnvBackend) Resolve(_ context.Context, ref secretref.Ref) (string, error) {
	name := strings.TrimSpace(ref.Path)
	if strings.HasPrefix(name, "/") {
		name = strings.TrimLeft(name, "/")
	}
	if name == "" || !envNameRe.MatchString(name) {
		return "", secretref.NewError(secretref.KindInvalidRef, ref.Raw, "invalid env variable name in env:// reference", nil)
	}

	value, ok := b.lookup(name)
	if !ok {
		return "", secretref.NewError(secretref.KindNotFound, ref.Raw, fmt.Sprintf("environment variable %q is not set", name), nil)
	}
	return value, nil
}
