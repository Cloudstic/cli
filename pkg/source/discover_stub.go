//go:build !darwin && !linux && !windows

package source

func discoverLocalCandidates() ([]discoverCandidate, error) {
	return nil, nil
}
