//go:build windows

package secretref

import (
	"context"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const credTypeGeneric = 1

type windowsCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

var (
	advapi32DLL                  = windows.NewLazySystemDLL("advapi32.dll")
	procCredReadW                = advapi32DLL.NewProc("CredReadW")
	procCredFree                 = advapi32DLL.NewProc("CredFree")
	wincredReadGenericCredential = readGenericCredential
)

func defaultWincredLookup(_ context.Context, target string) (string, error) {
	value, err := wincredReadGenericCredential(target)
	if err != nil {
		switch err {
		case windows.ERROR_NOT_FOUND:
			return "", errWincredNotFound
		case windows.ERROR_NO_SUCH_LOGON_SESSION:
			return "", fmt.Errorf("%w: Credential Manager unavailable in this logon session; this is common in service or scheduled-task contexts without a loaded user profile", errWincredUnavailable)
		default:
			return "", fmt.Errorf("windows credential lookup failed: %w", err)
		}
	}
	return value, nil
}

func readGenericCredential(target string) (string, error) {
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return "", fmt.Errorf("invalid windows credential target: %w", err)
	}

	var cred *windowsCredential
	r1, _, callErr := procCredReadW.Call(
		uintptr(unsafe.Pointer(targetPtr)),
		uintptr(credTypeGeneric),
		0,
		uintptr(unsafe.Pointer(&cred)),
	)
	if r1 == 0 {
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return "", callErr
		}
		return "", windows.ERROR_GEN_FAILURE
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(cred)))

	if cred == nil {
		return "", windows.ERROR_NOT_FOUND
	}
	if cred.CredentialBlob == nil || cred.CredentialBlobSize == 0 {
		return "", nil
	}

	blob := unsafe.Slice(cred.CredentialBlob, cred.CredentialBlobSize)
	return string(blob), nil
}
