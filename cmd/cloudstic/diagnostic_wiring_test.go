package main

import "testing"

// TestDiagnosticWiring pins which flag turns on which channel.
//
// -verbose and -debug share one writer so their lines interleave cleanly above
// the progress bar, which makes it easy to accidentally wire them to the same
// switch. They must not be: -debug wraps the store in a decorator that logs
// every operation, and asking for progress detail must not drag that in.
//
// This caught a real regression. When per-operation verbose options were
// replaced by a reporter detail level, the query operations (list, ls, diff,
// find) had no reporter to report through, so their detail moved to the
// component logger — which was only wired when -debug was set. `list -verbose`
// silently printed nothing.
func TestDiagnosticWiring(t *testing.T) {
	cases := []struct {
		name             string
		cfg              clientConfig
		wantDiagnostics  bool
		wantStoreTracing bool
	}{
		{"neither", clientConfig{}, false, false},
		{"verbose only", clientConfig{Verbose: true}, true, false},
		{"debug only", clientConfig{Store: storeConfig{Debug: true}}, true, true},
		{"both", clientConfig{Verbose: true, Store: storeConfig{Debug: true}}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diagnostics := newDebugLog(tc.cfg.Store.Debug || tc.cfg.Verbose)
			if got := diagnostics != nil; got != tc.wantDiagnostics {
				t.Errorf("diagnostics writer = %v, want %v: verbose detail from the "+
					"query operations goes here and is lost without it", got, tc.wantDiagnostics)
			}
			tracing := tc.cfg.Store.Debug
			if tracing != tc.wantStoreTracing {
				t.Errorf("store tracing = %v, want %v", tracing, tc.wantStoreTracing)
			}
			if tc.cfg.Verbose && !tc.cfg.Store.Debug && tracing {
				t.Error("-verbose must not enable per-operation store tracing")
			}
		})
	}
}
