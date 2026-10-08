package stratum

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

// Blob layout (43 bytes), matching peercash-chain consensus/randomx sealInput:
//
//	[0:32]  sealHash
//	[32:35] zero padding
//	[35:39] per-worker extranonce (high 4 bytes of the header nonce)
//	[39:43] XMRig's 4-byte nonce (low 4 bytes); sent as zero so XMRig stays in
//	        standard mode (non-zero here flips it to nicehash mode)
//
// The 8-byte region [35:43] is the big-endian header nonce the node expects.
const (
	blobLen       = 43
	extranonceOff = 35
	nonceOff      = 39
	nonceEnd      = 43
)

// Work is one unit of mining work derived from eth_getWork, pre-converted into
// the forms the stratum layer needs.
type Work struct {
	JobID    string
	SealHash [32]byte
	SeedHex  string // 64 hex chars, no 0x (XMRig "seed_hash")
	TargetLE string // 16 hex chars, little-endian (XMRig "target")
	Height   uint64
}

// NewWork builds a Work from raw eth_getWork fields (0x-prefixed hex) plus the
// head height, converting the seal hash, target and seed into stratum forms.
func NewWork(jobID, sealHashHex, seedHex, nodeTargetHex string, height uint64) (*Work, error) {
	sealHash, err := parseHash32(sealHashHex)
	if err != nil {
		return nil, fmt.Errorf("sealHash: %w", err)
	}
	target, err := targetToStratum(nodeTargetHex)
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	seed := strings.TrimPrefix(seedHex, "0x")
	if _, err := hex.DecodeString(seed); err != nil {
		return nil, fmt.Errorf("seedHash: %w", err)
	}
	return &Work{
		JobID:    jobID,
		SealHash: sealHash,
		SeedHex:  seed,
		TargetLE: target,
		Height:   height,
	}, nil
}

// buildBlob assembles the 43-byte job blob for a worker's extranonce. Bytes
// [39:43] are left zero for XMRig to fill with its nonce.
func buildBlob(sealHash [32]byte, extranonce [4]byte) [blobLen]byte {
	var blob [blobLen]byte
	copy(blob[0:32], sealHash[:])
	copy(blob[extranonceOff:nonceOff], extranonce[:])
	return blob
}

// targetToStratum converts the node's eth_getWork target (0x + 32-byte
// big-endian hex, value in the last 8 bytes) into the 8-byte little-endian hex
// target XMRig expects. XMRig compares a result's last 8 bytes (little-endian)
// against this, matching the node's meetsTarget rule exactly.
func targetToStratum(nodeTarget string) (string, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(nodeTarget, "0x"))
	if err != nil {
		return "", err
	}
	if len(b) != 32 {
		return "", fmt.Errorf("expected 32-byte target, got %d", len(b))
	}
	// Last 8 bytes hold the big-endian target value; reverse for little-endian.
	le := make([]byte, 8)
	for i := 0; i < 8; i++ {
		le[i] = b[31-i]
	}
	return hex.EncodeToString(le), nil
}

// headerNonce rebuilds the 8-byte big-endian header nonce (blob[35:43]) from a
// worker's extranonce and the 4 nonce bytes XMRig reported, returning it
// 0x-prefixed for eth_submitWork. The bytes are spliced back in blob order, so
// no endianness interpretation of XMRig's nonce is needed.
func headerNonce(extranonce [4]byte, xmrigNonce [4]byte) string {
	var n [8]byte
	copy(n[0:4], extranonce[:])
	copy(n[4:8], xmrigNonce[:])
	return "0x" + hex.EncodeToString(n[:])
}

// extranonceBytes encodes a counter as a 4-byte big-endian extranonce.
func extranonceBytes(v uint32) [4]byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b
}

// parseHash32 decodes a 0x-prefixed (or bare) 32-byte hex string.
func parseHash32(s string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		return out, err
	}
	if len(b) != 32 {
		return out, fmt.Errorf("expected 32 bytes, got %d", len(b))
	}
	copy(out[:], b)
	return out, nil
}

// parseNonce4 decodes XMRig's 4-byte nonce (8 hex chars), in blob byte order.
func parseNonce4(s string) ([4]byte, error) {
	var out [4]byte
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		return out, err
	}
	if len(b) != 4 {
		return out, fmt.Errorf("expected 4-byte nonce, got %d", len(b))
	}
	copy(out[:], b)
	return out, nil
}
