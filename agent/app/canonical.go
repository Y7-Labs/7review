package app

import (
	"crypto/sha256"
	"encoding/hex"
)

func canonicalPayloadDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
