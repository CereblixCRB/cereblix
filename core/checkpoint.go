package core

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
)

// AuthorityPubKey is the founder's checkpoint-signing public key. Nodes treat
// checkpoints signed by this key as canonical-chain finality: no reorg may cross
// a checkpointed height, and a block at a checkpointed height must match it. New
// nodes trust this key from first run, so they automatically follow the
// authority's chain even against a higher-hashrate fork.
//
// This is an INTENTIONAL, removable centralization lever for the early
// "benevolent dictator" phase. Set it to "" and rebuild to fully decentralize.
const AuthorityPubKey = "7a2712d5d7ac3734ecf85ee256c932465444c973ef486b595f327b9af8f68b20"

// Checkpoint is a height -> block-hash finality marker signed by the authority.
type Checkpoint struct {
	Height uint64 `json:"height"`
	Hash   string `json:"hash"`
	Sig    string `json:"sig"` // hex ed25519 over checkpointMsg(Height, Hash)
}

func checkpointMsg(height uint64, hash string) []byte {
	return []byte(fmt.Sprintf("cereblix-checkpoint-v1|%d|%s", height, hash))
}

// Verify checks the checkpoint's signature against the hardcoded authority key.
func (cp Checkpoint) Verify() bool {
	if AuthorityPubKey == "" || cp.Hash == "" {
		return false
	}
	pub, err := hex.DecodeString(AuthorityPubKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(cp.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), checkpointMsg(cp.Height, cp.Hash), sig)
}

// SignCheckpoint produces a signed checkpoint with the authority private key.
func SignCheckpoint(height uint64, hash string, priv ed25519.PrivateKey) Checkpoint {
	return Checkpoint{Height: height, Hash: hash,
		Sig: hex.EncodeToString(ed25519.Sign(priv, checkpointMsg(height, hash)))}
}
