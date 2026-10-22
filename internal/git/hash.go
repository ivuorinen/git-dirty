// Package git reads git repositories from disk: repository format, config,
// refs, the index and objects from loose files and packs. It uses the
// standard library only and never executes git.
//
// Everything it reads may come from a hostile clone, so every decoder
// bounds-checks its input and returns errors instead of panicking.
package git

import (
	"crypto/sha1" //nolint:gosec // G505: SHA-1 is git's object id format here, not a security primitive
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"strconv"
)

// maxHashSize is the largest object id, SHA-256's 32 bytes.
const maxHashSize = 32

// ObjectID is a SHA-1 (20-byte) or SHA-256 (32-byte) object id. It is
// comparable and usable as a map key; the zero value is the empty id.
type ObjectID struct {
	b [maxHashSize]byte
	n uint8
}

// String returns the id in lowercase hex.
func (id ObjectID) String() string { return hex.EncodeToString(id.b[:id.n]) }

// Bytes returns the raw id.
func (id ObjectID) Bytes() []byte { return append([]byte(nil), id.b[:id.n]...) }

// IsZero reports whether the id is empty or all zero bytes.
func (id ObjectID) IsZero() bool {
	for _, c := range id.b[:id.n] {
		if c != 0 {
			return false
		}
	}

	return true
}

// ObjectType is a git object type, with the pack-only delta types.
type ObjectType int

// Object types, as numbered in pack entry headers.
const (
	ObjCommit   ObjectType = 1
	ObjTree     ObjectType = 2
	ObjBlob     ObjectType = 3
	ObjTag      ObjectType = 4
	objOfsDelta ObjectType = 6
	objRefDelta ObjectType = 7
)

func (t ObjectType) String() string {
	switch t {
	case ObjCommit:
		return "commit"
	case ObjTree:
		return "tree"
	case ObjBlob:
		return "blob"
	case ObjTag:
		return "tag"
	case objOfsDelta, objRefDelta:
		// Pack-only: no object header names them.
	}

	return "unknown(" + strconv.Itoa(int(t)) + ")"
}

// Format is a repository's object format.
type Format struct {
	Name    string
	Size    int // id length in bytes
	newHash func() hash.Hash
}

// The object formats git supports.
var (
	SHA1   = Format{Name: "sha1", Size: sha1.Size, newHash: sha1.New}
	SHA256 = Format{Name: "sha256", Size: sha256.Size, newHash: sha256.New}
)

// IDFromBytes makes an id from exactly f.Size raw bytes.
func (f Format) IDFromBytes(b []byte) (ObjectID, error) {
	n := f.Size
	if n < 0 || n > maxHashSize {
		return ObjectID{}, fmt.Errorf("%s id: unsupported size %d", f.Name, n)
	}
	if len(b) != n {
		return ObjectID{}, fmt.Errorf("%s id: %d bytes, want %d", f.Name, len(b), n)
	}
	var id ObjectID
	copy(id.b[:], b)
	id.n = uint8(n)

	return id, nil
}

// ParseHex parses a full-length hex id.
func (f Format) ParseHex(s string) (ObjectID, error) {
	if len(s) != 2*f.Size {
		return ObjectID{}, fmt.Errorf("%s id %q: wrong length", f.Name, s)
	}
	var buf [maxHashSize]byte
	if _, err := hex.Decode(buf[:f.Size], []byte(s)); err != nil {
		return ObjectID{}, fmt.Errorf("%s id %q: %w", f.Name, s, err)
	}

	return f.IDFromBytes(buf[:f.Size])
}

// ObjectHasher computes an object id from streamed content.
type ObjectHasher struct {
	h hash.Hash
	f Format
}

// NewObjectHasher starts hashing an object of the given type and size: git
// hashes "<type> <size>\x00" followed by the content.
func (f Format) NewObjectHasher(t ObjectType, size int64) *ObjectHasher {
	h := f.newHash()
	_, _ = fmt.Fprintf(h, "%s %d\x00", t, size) // a hash.Hash never returns a write error

	return &ObjectHasher{h: h, f: f}
}

func (o *ObjectHasher) Write(p []byte) (int, error) { return o.h.Write(p) }

// Sum returns the object id.
func (o *ObjectHasher) Sum() ObjectID {
	id, _ := o.f.IDFromBytes(o.h.Sum(nil))

	return id
}

// HashObject returns the id of an object with the given type and content.
func (f Format) HashObject(t ObjectType, data []byte) ObjectID {
	h := f.NewObjectHasher(t, int64(len(data)))
	_, _ = h.Write(data) // a hash.Hash never returns a write error

	return h.Sum()
}
