//go:build darwin && cgo

package main

import (
	"context"
	"fmt"

	"github.com/keybase/go-keychain"
)

var (
	keychainAddItem             = keychain.AddItem
	keychainUpdateItem          = keychain.UpdateItem
	keychainGetGenericPassword  = keychain.GetGenericPassword
	keychainGenericPasswordKind = keychain.SecClassGenericPassword
)

func saveSecretToNativeStore(ctx context.Context, service, account, value string) error {
	_ = ctx

	item := keychain.NewItem()
	item.SetSecClass(keychainGenericPasswordKind)
	item.SetService(service)
	item.SetAccount(account)
	item.SetAccessible(keychain.AccessibleWhenUnlockedThisDeviceOnly)
	item.SetData([]byte(value))

	if err := keychainAddItem(item); err != nil {
		if err == keychain.ErrorDuplicateItem {
			query := keychain.NewItem()
			query.SetSecClass(keychainGenericPasswordKind)
			query.SetService(service)
			query.SetAccount(account)

			update := keychain.NewItem()
			update.SetData([]byte(value))

			if updateErr := keychainUpdateItem(query, update); updateErr != nil {
				return fmt.Errorf("save secret in macOS keychain failed: %v", updateErr)
			}
			return nil
		}
		return fmt.Errorf("save secret in macOS keychain failed: %v", err)
	}
	return nil
}

func nativeSecretExists(ctx context.Context, service, account string) (bool, error) {
	_ = ctx

	data, err := keychainGetGenericPassword(service, account, "", "")
	if err != nil {
		if err == keychain.ErrorItemNotFound {
			return false, nil
		}
		return false, fmt.Errorf("check secret in macOS keychain failed: %v", err)
	}
	if data == nil {
		return false, nil
	}
	return true, nil
}
