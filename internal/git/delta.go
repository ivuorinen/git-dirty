package git

import (
	"errors"
	"fmt"
)

// applyDelta rebuilds an object from its base and a git delta: the base and
// result sizes as varints, then copy-from-base and insert opcodes. Every
// size and range is checked against the declared sizes and maxObjectSize.
func applyDelta(base, delta []byte) ([]byte, error) {
	pos := 0
	srcSize, err := deltaSize(delta, &pos)
	if err != nil {
		return nil, err
	}
	if srcSize != uint64(len(base)) {
		return nil, fmt.Errorf("delta base size %d, actual %d", srcSize, len(base))
	}
	dstSize, err := deltaSize(delta, &pos)
	if err != nil {
		return nil, err
	}
	if dstSize > maxObjectSize {
		return nil, fmt.Errorf("delta result size %d exceeds the limit", dstSize)
	}
	out := make([]byte, 0, dstSize)
	for pos < len(delta) {
		op := delta[pos]
		pos++
		switch {
		case op&0x80 != 0: // copy from base
			var off, size uint64
			for i := range 4 {
				if op&(1<<i) != 0 {
					if pos >= len(delta) {
						return nil, errors.New("delta copy opcode truncated")
					}
					off |= uint64(delta[pos]) << (8 * i)
					pos++
				}
			}
			for i := range 3 {
				if op&(0x10<<i) != 0 {
					if pos >= len(delta) {
						return nil, errors.New("delta copy opcode truncated")
					}
					size |= uint64(delta[pos]) << (8 * i)
					pos++
				}
			}
			if size == 0 {
				size = 0x10000
			}
			if off+size > uint64(len(base)) {
				return nil, fmt.Errorf("delta copies %d bytes at %d from a %d-byte base", size, off, len(base))
			}
			if uint64(len(out))+size > dstSize {
				return nil, errors.New("delta writes past its declared size")
			}
			out = append(out, base[off:off+size]...)
		case op != 0: // insert op literal bytes
			n := int(op)
			if pos+n > len(delta) {
				return nil, errors.New("delta insert truncated")
			}
			if uint64(len(out)+n) > dstSize {
				return nil, errors.New("delta writes past its declared size")
			}
			out = append(out, delta[pos:pos+n]...)
			pos += n
		default:
			return nil, errors.New("delta uses reserved opcode 0")
		}
	}
	if uint64(len(out)) != dstSize {
		return nil, fmt.Errorf("delta produced %d bytes, declared %d", len(out), dstSize)
	}
	return out, nil
}

// deltaSize reads a little-endian base-128 size from a delta header.
func deltaSize(b []byte, pos *int) (uint64, error) {
	var v uint64
	for shift := 0; ; shift += 7 {
		if *pos >= len(b) || shift > 63 {
			return 0, errors.New("delta header truncated or too long")
		}
		c := b[*pos]
		*pos++
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return v, nil
		}
	}
}
