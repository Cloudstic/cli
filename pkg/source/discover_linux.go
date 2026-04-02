//go:build linux

package source

import (
	"os"
	"strings"
)

func discoverLocalCandidates() ([]discoverCandidate, error) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil, err
	}
	return parseLinuxMounts(string(data)), nil
}

func parseLinuxMounts(data string) []discoverCandidate {
	var candidates []discoverCandidate
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		device := fields[0]
		mountPoint := unescapeMountField(fields[1])
		if !strings.HasPrefix(device, "/dev/") {
			continue
		}
		switch {
		case mountPoint == "/":
			candidates = append(candidates, discoverCandidate{mountPoint: mountPoint})
		case strings.HasPrefix(mountPoint, "/media/"),
			strings.HasPrefix(mountPoint, "/run/media/"),
			strings.HasPrefix(mountPoint, "/mnt/"):
			candidates = append(candidates, discoverCandidate{mountPoint: mountPoint, portable: true})
		}
	}
	return candidates
}
