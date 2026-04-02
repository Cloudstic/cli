//go:build darwin

package source

import "testing"

func TestParseDarwinMountOutput(t *testing.T) {
	out := `/dev/disk3s1 on / (apfs, local, read-only, journaled)
/dev/disk4s1 on /Volumes/Photos (apfs, local, journaled)
/dev/disk5s1 on /Volumes/Archive Drive (exfat, local, nodev, nosuid)
/dev/disk3s6 on /System/Volumes/VM (apfs, local, noexec)
`

	got := parseDarwinMountOutput(out)
	if len(got) != 3 {
		t.Fatalf("len=%d want 3 (%v)", len(got), got)
	}
	if got[0].mountPoint != "/" || got[0].portable {
		t.Fatalf("root candidate = %+v", got[0])
	}
	if got[1].mountPoint != "/Volumes/Photos" || !got[1].portable {
		t.Fatalf("photos candidate = %+v", got[1])
	}
	if got[2].mountPoint != "/Volumes/Archive Drive" || !got[2].portable {
		t.Fatalf("archive candidate = %+v", got[2])
	}
}
