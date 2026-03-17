//go:build windows

package secretref

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const credPersistSession = 1

var (
	procCredWriteW  = advapi32DLL.NewProc("CredWriteW")
	procCredDeleteW = advapi32DLL.NewProc("CredDeleteW")
)

func TestWincredBackendIntegration(t *testing.T) {
	target := fmt.Sprintf("cloudstic-test-%d", time.Now().UnixNano())
	secret := "cloudstic-test-secret"

	if err := writeTestGenericCredential(target, secret); err != nil {
		if errors.Is(err, windows.ERROR_NO_SUCH_LOGON_SESSION) {
			t.Skipf("Credential Manager unavailable in this logon session: %v", err)
		}
		t.Fatalf("write test credential: %v", err)
	}
	t.Cleanup(func() {
		_ = deleteTestGenericCredential(target)
	})

	b := NewWincredBackend()
	got, err := b.Resolve(context.Background(), Ref{
		Raw:    "wincred://" + target,
		Scheme: "wincred",
		Path:   target,
	})
	if err != nil {
		var refErr *Error
		if errors.As(err, &refErr) && refErr.Kind == KindBackendUnavailable {
			t.Skipf("wincred backend unavailable: %v", err)
		}
		t.Fatalf("Resolve: %v", err)
	}
	if got != secret {
		t.Fatalf("Resolve: got %q want %q", got, secret)
	}
}

func writeTestGenericCredential(target, secret string) error {
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	blob := []byte(secret)
	cred := windowsCredential{
		Type:               credTypeGeneric,
		TargetName:         targetPtr,
		CredentialBlobSize: uint32(len(blob)),
		Persist:            credPersistSession,
	}
	if len(blob) > 0 {
		cred.CredentialBlob = &blob[0]
	}
	r1, _, callErr := procCredWriteW.Call(uintptr(unsafe.Pointer(&cred)), 0)
	if r1 == 0 {
		return callErr
	}
	return nil
}

func deleteTestGenericCredential(target string) error {
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r1, _, callErr := procCredDeleteW.Call(uintptr(unsafe.Pointer(targetPtr)), uintptr(credTypeGeneric), 0)
	if r1 == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return nil
		}
		return callErr
	}
	return nil
}
