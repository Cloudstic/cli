//go:build linux

package engine

import (
	"os"
	"strconv"
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

func unescapeMountField(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}

	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+3 >= len(s) {
			out = append(out, s[i])
			continue
		}
		if !isOctalDigit(s[i+1]) || !isOctalDigit(s[i+2]) || !isOctalDigit(s[i+3]) {
			out = append(out, s[i])
			continue
		}
		v, err := strconv.ParseUint(s[i+1:i+4], 8, 8)
		if err != nil {
			out = append(out, s[i])
			continue
		}
		out = append(out, byte(v))
		i += 3
	}
	return string(out)
}

func isOctalDigit(b byte) bool {
	return b >= '0' && b <= '7'
}
