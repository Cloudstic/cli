package main

import (
	"path/filepath"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"cloudstic": main,
	})
}

func TestCLI(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir:                 "testdata/scripts",
		RequireExplicitExec: true,
		UpdateScripts:       *updateGolden,
		Setup: func(env *testscript.Env) error {
			env.Vars = append(env.Vars,
				"CLOUDSTIC_CONFIG_DIR="+filepath.Join(env.WorkDir, "config"),
				// The object cache defaults to os.UserCacheDir(), so without
				// this a script would write into the developer's own cache
				// directory and, since entries outlive the run, could be
				// served an object from a previous script's repository.
				"CLOUDSTIC_OBJECT_CACHE_DIR="+filepath.Join(env.WorkDir, "objectcache"),
				"CLOUDSTIC_PASSWORD=",
				"CLOUDSTIC_ENCRYPTION_KEY=",
				"CLOUDSTIC_RECOVERY_KEY=",
				"CLOUDSTIC_PROFILE=",
				"CLOUDSTIC_STORE=",
				"CLOUDSTIC_SOURCE=",
			)
			return nil
		},
	})
}
