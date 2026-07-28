//go:build !darwin && !linux && !windows

package workstation

func discoverLocalCandidates() ([]discoverCandidate, error) {
	return nil, nil
}
