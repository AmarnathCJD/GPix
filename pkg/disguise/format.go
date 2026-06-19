package disguise

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	Magic    = "GPIX_DISGUISE_V1"
	MagicLen = 16
	maxScan  = 64 * 1024
	maxName  = 4096
)

var (
	ErrNotDisguised = errors.New("disguise: file is not disguised")
	ErrBadHeader    = errors.New("disguise: header is corrupt")
)

type Header struct {
	Filename    string
	PayloadSize int64
}

func Wrap(name string, payload io.Reader, payloadSize int64) (io.Reader, int64) {
	nameBytes := []byte(name)
	hdr := bytes.NewBuffer(make([]byte, 0, MagicLen+4+len(nameBytes)+8))
	hdr.WriteString(Magic)
	_ = binary.Write(hdr, binary.LittleEndian, uint32(len(nameBytes)))
	hdr.Write(nameBytes)
	_ = binary.Write(hdr, binary.LittleEndian, uint64(payloadSize))

	total := int64(len(wrapperMP4)) + int64(hdr.Len()) + payloadSize
	r := io.MultiReader(
		bytes.NewReader(wrapperMP4),
		bytes.NewReader(hdr.Bytes()),
		payload,
	)
	return r, total
}

func WrapEncrypted(name string, payload io.Reader, passphrase string) (io.Reader, int64, error) {
	pt, err := readAllCapped(payload)
	if err != nil {
		return nil, 0, err
	}
	enc, err := encryptPayload(name, pt, passphrase)
	if err != nil {
		return nil, 0, err
	}
	total := int64(len(wrapperMP4)) + int64(len(enc))
	r := io.MultiReader(
		bytes.NewReader(wrapperMP4),
		bytes.NewReader(enc),
	)
	return r, total, nil
}

func LooksDisguised(head []byte) bool {
	v, _ := peekVersion(head)
	return v > 0
}

func LooksEncrypted(head []byte) bool {
	v, _ := peekVersion(head)
	return v == 2
}

func ParseHeader(buf []byte) (Header, int, error) {
	idx := bytes.Index(buf, []byte(Magic))
	if idx < 0 {
		return Header{}, 0, ErrNotDisguised
	}
	rest := buf[idx+MagicLen:]
	if len(rest) < 4 {
		return Header{}, 0, ErrBadHeader
	}
	nameLen := binary.LittleEndian.Uint32(rest[:4])
	if nameLen > maxName {
		return Header{}, 0, ErrBadHeader
	}
	if len(rest) < int(4+nameLen+8) {
		return Header{}, 0, ErrBadHeader
	}
	name := string(rest[4 : 4+nameLen])
	payloadSize := binary.LittleEndian.Uint64(rest[4+nameLen : 4+nameLen+8])
	headerEnd := idx + MagicLen + 4 + int(nameLen) + 8
	return Header{Filename: name, PayloadSize: int64(payloadSize)}, headerEnd, nil
}

func Extract(r io.Reader) (Header, io.Reader, error) {
	br := bufio.NewReaderSize(r, maxScan)
	head, err := br.Peek(maxScan)
	if err != nil && err != io.EOF {
		return Header{}, nil, fmt.Errorf("disguise: peek: %w", err)
	}
	v, _ := peekVersion(head)
	if v == 2 {
		return Header{}, nil, ErrEncrypted
	}
	hdr, headerEnd, err := ParseHeader(head)
	if err != nil {
		return Header{}, nil, err
	}
	if _, err := br.Discard(headerEnd); err != nil {
		return Header{}, nil, fmt.Errorf("disguise: discard wrapper: %w", err)
	}
	return hdr, io.LimitReader(br, hdr.PayloadSize), nil
}

func ExtractWithPassphrase(r io.Reader, passphrase string) (Header, io.Reader, error) {
	full, err := io.ReadAll(r)
	if err != nil {
		return Header{}, nil, err
	}
	v, idx := peekVersion(full)
	if v == 0 {
		return Header{}, nil, ErrNotDisguised
	}
	if v == 1 {
		hdr, headerEnd, err := ParseHeader(full)
		if err != nil {
			return Header{}, nil, err
		}
		end := min(int64(headerEnd)+hdr.PayloadSize, int64(len(full)))
		return hdr, bytes.NewReader(full[headerEnd:end]), nil
	}
	hdr, plaintext, err := decryptPayload(full[idx:], passphrase)
	if err != nil {
		return Header{}, nil, err
	}
	return hdr, bytes.NewReader(plaintext), nil
}

func SniffStream(r io.Reader) (bool, []byte, io.Reader, error) {
	br := bufio.NewReaderSize(r, maxScan)
	head, err := br.Peek(maxScan)
	if err != nil && err != io.EOF {
		return false, nil, nil, fmt.Errorf("disguise: peek: %w", err)
	}
	isDisguised := LooksDisguised(head)
	headCopy := make([]byte, len(head))
	copy(headCopy, head)
	return isDisguised, headCopy, br, nil
}
