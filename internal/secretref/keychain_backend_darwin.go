//go:build darwin && cgo

package secretref

import (
	"context"
	"fmt"
	"strings"

	"github.com/keybase/go-keychain"
)

var keychainGetGenericPasswordDarwin = keychain.GetGenericPassword

func defaultKeychainLookup(ctx context.Context, service, account string) (string, error) {
	_ = ctx

	out, err := keychainGetGenericPasswordDarwin(service, account, "", "")
	if err != nil {
		switch err {
		case keychain.ErrorItemNotFound:
			return "", errKeychainNotFound
		case keychain.ErrorInteractionNotAllowed, keychain.ErrorNotAvailable, keychain.ErrorNoSuchKeychain:
			return "", fmt.Errorf("%w: keychain locked or unavailable in this session", errKeychainUnavailable)
		default:
			return "", fmt.Errorf("keychain lookup failed: %w", err)
		}
	}
	if out == nil {
		return "", errKeychainNotFound
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}
