package crypto

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

func newTestSealer(t *testing.T, master string) *MemberSealer {
	t.Helper()
	key := sha256.Sum256([]byte(master))
	s, err := NewMemberSealer(key[:])
	if err != nil {
		t.Fatalf("NewMemberSealer: %v", err)
	}
	return s
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestMemberSealRoundTrip(t *testing.T) {
	s := newTestSealer(t, "master")
	body := []byte("the contents of one small file")
	blobRef := []byte("blob/" + hashOf([]byte("some blob")))

	sealed, err := s.SealMember(body, hashOf(body), blobRef)
	if err != nil {
		t.Fatalf("SealMember: %v", err)
	}
	if bytes.Contains(sealed, body) {
		t.Fatal("the plaintext is visible in the sealed member")
	}
	got, err := s.OpenMember(sealed, hashOf(body), blobRef)
	if err != nil {
		t.Fatalf("OpenMember: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("round trip returned %q, want %q", got, body)
	}
}

// An empty member is a real case: a zero-byte file. GCM handles it, but the
// framing has to as well.
func TestMemberSealEmptyBody(t *testing.T) {
	s := newTestSealer(t, "master")
	sealed, err := s.SealMember(nil, hashOf(nil), []byte("blob/x"))
	if err != nil {
		t.Fatalf("SealMember: %v", err)
	}
	if len(sealed) != MemberOverhead {
		t.Fatalf("empty member sealed to %d bytes, want %d", len(sealed), MemberOverhead)
	}
	got, err := s.OpenMember(sealed, hashOf(nil), []byte("blob/x"))
	if err != nil {
		t.Fatalf("OpenMember: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("opened %d bytes, want 0", len(got))
	}
}

// Determinism is what makes a retried upload free: the same body sealed for
// the same blob must produce byte-identical output, which a random nonce
// cannot offer a content-addressed store.
func TestMemberSealIsDeterministic(t *testing.T) {
	s := newTestSealer(t, "master")
	body := []byte("retried")
	ref := []byte("blob/abc")

	first, err := s.SealMember(body, hashOf(body), ref)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.SealMember(body, hashOf(body), ref)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("sealing the same member twice produced different bytes")
	}

	// A second sealer built from the same master key must agree, or the
	// determinism is per-process and worth nothing.
	if again, err := newTestSealer(t, "master").SealMember(body, hashOf(body), ref); err != nil {
		t.Fatal(err)
	} else if !bytes.Equal(first, again) {
		t.Fatal("a fresh sealer with the same master key produced different bytes")
	}
}

// The property the whole design leans on. A blob's ref is never verified
// against its bytes — no reader assembles a blob's plaintext — so this AAD
// binding is the only thing standing between a reader and a member substituted
// from elsewhere. FileMeta.ContentHash is a second check, but restore
// -no-verify skips it, so this one must hold on its own.
func TestMemberSealedForOneBlobCannotOpenUnderAnother(t *testing.T) {
	s := newTestSealer(t, "master")
	body := []byte("a body that lives in exactly one blob")
	h := hashOf(body)

	sealed, err := s.SealMember(body, h, []byte("blob/aaa"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenMember(sealed, h, []byte("blob/bbb")); !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("a member opened under a different blob ref: err = %v", err)
	}
}

// A member moved to another offset keeps its bytes, so nothing about the
// ciphertext changes — what catches it is that the entry naming that offset
// carries a different content hash.
func TestMemberDoesNotOpenUnderAnotherContentHash(t *testing.T) {
	s := newTestSealer(t, "master")
	body := []byte("body one")
	ref := []byte("blob/aaa")

	sealed, err := s.SealMember(body, hashOf(body), ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenMember(sealed, hashOf([]byte("body two")), ref); !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("a member opened under a different content hash: err = %v", err)
	}
}

func TestMemberDoesNotOpenUnderAnotherMasterKey(t *testing.T) {
	body := []byte("secret")
	ref := []byte("blob/aaa")

	sealed, err := newTestSealer(t, "master one").SealMember(body, hashOf(body), ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newTestSealer(t, "master two").OpenMember(sealed, hashOf(body), ref); !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("a member opened under a different master key: err = %v", err)
	}
}

func TestMemberTamperingIsDetected(t *testing.T) {
	s := newTestSealer(t, "master")
	body := bytes.Repeat([]byte("tamper "), 20)
	h, ref := hashOf(body), []byte("blob/aaa")

	sealed, err := s.SealMember(body, h, ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{1, len(sealed) / 2, len(sealed) - 1} {
		bad := bytes.Clone(sealed)
		bad[i] ^= 0x01
		if _, err := s.OpenMember(bad, h, ref); !errors.Is(err, ErrDecryptFailed) {
			t.Errorf("flipping byte %d was not detected: err = %v", i, err)
		}
	}
}

// The two framings differ — a member carries no nonce — so each opener must
// refuse the other's output rather than reading a ciphertext byte as a nonce.
func TestMemberFramingIsNotInterchangeableWithEncrypt(t *testing.T) {
	key := sha256.Sum256([]byte("master"))
	s := newTestSealer(t, "master")
	body := []byte("which framing is this")
	h, ref := hashOf(body), []byte("blob/aaa")

	sealed, err := s.SealMember(body, h, ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(sealed, key[:]); err == nil {
		t.Error("Decrypt accepted a sealed member")
	}
	if IsEncrypted(sealed) {
		t.Error("IsEncrypted reported a sealed member as an Encrypt ciphertext")
	}

	ct, err := Encrypt(body, key[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenMember(ct, h, ref); !errors.Is(err, ErrInvalidMember) {
		t.Errorf("OpenMember accepted an Encrypt ciphertext: err = %v", err)
	}
}

// The derivation mixes two variable-length strings. Without length prefixes,
// ("ab", "cd") and ("abc", "d") would concatenate identically and derive the
// same key — letting a member sealed for one blob open under another whose ref
// merely shares a boundary.
func TestMemberDerivationResistsBoundaryConfusion(t *testing.T) {
	s := newTestSealer(t, "master")
	body := []byte("boundary")

	sealed, err := s.SealMember(body, "ab", []byte("cd"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenMember(sealed, "abc", []byte("d")); !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("a shifted hash/AAD boundary derived the same key: err = %v", err)
	}
}

func TestMemberSealerRejectsEmptyMasterKey(t *testing.T) {
	if _, err := NewMemberSealer(nil); err == nil {
		t.Fatal("NewMemberSealer accepted an empty master key")
	}
}

func TestOpenMemberRejectsShortInput(t *testing.T) {
	s := newTestSealer(t, "master")
	for _, n := range []int{0, 1, MemberOverhead - 1} {
		if _, err := s.OpenMember(make([]byte, n), "ab", []byte("cd")); !errors.Is(err, ErrInvalidMember) {
			t.Errorf("%d-byte input: err = %v, want ErrInvalidMember", n, err)
		}
	}
}

// Overhead is quoted in the format's sizing arithmetic, so it is part of the
// contract rather than an implementation detail.
func TestMemberOverheadIsWhatWeClaim(t *testing.T) {
	s := newTestSealer(t, "master")
	body := bytes.Repeat([]byte("x"), 1000)
	sealed, err := s.SealMember(body, hashOf(body), []byte("blob/aaa"))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(sealed) - len(body); got != MemberOverhead {
		t.Fatalf("overhead is %d bytes, want %d", got, MemberOverhead)
	}
	if MemberOverhead >= Overhead {
		t.Fatalf("member overhead %d is not below Encrypt's %d; the derived nonce bought nothing",
			MemberOverhead, Overhead)
	}
}

// The append form exists so a blob can be assembled without allocating each
// member and copying it in. What it writes therefore has to be exactly what
// SealMember would have written: those bytes are what a repository stores, and
// the ref naming the blob is fixed before any of them exist.
//
// The destinations below are the three shapes a caller presents. "spare" is
// the one the blob writer actually hits — a buffer sized for the whole blob,
// so the seal writes into capacity that is already there — and it is where an
// overlap between destination and plaintext would corrupt the output rather
// than announce itself.
func TestAppendSealMemberWritesWhatSealMemberWould(t *testing.T) {
	s := newTestSealer(t, "master")
	ref := []byte("blob/" + hashOf([]byte("a blob")))
	prefix := []byte("the members already packed")

	for _, body := range [][]byte{nil, []byte("one small file"), bytes.Repeat([]byte("body "), 1000)} {
		want, err := s.SealMember(body, hashOf(body), ref)
		if err != nil {
			t.Fatalf("SealMember: %v", err)
		}

		dsts := []struct {
			name string
			dst  []byte
		}{
			{"empty", nil},
			{"full", bytes.Clone(prefix)},
			{"spare", append(make([]byte, 0, len(prefix)+len(want)+64), prefix...)},
		}
		for _, d := range dsts {
			base := len(d.dst)
			head := bytes.Clone(d.dst)

			got, err := s.AppendSealMember(d.dst, body, hashOf(body), ref)
			if err != nil {
				t.Fatalf("%s/%d-byte body: AppendSealMember: %v", d.name, len(body), err)
			}
			if !bytes.Equal(got[:base], head) {
				t.Errorf("%s/%d-byte body: the destination's existing bytes were disturbed", d.name, len(body))
			}
			if !bytes.Equal(got[base:], want) {
				t.Errorf("%s/%d-byte body: appended %d bytes, want the %d SealMember produces",
					d.name, len(body), len(got)-base, len(want))
			}
			// And the appended run must open from exactly its own range, since
			// that is all a ranged read of the containing blob returns.
			opened, err := s.OpenMember(got[base:], hashOf(body), ref)
			if err != nil {
				t.Fatalf("%s/%d-byte body: OpenMember: %v", d.name, len(body), err)
			}
			if !bytes.Equal(opened, body) {
				t.Errorf("%s/%d-byte body: did not round-trip", d.name, len(body))
			}
		}
	}
}
