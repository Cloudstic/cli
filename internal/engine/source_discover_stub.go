//go:build !darwin && !linux && !windows

package engine

func discoverLocalCandidates() ([]discoverCandidate, error) {
	return nil, nil
}
