package main

import (
	"context"
	"io"

	cloudstic "github.com/cloudstic/cli"
	"github.com/cloudstic/cli/pkg/source"
)

// stubClient is a test double that implements cloudsticClient.
// Each field pair holds the value to return and an optional error.
type stubClient struct {
	backupResult    *cloudstic.BackupResult
	backupErr       error
	discoverResult  []cloudstic.DiscoveredSource
	discoverErr     error
	restoreResult   *cloudstic.RestoreResult
	restoreErr      error
	listResult      *cloudstic.ListResult
	listErr         error
	lsResult        *cloudstic.LsSnapshotResult
	lsErr           error
	pruneResult     *cloudstic.PruneResult
	pruneErr        error
	forgetResult    *cloudstic.ForgetResult
	forgetErr       error
	policyResult    *cloudstic.PolicyResult
	policyErr       error
	diffResult      *cloudstic.DiffResult
	diffErr         error
	checkResult     *cloudstic.CheckResult
	checkErr        error
	catResults      []*cloudstic.CatResult
	catErr          error
	breakLockResult []*cloudstic.RepoLock
	breakLockErr    error
}

func (s *stubClient) Backup(_ context.Context, _ source.Source, _ ...cloudstic.BackupOption) (*cloudstic.BackupResult, error) {
	return s.backupResult, s.backupErr
}

func (s *stubClient) DiscoverSources(_ context.Context) ([]cloudstic.DiscoveredSource, error) {
	return s.discoverResult, s.discoverErr
}

func (s *stubClient) Restore(_ context.Context, _ io.Writer, _ string, _ ...cloudstic.RestoreOption) (*cloudstic.RestoreResult, error) {
	return s.restoreResult, s.restoreErr
}

func (s *stubClient) RestoreToDir(_ context.Context, _, _ string, _ ...cloudstic.RestoreOption) (*cloudstic.RestoreResult, error) {
	return s.restoreResult, s.restoreErr
}

func (s *stubClient) List(_ context.Context, _ ...cloudstic.ListOption) (*cloudstic.ListResult, error) {
	return s.listResult, s.listErr
}

func (s *stubClient) LsSnapshot(_ context.Context, _ string, _ ...cloudstic.LsSnapshotOption) (*cloudstic.LsSnapshotResult, error) {
	return s.lsResult, s.lsErr
}

func (s *stubClient) Prune(_ context.Context, _ ...cloudstic.PruneOption) (*cloudstic.PruneResult, error) {
	return s.pruneResult, s.pruneErr
}

func (s *stubClient) Forget(_ context.Context, _ string, _ ...cloudstic.ForgetOption) (*cloudstic.ForgetResult, error) {
	return s.forgetResult, s.forgetErr
}

func (s *stubClient) ForgetPolicy(_ context.Context, _ ...cloudstic.ForgetOption) (*cloudstic.PolicyResult, error) {
	return s.policyResult, s.policyErr
}

func (s *stubClient) Diff(_ context.Context, _, _ string, _ ...cloudstic.DiffOption) (*cloudstic.DiffResult, error) {
	return s.diffResult, s.diffErr
}

func (s *stubClient) Check(_ context.Context, _ ...cloudstic.CheckOption) (*cloudstic.CheckResult, error) {
	return s.checkResult, s.checkErr
}

func (s *stubClient) Cat(_ context.Context, _ ...string) ([]*cloudstic.CatResult, error) {
	return s.catResults, s.catErr
}

func (s *stubClient) BreakLock(_ context.Context) ([]*cloudstic.RepoLock, error) {
	return s.breakLockResult, s.breakLockErr
}
