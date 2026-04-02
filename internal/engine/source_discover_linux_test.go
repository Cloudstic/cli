//go:build linux

package engine

import "testing"

func TestParseLinuxMounts(t *testing.T) {
	data := `/dev/nvme0n1p2 / ext4 rw,relatime 0 0
tmpfs /run tmpfs rw,nosuid,nodev 0 0
/dev/sdb1 /media/loic/Photos\040SSD exfat rw,nosuid,nodev 0 0
/dev/sdc1 /mnt/archive ext4 rw,relatime 0 0
/dev/sdd1 /run/media/loic/USB vfat rw,nosuid,nodev 0 0
`

	got := parseLinuxMounts(data)
	if len(got) != 4 {
		t.Fatalf("len=%d want 4 (%v)", len(got), got)
	}
	if got[0].mountPoint != "/" || got[0].portable {
		t.Fatalf("root candidate = %+v", got[0])
	}
	if got[1].mountPoint != "/media/loic/Photos SSD" || !got[1].portable {
		t.Fatalf("media candidate = %+v", got[1])
	}
	if got[2].mountPoint != "/mnt/archive" || !got[2].portable {
		t.Fatalf("mnt candidate = %+v", got[2])
	}
	if got[3].mountPoint != "/run/media/loic/USB" || !got[3].portable {
		t.Fatalf("run-media candidate = %+v", got[3])
	}
}

func TestUnescapeMountField(t *testing.T) {
	if got := unescapeMountField(`/media/loic/Photos\040SSD`); got != "/media/loic/Photos SSD" {
		t.Fatalf("got %q", got)
	}
	if got := unescapeMountField(`/plain/path`); got != "/plain/path" {
		t.Fatalf("got %q", got)
	}
	if got := unescapeMountField(`/bad\99path`); got != `/bad\99path` {
		t.Fatalf("got %q", got)
	}
}
