// Package prodid pins the SHA-256 digests of the production ids that must stay out of the tree.
// The digests are reversible by brute force: they stop a value returning to the tree, not hide it.
package prodid

import (
	"crypto/sha256"
	"encoding/hex"
)

const (
	ProjectDigest = "b7343bb721f376f80356cb2c71b30071c1d7ac138f5074d7523f03ea025e73df" // GCP prod project number, 12 digits
	PortalDigest  = "a884363b5d6a9f30a90516f54e984f9138d0cced9e9f5ad323975123302b4697" // HubSpot portal id, 9 digits
)

// Sum is the hex SHA-256 of s.
func Sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
