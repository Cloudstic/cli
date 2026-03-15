//go:build darwin

package main

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestSaveSecretToNativeStore_Success(t *testing.T) {
	orig := execCommandContext
	defer func() { execCommandContext = orig }()

	var gotName string
	var gotArgs []string
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string{}, args...)
		return exec.CommandContext(ctx, "sh", "-c", "exit 0")
	}

	err := saveSecretToNativeStore(context.Background(), "cloudstic/store/prod", "password", "super-secret")
	if err != nil {
		t.Fatalf("saveSecretToNativeStore: %v", err)
	}
	if gotName != "security" {
		t.Fatalf("command name=%q want security", gotName)
	}
	wantArgs := []string{"add-generic-password", "-U", "-s", "cloudstic/store/prod", "-a", "password", "-w", "super-secret"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("command args=%v want=%v", gotArgs, wantArgs)
	}
}

func TestSaveSecretToNativeStore_Failure(t *testing.T) {
	orig := execCommandContext
	defer func() { execCommandContext = orig }()

	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo keychain failed 1>&2; exit 1")
	}

	err := saveSecretToNativeStore(context.Background(), "svc", "acct", "secret")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "save secret in macOS keychain failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "keychain failed") {
		t.Fatalf("expected stderr in error: %v", err)
	}
}

func TestNativeSecretExists_Success(t *testing.T) {
	orig := execCommandContext
	defer func() { execCommandContext = orig }()

	var gotName string
	var gotArgs []string
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string{}, args...)
		return exec.CommandContext(ctx, "sh", "-c", "exit 0")
	}

	exists, err := nativeSecretExists(context.Background(), "cloudstic/store/prod", "password")
	if err != nil {
		t.Fatalf("nativeSecretExists: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true")
	}
	if gotName != "security" {
		t.Fatalf("command name=%q want security", gotName)
	}
	wantArgs := []string{"find-generic-password", "-s", "cloudstic/store/prod", "-a", "password", "-w"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("command args=%v want=%v", gotArgs, wantArgs)
	}
}

func TestNativeSecretExists_NotFound(t *testing.T) {
	orig := execCommandContext
	defer func() { execCommandContext = orig }()

	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "exit 1")
	}

	exists, err := nativeSecretExists(context.Background(), "svc", "acct")
	if err != nil {
		t.Fatalf("nativeSecretExists: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false")
	}
}
