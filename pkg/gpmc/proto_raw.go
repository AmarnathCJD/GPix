package gpmc

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	wireVarint = 0
	wireFixed64 = 1
	wireBytes  = 2
	wireFixed32 = 5
)

func encVarint(buf []byte, x uint64) []byte {
	for x >= 0x80 {
		buf = append(buf, byte(x)|0x80)
		x >>= 7
	}
	return append(buf, byte(x))
}

func encTag(buf []byte, field, wire int) []byte {
	return encVarint(buf, uint64(field)<<3|uint64(wire))
}

func encInt(buf []byte, field int, v int64) []byte {
	buf = encTag(buf, field, wireVarint)
	return encVarint(buf, uint64(v))
}

func encString(buf []byte, field int, s string) []byte {
	buf = encTag(buf, field, wireBytes)
	buf = encVarint(buf, uint64(len(s)))
	return append(buf, s...)
}

func encBytes(buf []byte, field int, b []byte) []byte {
	buf = encTag(buf, field, wireBytes)
	buf = encVarint(buf, uint64(len(b)))
	return append(buf, b...)
}

func encSubMessage(buf []byte, field int, sub []byte) []byte {
	buf = encTag(buf, field, wireBytes)
	buf = encVarint(buf, uint64(len(sub)))
	return append(buf, sub...)
}

func decVarint(b []byte) (uint64, int, error) {
	var x uint64
	var s uint
	for i, c := range b {
		if i >= 10 {
			return 0, 0, errors.New("proto: varint overflow")
		}
		if c < 0x80 {
			return x | uint64(c)<<s, i + 1, nil
		}
		x |= uint64(c&0x7f) << s
		s += 7
	}
	return 0, 0, errors.New("proto: varint truncated")
}

type protoField struct {
	num  int
	wire int
	raw  []byte
	val  uint64
}

func walkMessage(buf []byte, fn func(f protoField) error) error {
	pos := 0
	for pos < len(buf) {
		tag, n, err := decVarint(buf[pos:])
		if err != nil {
			return fmt.Errorf("proto: tag at %d: %w", pos, err)
		}
		pos += n
		field := int(tag >> 3)
		wire := int(tag & 7)
		f := protoField{num: field, wire: wire}
		switch wire {
		case wireVarint:
			v, k, err := decVarint(buf[pos:])
			if err != nil {
				return fmt.Errorf("proto: varint at %d: %w", pos, err)
			}
			f.val = v
			pos += k
		case wireFixed64:
			if pos+8 > len(buf) {
				return errors.New("proto: fixed64 truncated")
			}
			f.val = binary.LittleEndian.Uint64(buf[pos : pos+8])
			pos += 8
		case wireBytes:
			ln, k, err := decVarint(buf[pos:])
			if err != nil {
				return fmt.Errorf("proto: length at %d: %w", pos, err)
			}
			pos += k
			if pos+int(ln) > len(buf) {
				return errors.New("proto: bytes truncated")
			}
			f.raw = buf[pos : pos+int(ln)]
			pos += int(ln)
		case wireFixed32:
			if pos+4 > len(buf) {
				return errors.New("proto: fixed32 truncated")
			}
			f.val = uint64(binary.LittleEndian.Uint32(buf[pos : pos+4]))
			pos += 4
		default:
			return fmt.Errorf("proto: unsupported wire type %d", wire)
		}
		if err := fn(f); err != nil {
			return err
		}
	}
	return nil
}

func findFieldBytes(buf []byte, field int) ([]byte, bool) {
	var out []byte
	found := false
	_ = walkMessage(buf, func(f protoField) error {
		if found || f.num != field || f.wire != wireBytes {
			return nil
		}
		out = f.raw
		found = true
		return nil
	})
	return out, found
}

func findFieldVarint(buf []byte, field int) (uint64, bool) {
	var out uint64
	found := false
	_ = walkMessage(buf, func(f protoField) error {
		if found || f.num != field || f.wire != wireVarint {
			return nil
		}
		out = f.val
		found = true
		return nil
	})
	return out, found
}

func findFieldString(buf []byte, field int) (string, bool) {
	if b, ok := findFieldBytes(buf, field); ok {
		return string(b), true
	}
	return "", false
}

func findAllFields(buf []byte, field int) [][]byte {
	var out [][]byte
	_ = walkMessage(buf, func(f protoField) error {
		if f.num == field && f.wire == wireBytes {
			out = append(out, f.raw)
		}
		return nil
	})
	return out
}
