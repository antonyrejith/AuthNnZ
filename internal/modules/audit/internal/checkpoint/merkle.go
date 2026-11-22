package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"math/bits"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// Merkle tree hashing of RFC 6962 §2.1 (as in RFC 9162 §2.1): a leaf is
// SHA-256(0x00 || data), an interior node SHA-256(0x01 || left || right),
// and the tree over n leaves splits at the largest power of two below n.
// The leaf data of an audit checkpoint is the 32-byte event hash.

// LeafHash returns the RFC 6962 leaf hash of data.
func LeafHash(data []byte) []byte {
	h := sha256.New()
	h.Write([]byte{0x00})
	h.Write(data)
	return h.Sum(nil)
}

func nodeHash(left, right []byte) []byte {
	h := sha256.New()
	h.Write([]byte{0x01})
	h.Write(left)
	h.Write(right)
	return h.Sum(nil)
}

// RootHash returns the Merkle tree hash of the leaf data items (the
// SHA-256 of the empty string for no items).
func RootHash(items [][]byte) []byte {
	if len(items) == 0 {
		s := sha256.Sum256(nil)
		return s[:]
	}
	leaves := make([][]byte, len(items))
	for i, it := range items {
		leaves[i] = LeafHash(it)
	}
	return subtreeRoot(leaves)
}

func subtreeRoot(leaves [][]byte) []byte {
	if len(leaves) == 1 {
		return leaves[0]
	}
	k := split(len(leaves))
	return nodeHash(subtreeRoot(leaves[:k]), subtreeRoot(leaves[k:]))
}

// split returns the largest power of two smaller than n (n >= 2).
func split(n int) int {
	return 1 << (bits.Len(uint(n-1)) - 1)
}

// InclusionProof returns the audit path of leaf index in the tree over
// items (RFC 6962 §2.1.1).
func InclusionProof(items [][]byte, index int) ([][]byte, error) {
	if index < 0 || index >= len(items) {
		return nil, errs.New(errs.CodeInvalidRequest, "leaf index out of range")
	}
	leaves := make([][]byte, len(items))
	for i, it := range items {
		leaves[i] = LeafHash(it)
	}
	return path(index, leaves), nil
}

func path(m int, leaves [][]byte) [][]byte {
	if len(leaves) <= 1 {
		return nil
	}
	k := split(len(leaves))
	if m < k {
		return append(path(m, leaves[:k]), subtreeRoot(leaves[k:]))
	}
	return append(path(m-k, leaves[k:]), subtreeRoot(leaves[:k]))
}

// VerifyInclusion checks that leafData is item index of a tree of size
// items with the given root, using the audit path (RFC 9162 §2.1.3.2).
func VerifyInclusion(leafData []byte, index, size uint64, proof [][]byte, root []byte) bool {
	if index >= size {
		return false
	}
	fn, sn := index, size-1
	r := LeafHash(leafData)
	for _, p := range proof {
		if sn == 0 {
			return false
		}
		if fn&1 == 1 || fn == sn {
			r = nodeHash(p, r)
			if fn&1 == 0 {
				for fn&1 == 0 && fn != 0 {
					fn >>= 1
					sn >>= 1
				}
			}
		} else {
			r = nodeHash(r, p)
		}
		fn >>= 1
		sn >>= 1
	}
	return sn == 0 && bytes.Equal(r, root)
}
