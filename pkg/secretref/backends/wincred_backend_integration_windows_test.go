//go:build windows

package backends

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/cloudstic/cli/pkg/secretref"
)

var (
	procCredDeleteW = advapi32DLL.NewProc("CredDeleteW")
)

func TestWincredBackendIntegration(t *testing.T) {
	target := fmt.Sprintf("cloudstic-test-%d", time.Now().UnixNano())
	secret := "cloudstic-test-secret"

	b := NewWincredBackend()
	if err := b.Store(context.Background(), secretref.Ref{Raw: "wincred://" + target, Scheme: "wincred", Path: target}, secret); err != nil {
		var refErr *secretref.Error
		if errors.As(err, &refErr) && refErr.Kind == secretref.KindBackendUnavailable {
			t.Skipf("Credential Manager unavailable in this logon session: %v", err)
		}
		t.Fatalf("Store: %v", err)
	}
	t.Cleanup(func() {
		_ = deleteTestGenericCredential(target)
	})

	got, err := b.Resolve(context.Background(), secretref.Ref{
		Raw:    "wincred://" + target,
		Scheme: "wincred",
		Path:   target,
	})
	if err != nil {
		var refErr *secretref.Error
		if errors.As(err, &refErr) && refErr.Kind == secretref.KindBackendUnavailable {
			t.Skipf("wincred backend unavailable: %v", err)
		}
		t.Fatalf("Resolve: %v", err)
	}
	if got != secret {
		t.Fatalf("Resolve: got %q want %q", got, secret)
	}
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
