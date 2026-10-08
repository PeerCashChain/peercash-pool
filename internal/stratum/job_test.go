package stratum

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

// sealInput replicates peercash-chain consensus/randomx sealInput so the test
// can assert the blob the bridge builds matches what the node will hash.
func sealInput(sealHash [32]byte, nonce uint64) [43]byte {
	var out [43]byte
	copy(out[0:32], sealHash[:])
	binary.BigEndian.PutUint64(out[35:], nonce)
	return out
}

func TestBuildBlobLayout(t *testing.T) {
	var sealHash [32]byte
	for i := range sealHash {
		sealHash[i] = byte(i + 1)
	}
	extranonce := [4]byte{0x00, 0x00, 0x00, 0x05}
	blob := buildBlob(sealHash, extranonce)

	if len(blob) != 43 {
		t.Fatalf("blob len = %d, want 43", len(blob))
	}
	if !bytes.Equal(blob[0:32], sealHash[:]) {
		t.Error("sealHash not at [0:32]")
	}
	if !bytes.Equal(blob[32:35], []byte{0, 0, 0}) {
		t.Error("[32:35] not zero padding")
	}
	if !bytes.Equal(blob[35:39], extranonce[:]) {
		t.Error("extranonce not at [35:39]")
	}
	if !bytes.Equal(blob[39:43], []byte{0, 0, 0, 0}) {
		t.Error("[39:43] nonce field not zero (would flip XMRig to nicehash)")
	}
}

func TestTargetToStratum(t *testing.T) {
	// Node target: 32-byte big-endian, value 0x3ff in the last 8 bytes.
	nodeTarget := "0x" + strings.Repeat("00", 24) + "00000000000003ff"
	got, err := targetToStratum(nodeTarget)
	if err != nil {
		t.Fatal(err)
	}
	// Little-endian of 0x00000000000003ff -> ff 03 00 00 00 00 00 00.
	if want := "ff03000000000000"; got != want {
		t.Errorf("targetToStratum = %s, want %s", got, want)
	}
}

func TestHeaderNonceRoundTrip(t *testing.T) {
	var sealHash [32]byte
	for i := range sealHash {
		sealHash[i] = byte(0xA0 + i)
	}
	extranonce := [4]byte{0x00, 0x00, 0x00, 0x05}
	xmrigNonce := [4]byte{0xde, 0xad, 0xbe, 0xef}

	// What the bridge sends to eth_submitWork.
	nonceHex := headerNonce(extranonce, xmrigNonce)
	if want := "0x00000005deadbeef"; nonceHex != want {
		t.Fatalf("headerNonce = %s, want %s", nonceHex, want)
	}

	// Reconstruct the blob XMRig hashed: buildBlob + XMRig's nonce spliced in.
	blob := buildBlob(sealHash, extranonce)
	copy(blob[39:43], xmrigNonce[:])

	// The node will hash sealInput(sealHash, BigEndian(blob[35:43])). Assert that
	// equals the exact blob XMRig hashed, i.e. the hashes will match.
	nonceVal := binary.BigEndian.Uint64(blob[35:43])
	reconstructed := sealInput(sealHash, nonceVal)
	if !bytes.Equal(blob[:], reconstructed[:]) {
		t.Error("node sealInput does not reproduce the blob XMRig hashed")
	}
	// And the submitted nonce hex must decode to exactly blob[35:43].
	decoded, _ := hex.DecodeString(strings.TrimPrefix(nonceHex, "0x"))
	if !bytes.Equal(decoded, blob[35:43]) {
		t.Error("submitted nonce does not equal blob[35:43]")
	}
}

func TestNewWork(t *testing.T) {
	seal := "0x" + strings.Repeat("11", 32)
	seed := "0x" + strings.Repeat("22", 32)
	target := "0x" + strings.Repeat("00", 24) + "00000000000003ff"
	w, err := NewWork("abc", seal, seed, target, 42)
	if err != nil {
		t.Fatal(err)
	}
	if w.JobID != "abc" || w.Height != 42 {
		t.Error("job id / height not carried through")
	}
	if w.SeedHex != strings.Repeat("22", 32) {
		t.Error("seed should be stored without 0x")
	}
	if w.TargetLE != "ff03000000000000" {
		t.Errorf("target = %s", w.TargetLE)
	}
}
