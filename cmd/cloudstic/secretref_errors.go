// Secret-reference error classification used by store_health.go.
//
// The encryption *workflow* moved to internal/onboarding; this stayed because
// its only caller is the store reachability check, which is CLI wiring.
package main

import (
	"errors"

	"github.com/cloudstic/cli/pkg/secretref"
)

func isSecretNotFoundError(err error) bool {
	var refErr *secretref.Error
	if errors.As(err, &refErr) {
		return refErr.Kind == secretref.KindNotFound
	}
	return false
}
