//go:build darwin && cgo

package backends

import (
	"context"
	"errors"
	"fmt"

	"github.com/keybase/go-keychain"
)

var keychainGetGenericPasswordDarwin = keychain.GetGenericPassword
var keychainAddItemDarwin = keychain.AddItem
var keychainUpdateItemDarwin = keychain.UpdateItem
var keychainDeleteItemDarwin = keychain.DeleteItem

func defaultKeychainWriteSupported() bool { return true }

func defaultKeychainLookupBlob(ctx context.Context, service, account string) ([]byte, error) {
	_ = ctx

	out, err := keychainGetGenericPasswordDarwin(service, account, "", "")
	if err != nil {
		switch {
		case errors.Is(err, keychain.ErrorItemNotFound):
			return nil, errKeychainNotFound
		case errors.Is(err, keychain.ErrorInteractionNotAllowed),
			errors.Is(err, keychain.ErrorNotAvailable),
			errors.Is(err, keychain.ErrorNoSuchKeychain):
			return nil, fmt.Errorf("%w: keychain locked or unavailable in this session", errKeychainUnavailable)
		default:
			return nil, fmt.Errorf("keychain lookup failed: %w", err)
		}
	}
	if out == nil {
		return nil, errKeychainNotFound
	}
	return out, nil
}

func defaultKeychainExists(ctx context.Context, service, account string) (bool, error) {
	_, err := defaultKeychainLookupBlob(ctx, service, account)
	if err != nil {
		switch {
		case errors.Is(err, errKeychainNotFound):
			return false, nil
		default:
			return false, err
		}
	}
	return true, nil
}

func defaultKeychainStoreBlob(_ context.Context, service, account string, value []byte) error {
	item := keychain.NewItem()
	item.SetSecClass(keychain.SecClassGenericPassword)
	item.SetService(service)
	item.SetAccount(account)
	item.SetAccessible(keychain.AccessibleWhenUnlockedThisDeviceOnly)
	item.SetData(value)

	if err := keychainAddItemDarwin(item); err != nil {
		if errors.Is(err, keychain.ErrorDuplicateItem) {
			query := keychain.NewItem()
			query.SetSecClass(keychain.SecClassGenericPassword)
			query.SetService(service)
			query.SetAccount(account)

			update := keychain.NewItem()
			update.SetData(value)

			if updateErr := keychainUpdateItemDarwin(query, update); updateErr != nil {
				return mapKeychainStoreError(updateErr)
			}
			return nil
		}
		return mapKeychainStoreError(err)
	}
	return nil
}

func defaultKeychainDelete(_ context.Context, service, account string) error {
	query := keychain.NewItem()
	query.SetSecClass(keychain.SecClassGenericPassword)
	query.SetService(service)
	query.SetAccount(account)

	if err := keychainDeleteItemDarwin(query); err != nil {
		if errors.Is(err, keychain.ErrorItemNotFound) {
			return nil
		}
		return mapKeychainStoreError(err)
	}
	return nil
}

func mapKeychainStoreError(err error) error {
	switch {
	case errors.Is(err, keychain.ErrorInteractionNotAllowed),
		errors.Is(err, keychain.ErrorNotAvailable),
		errors.Is(err, keychain.ErrorNoSuchKeychain):
		return fmt.Errorf("%w: keychain locked or unavailable in this session", errKeychainUnavailable)
	default:
		return fmt.Errorf("keychain write failed: %w", err)
	}
}
