//go:build !crashinject

package main

import "github.com/cloudstic/cli/pkg/store"

// withCrashInjection is a no-op in the default build. See crashinject.go,
// which is compiled in only under the "crashinject" build tag.
func withCrashInjection(s store.ObjectStore) (store.ObjectStore, error) {
	return s, nil
}
