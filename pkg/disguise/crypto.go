package disguise

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

const (
	MagicV2  = "GPIX_DISGUISE_V2"
	saltLen  = 16
	nonceLen = 12
	tagLen   = 16
	keyLen   = 32

	argonTime    = 1
	argonMemory  = 16 * 1024
	argonThreads = 1
)

var (
	ErrEncrypted       = errors.New("disguise: file is encrypted, passphrase required")
	ErrWrongPassphrase = errors.New("disguise: wrong passphrase")
	ErrEmptyPassphrase = errors.New("disguise: passphrase is empty")
)

const magicAnyPrefix = "GPIX_DISGUISE_V"

func deriveKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, keyLen)
}

func encryptPayload(name string, payload []byte, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, ErrEmptyPassphrase
	}

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("disguise: salt: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("disguise: nonce: %w", err)
	}

	key := deriveKey(passphrase, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("disguise: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("disguise: gcm: %w", err)
	}

	aad := buildAAD(name, int64(len(payload)))
	ct := gcm.Seal(nil, nonce, payload, aad)

	nameBytes := []byte(name)
	out := make([]byte, 0, MagicLen+4+len(nameBytes)+saltLen+nonceLen+8+len(ct))
	out = append(out, []byte(MagicV2)...)
	out = appendU32(out, uint32(len(nameBytes)))
	out = append(out, nameBytes...)
	out = append(out, salt...)
	out = append(out, nonce...)
	out = appendU64(out, uint64(len(payload)))
	out = append(out, ct...)
	return out, nil
}

func decryptPayload(buf []byte, passphrase string) (Header, []byte, error) {
	if passphrase == "" {
		return Header{}, nil, ErrEmptyPassphrase
	}
	if len(buf) < MagicLen+4 {
		return Header{}, nil, ErrBadHeader
	}
	if string(buf[:MagicLen]) != MagicV2 {
		return Header{}, nil, ErrBadHeader
	}
	rest := buf[MagicLen:]
	nameLen := readU32(rest[:4])
	rest = rest[4:]
	if nameLen > maxName || len(rest) < int(nameLen)+saltLen+nonceLen+8+tagLen {
		return Header{}, nil, ErrBadHeader
	}
	name := string(rest[:nameLen])
	rest = rest[nameLen:]
	salt := rest[:saltLen]
	rest = rest[saltLen:]
	nonce := rest[:nonceLen]
	rest = rest[nonceLen:]
	plainSize := readU64(rest[:8])
	rest = rest[8:]
	ct := rest
	if uint64(len(ct)) < plainSize+tagLen {
		return Header{}, nil, ErrBadHeader
	}
	ct = ct[:plainSize+tagLen]

	key := deriveKey(passphrase, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return Header{}, nil, fmt.Errorf("disguise: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Header{}, nil, fmt.Errorf("disguise: gcm: %w", err)
	}

	aad := buildAAD(name, int64(plainSize))
	pt, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return Header{}, nil, ErrWrongPassphrase
	}
	return Header{Filename: name, PayloadSize: int64(len(pt))}, pt, nil
}

func buildAAD(name string, plainSize int64) []byte {
	aad := make([]byte, 0, len(name)+8)
	aad = append(aad, []byte(name)...)
	aad = appendU64(aad, uint64(plainSize))
	return aad
}

func appendU32(b []byte, v uint32) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}
func appendU64(b []byte, v uint64) []byte {
	return append(b,
		byte(v), byte(v>>8), byte(v>>16), byte(v>>24),
		byte(v>>32), byte(v>>40), byte(v>>48), byte(v>>56))
}
func readU32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
func readU64(b []byte) uint64 {
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}

func peekVersion(head []byte) (int, int) {
	idx := bytesIndexString(head, magicAnyPrefix)
	if idx < 0 || idx+MagicLen > len(head) {
		return 0, -1
	}
	switch head[idx+MagicLen-1] {
	case '1':
		return 1, idx
	case '2':
		return 2, idx
	}
	return 0, -1
}

func bytesIndexString(b []byte, s string) int {
	for i := 0; i+len(s) <= len(b); i++ {
		match := true
		for j := 0; j < len(s); j++ {
			if b[i+j] != s[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

const maxEncrypt = 1 << 30

func readAllCapped(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxEncrypt+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxEncrypt {
		return nil, fmt.Errorf("disguise: payload exceeds %d-byte encryption cap", maxEncrypt)
	}
	return b, nil
}
