package e2e

import (
	"os/exec"
	"testing"
)

type capability string

const (
	capXattrs capability = "xattrs"
)

type sourceFactory struct {
	name string
	env  TestEnv
	caps map[capability]bool
	new  func(t *testing.T) TestSource
}

type storeFactory struct {
	name string
	env  TestEnv
	new  func(t *testing.T) TestStore
}

type matrixEntry struct {
	source sourceFactory
	store  storeFactory
}

type featureSpec struct {
	name         string
	sourceFilter func(sourceFactory) bool
	storeFilter  func(storeFactory) bool
	test         func(t *testing.T, h *harness, entry matrixEntry)
}

func shouldRun(e TestEnv) bool {
	mode := currentE2EMode()
	if mode == "all" {
		return true
	}
	return mode == string(e)
}

func allSourceFactories() []sourceFactory {
	sources := []sourceFactory{
		{
			name: "local",
			env:  Hermetic,
			caps: map[capability]bool{capXattrs: true},
			new: func(t *testing.T) TestSource {
				return newLocalSource(t)
			},
		},
	}
	sources = append(sources, portableDriveSources()...)
	if dockerAvailable() {
		sources = append(sources, sourceFactory{
			name: "sftp",
			env:  Hermetic,
			new: func(t *testing.T) TestSource {
				return newSFTPTestSource(t)
			},
		})
	}
	return sources
}

func allStoreFactories() []storeFactory {
	stores := []storeFactory{
		{
			name: "local",
			env:  Hermetic,
			new: func(t *testing.T) TestStore {
				return newLocalStore(t)
			},
		},
	}
	if dockerAvailable() {
		stores = append(stores,
			storeFactory{
				name: "minio",
				env:  Hermetic,
				new: func(t *testing.T) TestStore {
					return newMinIOTestStore(t)
				},
			},
			storeFactory{
				name: "sftp",
				env:  Hermetic,
				new: func(t *testing.T) TestStore {
					return newSFTPTestStore(t)
				},
			},
		)
	}
	return stores
}

func dockerAvailable() bool {
	cmd := exec.Command("docker", "info")
	return cmd.Run() == nil
}

func runFeatureMatrix(t *testing.T, spec featureSpec) {
	t.Helper()

	bin := buildBinary(t)
	for _, source := range allSourceFactories() {
		if !shouldRun(source.env) {
			continue
		}
		if spec.sourceFilter != nil && !spec.sourceFilter(source) {
			continue
		}
		for _, store := range allStoreFactories() {
			if !shouldRun(store.env) {
				continue
			}
			if spec.storeFilter != nil && !spec.storeFilter(store) {
				continue
			}

			entry := matrixEntry{source: source, store: store}
			t.Run(spec.name+"/"+source.name+"_to_"+store.name, func(t *testing.T) {
				t.Parallel()
				h := newHarness(t, bin, source.new(t), store.new(t))
				spec.test(t, h, entry)
			})
		}
	}
}

func localOnlySource(source sourceFactory) bool {
	return source.name == "local"
}

func localOnlyStore(store storeFactory) bool {
	return store.name == "local"
}
