package mediacrypt

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"
)

func mustKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, keySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func roundTrip(t *testing.T, key []byte, plaintext []byte, name string) {
	t.Helper()
	var enc bytes.Buffer
	if err := Encrypt(&enc, bytes.NewReader(plaintext), key, int64(len(plaintext)), name); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !HasMagic(enc.Bytes()) {
		t.Fatal("output missing magic")
	}
	if got := EncryptedSize(int64(len(plaintext)), name); got != int64(enc.Len()) {
		t.Fatalf("EncryptedSize=%d, actual=%d", got, enc.Len())
	}

	var dec bytes.Buffer
	hdr, err := Decrypt(&dec, bytes.NewReader(enc.Bytes()), key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if hdr.Name != name {
		t.Fatalf("name mismatch: %q != %q", hdr.Name, name)
	}
	if hdr.OrigSize != int64(len(plaintext)) {
		t.Fatalf("size mismatch: %d != %d", hdr.OrigSize, len(plaintext))
	}
	if !bytes.Equal(dec.Bytes(), plaintext) {
		t.Fatalf("plaintext mismatch (len %d vs %d)", dec.Len(), len(plaintext))
	}
}

func TestRoundTripSizes(t *testing.T) {
	key := mustKey(t)
	sizes := []int{
		0, 1, 16, 100,
		defaultChunk - 1, defaultChunk, defaultChunk + 1,
		3*defaultChunk + 123,
	}
	for _, n := range sizes {
		pt := make([]byte, n)
		_, _ = rand.Read(pt)
		roundTrip(t, key, pt, "photo.jpg")
	}
}

func TestDecryptingReader(t *testing.T) {
	key := mustKey(t)
	pt := make([]byte, 5*defaultChunk+7)
	_, _ = rand.Read(pt)

	var enc bytes.Buffer
	if err := Encrypt(&enc, bytes.NewReader(pt), key, int64(len(pt)), "clip.mp4"); err != nil {
		t.Fatal(err)
	}
	hdr, rc, err := DecryptingReader(bytes.NewReader(enc.Bytes()), key)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if hdr.Name != "clip.mp4" {
		t.Fatalf("name: %q", hdr.Name)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatal("streamed plaintext mismatch")
	}
}

func TestWrongKeyFails(t *testing.T) {
	key := mustKey(t)
	other := mustKey(t)
	pt := []byte("secret pixels")
	var enc bytes.Buffer
	if err := Encrypt(&enc, bytes.NewReader(pt), key, int64(len(pt)), "x.png"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := Decrypt(&out, bytes.NewReader(enc.Bytes()), other); err == nil {
		t.Fatal("expected decryption with wrong key to fail")
	}
}

func TestTamperDetected(t *testing.T) {
	key := mustKey(t)
	pt := make([]byte, 2*defaultChunk)
	_, _ = rand.Read(pt)
	var enc bytes.Buffer
	if err := Encrypt(&enc, bytes.NewReader(pt), key, int64(len(pt)), "a.bin"); err != nil {
		t.Fatal(err)
	}
	b := enc.Bytes()
	// flip a byte in the ciphertext body (well past the header)
	b[len(b)-20] ^= 0x40
	var out bytes.Buffer
	if _, err := Decrypt(&out, bytes.NewReader(b), key); err == nil {
		t.Fatal("expected tamper to be detected")
	}
}

func TestTruncationDetected(t *testing.T) {
	key := mustKey(t)
	pt := make([]byte, 3*defaultChunk)
	_, _ = rand.Read(pt)
	var enc bytes.Buffer
	if err := Encrypt(&enc, bytes.NewReader(pt), key, int64(len(pt)), "v.dat"); err != nil {
		t.Fatal(err)
	}
	// drop the final chunk entirely
	b := enc.Bytes()[:enc.Len()-(defaultChunk+tagSize)]
	var out bytes.Buffer
	if _, err := Decrypt(&out, bytes.NewReader(b), key); err == nil {
		t.Fatal("expected truncation to be detected")
	}
}

func TestNotEncrypted(t *testing.T) {
	key := mustKey(t)
	var out bytes.Buffer
	_, err := Decrypt(&out, bytes.NewReader([]byte("just some plain bytes, not encrypted")), key)
	if err != ErrNotEncrypted {
		t.Fatalf("expected ErrNotEncrypted, got %v", err)
	}
}
