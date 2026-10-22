package git

import (
	"errors"
	"fmt"
)

// applyDelta rebuilds an object from its base and a git delta: the base and
// result sizes as varints, then copy-from-base and insert opcodes. Every
// size and range is checked against the declared sizes and maxObjectSize.
func applyDelta(base, delta []byte) ([]byte, error) {
	d := deltaApplier{base: base, delta: delta}
	if err := d.header(); err != nil {
		return nil, err
	}
	d.out = make([]byte, 0, d.size)
	for d.pos < len(d.delta) {
		op := d.delta[d.pos]
		d.pos++
		var err error
		switch {
		case op&0x80 != 0: // copy from base
			err = d.copyFromBase(op)
		case op != 0: // insert op literal bytes
			err = d.insert(op)
		default:
			err = errors.New("delta uses reserved opcode 0")
		}
		if err != nil {
			return nil, err
		}
	}
	if uint64(len(d.out)) != d.size {
		return nil, fmt.Errorf("delta produced %d bytes, declared %d", len(d.out), d.size)
	}

	return d.out, nil
}

// deltaApplier is applyDelta's state: the input, the read position in the
// delta, the declared result size and the result so far.
type deltaApplier struct {
	base, delta, out []byte
	pos              int
	size             uint64
}

// header reads the base and result sizes, checking them against the base
// and maxObjectSize.
func (d *deltaApplier) header() error {
	srcSize, err := deltaSize(d.delta, &d.pos)
	if err != nil {
		return err
	}
	if srcSize != uint64(len(d.base)) {
		return fmt.Errorf("delta base size %d, actual %d", srcSize, len(d.base))
	}
	if d.size, err = deltaSize(d.delta, &d.pos); err != nil {
		return err
	}
	if d.size > maxObjectSize {
		return fmt.Errorf("delta result size %d exceeds the limit", d.size)
	}

	return nil
}

// copyFromBase applies a copy opcode: bits 0-3 select the offset bytes and
// bits 4-6 the size bytes that follow it; a size of zero means 0x10000.
func (d *deltaApplier) copyFromBase(op byte) error {
	off, err := d.copyArg(op, 4)
	if err != nil {
		return err
	}
	size, err := d.copyArg(op>>4, 3)
	if err != nil {
		return err
	}
	if size == 0 {
		size = 0x10000
	}
	if off+size > uint64(len(d.base)) {
		return fmt.Errorf("delta copies %d bytes at %d from a %d-byte base", size, off, len(d.base))
	}
	if uint64(len(d.out))+size > d.size {
		return errors.New("delta writes past its declared size")
	}
	d.out = append(d.out, d.base[off:off+size]...)

	return nil
}

// copyArg reads a copy opcode's little-endian offset or size: one byte for
// each of the low n bits set in mask.
func (d *deltaApplier) copyArg(mask byte, n int) (uint64, error) {
	var v uint64
	for i := range n {
		if mask&(1<<i) == 0 {
			continue
		}
		if d.pos >= len(d.delta) {
			return 0, errors.New("delta copy opcode truncated")
		}
		v |= uint64(d.delta[d.pos]) << (8 * i)
		d.pos++
	}

	return v, nil
}

// insert applies an insert opcode: the next op bytes are literal output.
func (d *deltaApplier) insert(op byte) error {
	n := int(op)
	if d.pos+n > len(d.delta) {
		return errors.New("delta insert truncated")
	}
	if uint64(len(d.out))+uint64(op) > d.size {
		return errors.New("delta writes past its declared size")
	}
	d.out = append(d.out, d.delta[d.pos:d.pos+n]...)
	d.pos += n

	return nil
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
