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
