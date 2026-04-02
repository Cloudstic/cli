//go:build darwin

package source

import (
	"os/exec"
	"strings"
)

func discoverLocalCandidates() ([]discoverCandidate, error) {
	out, err := exec.Command("mount").Output()
	if err != nil {
		return nil, err
	}
	return parseDarwinMountOutput(string(out)), nil
}

func parseDarwinMountOutput(out string) []discoverCandidate {
	var candidates []discoverCandidate
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		onIdx := strings.Index(line, " on ")
		if onIdx < 0 {
			continue
		}
		rest := line[onIdx+4:]
		openIdx := strings.Index(rest, " (")
		if openIdx < 0 {
			continue
		}
		mountPoint := strings.TrimSpace(rest[:openIdx])
		switch {
		case mountPoint == "/":
			candidates = append(candidates, discoverCandidate{mountPoint: mountPoint})
		case strings.HasPrefix(mountPoint, "/Volumes/"):
			candidates = append(candidates, discoverCandidate{mountPoint: mountPoint, portable: true})
		}
	}
	return candidates
}
