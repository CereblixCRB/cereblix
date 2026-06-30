package main

// store.go ports the cereblix-wallet CLI's encrypted key store one-for-one so a
// wallet.json written by the CLI opens here unchanged (and vice-versa):
//
//   - file format: {version, encrypted, kdf, iter, salt, nonce, cipher, keys}
//   - KDF:         PBKDF2-HMAC-SHA256, 200000 iterations -> 32-byte key
//   - cipher:      AES-256-GCM over the JSON-encoded key array
//   - keys:        []KeyEntry{Label, Priv(128-hex ed25519), Addr}
//
// The only thing added for the GUI is an in-memory *locked* state: an encrypted
// wallet loads WITHOUT prompting (keys stay sealed in `enc`) and is decrypted on
// demand by Unlock(). Private keys never leave this file except via Export().

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"cereblix/core"
)

// kdfIters is the PBKDF2 iteration count for NEW saves (OWASP 2023 guidance for
// PBKDF2-HMAC-SHA256). Decryption uses the per-file stored Iter, so wallets written
// with the previous 200000-iteration count still open unchanged.
const kdfIters = 600_000

// minPassphrase is the minimum passphrase length enforced on every set/change.
const minPassphrase = 8

// maxKdfIters caps the iteration count honored when opening a wallet, so a tampered
// file cannot make decryption hang on an absurd work factor.
const maxKdfIters = 5_000_000

// KeyEntry is one address: label, 128-hex ed25519 private key, crb1 address.
// Wire-identical to the CLI wallet so files interoperate.
type KeyEntry struct {
	Label string `json:"label"`
	Priv  string `json:"priv"`
	Addr  string `json:"addr"`
}

// fileFormat is what lands on disk. When Encrypted, Keys is empty and the key
// array lives AES-GCM-encrypted in Cipher.
type fileFormat struct {
	Version   int        `json:"version"`
	Encrypted bool       `json:"encrypted"`
	KDF       string     `json:"kdf,omitempty"`
	Iter      int        `json:"iter,omitempty"`
	Salt      string     `json:"salt,omitempty"`
	Nonce     string     `json:"nonce,omitempty"`
	Cipher    string     `json:"cipher,omitempty"`
	Keys      []KeyEntry `json:"keys,omitempty"`
}

// Store is the in-memory wallet with a lock state. All exported methods are
// safe for concurrent use (Wails dispatches binding calls on goroutines).
type Store struct {
	mu sync.Mutex

	path       string
	encrypted  bool
	locked     bool        // true only for an encrypted wallet whose keys are still sealed
	keys       []KeyEntry  // decrypted keys; nil/empty while locked
	passphrase []byte      // cached for the session once unlocked
	enc        *fileFormat // retained encrypted blob (salt/nonce/cipher/iter) for Unlock()
	exists     bool        // a wallet file is present on disk
}

func defaultWalletPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".cereblix", "wallet.json")
}

// loadStore reads the wallet file WITHOUT prompting. An encrypted wallet comes
// back locked (keys sealed); call Unlock() to open it.
func loadStore(path string) (*Store, error) {
	s := &Store{path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil // fresh wallet, not yet saved
	}
	if err != nil {
		return nil, err
	}
	var ff fileFormat
	if err := json.Unmarshal(raw, &ff); err != nil {
		return nil, fmt.Errorf("corrupt wallet file: %w", err)
	}
	s.exists = true
	s.encrypted = ff.Encrypted
	if !ff.Encrypted {
		s.keys = ff.Keys
		return s, nil
	}
	// Encrypted: keep it sealed until Unlock() supplies the passphrase.
	s.locked = true
	c := ff
	s.enc = &c
	return s, nil
}

// ------------------------------------------------------------------ state

// state reports the wallet snapshot used by WalletState(). While locked the key
// count is unknown (the keys are still ciphertext) and reports 0.
func (s *Store) state() (exists, encrypted, locked bool, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exists, s.encrypted, s.locked, len(s.keys)
}

func (s *Store) isLocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked
}

// isEncrypted reports whether the wallet is passphrase-encrypted (used by the
// backend idle-lock to decide whether there is anything to seal).
func (s *Store) isEncrypted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.encrypted
}

// unlock decrypts an encrypted wallet. Returns (false,nil) on a wrong passphrase,
// (true,nil) once open. An unencrypted wallet is always considered open.
func (s *Store) unlock(passphrase string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.encrypted {
		s.locked = false
		return true, nil
	}
	if !s.locked {
		return true, nil // already open
	}
	if s.enc == nil {
		return false, errors.New("no encrypted wallet data to unlock")
	}
	keys, err := decryptKeys(s.enc, []byte(passphrase))
	if err != nil {
		return false, nil // wrong passphrase (or corrupt) -> caller shows "try again"
	}
	s.keys = keys
	s.passphrase = []byte(passphrase)
	s.locked = false
	return true, nil
}

// lock re-seals an encrypted wallet, wiping the decrypted keys and passphrase
// from memory. A no-op for an unencrypted wallet (nothing to seal it with).
func (s *Store) lock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.encrypted {
		return
	}
	for i := range s.keys {
		s.keys[i] = KeyEntry{}
	}
	s.keys = nil
	for i := range s.passphrase {
		s.passphrase[i] = 0
	}
	s.passphrase = nil
	s.locked = true
}

// requireOpen returns an error if the wallet is currently sealed.
func (s *Store) requireOpen() error {
	if s.locked {
		return errors.New("wallet is locked - unlock it first")
	}
	return nil
}

// ------------------------------------------------------------- persistence

// save writes the wallet atomically. When encrypted it derives a fresh AES-GCM
// blob from the cached passphrase and refreshes s.enc so a later lock/unlock
// round-trips. Caller must hold s.mu.
func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	var ff fileFormat
	ff.Version = 1
	if s.encrypted {
		if len(s.passphrase) == 0 {
			return errors.New("cannot save encrypted wallet without an unlocked passphrase")
		}
		salt := make([]byte, 16)
		nonce := make([]byte, 12)
		if _, err := rand.Read(salt); err != nil {
			return err
		}
		if _, err := rand.Read(nonce); err != nil {
			return err
		}
		plain, _ := json.Marshal(s.keys)
		key := pbkdf2(s.passphrase, salt, kdfIters, 32)
		blk, err := aes.NewCipher(key)
		if err != nil {
			return err
		}
		gcm, err := cipher.NewGCM(blk)
		if err != nil {
			return err
		}
		ct := gcm.Seal(nil, nonce, plain, nil)
		ff.Encrypted = true
		ff.KDF = "pbkdf2-sha256"
		ff.Iter = kdfIters
		ff.Salt = hex.EncodeToString(salt)
		ff.Nonce = hex.EncodeToString(nonce)
		ff.Cipher = hex.EncodeToString(ct)
		c := ff
		s.enc = &c // keep the on-disk blob for a future Unlock()
	} else {
		ff.Keys = s.keys
	}
	raw, _ := json.MarshalIndent(&ff, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.exists = true
	return nil
}

func decryptKeys(ff *fileFormat, pass []byte) ([]KeyEntry, error) {
	// Validate the on-disk fields BEFORE touching the cipher: a 12-byte nonce is a
	// hard requirement for AES-GCM (gcm.Open panics on a wrong-size nonce), and a
	// bounded Iter keeps a tampered file from hanging on an absurd work factor.
	salt, err := hex.DecodeString(ff.Salt)
	if err != nil || len(salt) < 16 {
		return nil, errors.New("corrupt wallet: bad salt")
	}
	nonce, err := hex.DecodeString(ff.Nonce)
	if err != nil || len(nonce) != 12 {
		return nil, errors.New("corrupt wallet: bad nonce")
	}
	ct, err := hex.DecodeString(ff.Cipher)
	if err != nil || len(ct) == 0 {
		return nil, errors.New("corrupt wallet: bad ciphertext")
	}
	iter := ff.Iter
	if iter == 0 {
		iter = kdfIters
	}
	if iter < 1 || iter > maxKdfIters {
		return nil, errors.New("corrupt wallet: invalid kdf iteration count")
	}
	key := pbkdf2(pass, salt, iter, 32)
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("wrong passphrase or corrupt wallet")
	}
	var keys []KeyEntry
	if err := json.Unmarshal(plain, &keys); err != nil {
		return nil, err
	}
	return keys, nil
}

// ------------------------------------------------------------------ keys

// findLocked returns a COPY of the matching entry (by address or label). Caller
// must hold s.mu. The copy includes the private key for local signing only.
func (s *Store) findLocked(addrOrLabel string) (KeyEntry, bool) {
	for i := range s.keys {
		if s.keys[i].Addr == addrOrLabel || s.keys[i].Label == addrOrLabel {
			return s.keys[i], true
		}
	}
	return KeyEntry{}, false
}

// find returns a copy of an entry (used internally for signing). Errors if locked.
func (s *Store) find(addrOrLabel string) (KeyEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return KeyEntry{}, err
	}
	e, ok := s.findLocked(addrOrLabel)
	if !ok {
		return KeyEntry{}, fmt.Errorf("no such address/label %q in wallet", addrOrLabel)
	}
	return e, nil
}

// list returns label/address copies of every key WITHOUT private material.
func (s *Store) list() ([]KeyEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	out := make([]KeyEntry, len(s.keys))
	for i, k := range s.keys {
		out[i] = KeyEntry{Label: k.Label, Addr: k.Addr} // Priv intentionally omitted
	}
	return out, nil
}

// create makes the wallet's FIRST address and encrypts the new wallet in one step.
// A passphrase (min length minPassphrase) is REQUIRED - encryption-at-rest is the
// default, so there is no plaintext-wallet path through onboarding. Errors if a
// wallet already exists.
func (s *Store) create(passphrase string) (KeyEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exists || len(s.keys) > 0 {
		return KeyEntry{}, errors.New("a wallet already exists at " + s.path)
	}
	if len(passphrase) < minPassphrase {
		return KeyEntry{}, fmt.Errorf("passphrase too short (min %d)", minPassphrase)
	}
	e, err := s.newKeyLocked("main")
	if err != nil {
		return KeyEntry{}, err
	}
	s.encrypted = true
	s.passphrase = []byte(passphrase)
	s.locked = false
	if err := s.save(); err != nil {
		return KeyEntry{}, err
	}
	return KeyEntry{Label: e.Label, Addr: e.Addr}, nil
}

// add creates an additional address. Errors if locked.
func (s *Store) add(label string) (KeyEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return KeyEntry{}, err
	}
	e, err := s.newKeyLocked(label)
	if err != nil {
		return KeyEntry{}, err
	}
	if err := s.save(); err != nil {
		return KeyEntry{}, err
	}
	return KeyEntry{Label: e.Label, Addr: e.Addr}, nil
}

// newKeyLocked appends a freshly generated key. Caller holds s.mu; does NOT save.
func (s *Store) newKeyLocked(label string) (KeyEntry, error) {
	if label == "" {
		label = fmt.Sprintf("addr-%d", len(s.keys)+1)
	}
	if _, ok := s.findLocked(label); ok {
		return KeyEntry{}, fmt.Errorf("label %q already exists", label)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyEntry{}, err
	}
	e := KeyEntry{
		Label: label,
		Priv:  hex.EncodeToString(priv),
		Addr:  core.AddrFromPub(priv.Public().(ed25519.PublicKey)),
	}
	s.keys = append(s.keys, e)
	return e, nil
}

// importKey adds an existing 128-hex ed25519 private key. Errors if locked.
func (s *Store) importKey(privHex, label string) (KeyEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return KeyEntry{}, err
	}
	raw, err := hex.DecodeString(privHex)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return KeyEntry{}, errors.New("private key must be 128 hex characters")
	}
	priv := ed25519.PrivateKey(raw)
	// Integrity check: a 128-hex blob is seed(32)||pubkey(32); a malformed key can
	// carry a public half that does not match its seed. Re-derive the canonical key
	// from the seed and confirm the embedded public half matches, then round-trip a
	// signature. This rejects a mismatched/garbage key instead of storing one whose
	// address signs nothing.
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok || len(pub) != ed25519.PublicKeySize {
		return KeyEntry{}, errors.New("invalid private key")
	}
	canonical := ed25519.NewKeyFromSeed(priv.Seed())
	if !bytes.Equal(canonical.Public().(ed25519.PublicKey), pub) {
		return KeyEntry{}, errors.New("invalid private key: public half does not match its seed")
	}
	probe := []byte("cereblix-import-integrity-check")
	if !ed25519.Verify(pub, probe, ed25519.Sign(priv, probe)) {
		return KeyEntry{}, errors.New("invalid private key: failed sign/verify check")
	}
	if label == "" {
		label = "imported"
	}
	if _, ok := s.findLocked(label); ok {
		label = fmt.Sprintf("%s-%d", label, len(s.keys)+1)
	}
	addr := core.AddrFromPub(pub)
	if _, ok := s.findLocked(addr); ok {
		return KeyEntry{}, errors.New("address already in wallet")
	}
	s.keys = append(s.keys, KeyEntry{Label: label, Priv: privHex, Addr: addr})
	if err := s.save(); err != nil {
		return KeyEntry{}, err
	}
	return KeyEntry{Label: label, Addr: addr}, nil
}

// exportKey reveals a private key. This is the ONLY path that returns private
// material. For an encrypted wallet the passphrase is re-verified even within an
// already-unlocked session.
func (s *Store) exportKey(addrOrLabel, passphrase string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return "", err
	}
	if s.encrypted {
		if !passMatch(s.passphrase, []byte(passphrase)) {
			return "", errors.New("wrong passphrase")
		}
	}
	e, ok := s.findLocked(addrOrLabel)
	if !ok {
		return "", fmt.Errorf("no such address/label %q in wallet", addrOrLabel)
	}
	return e.Priv, nil
}

// encrypt passphrase-encrypts a previously plaintext wallet.
func (s *Store) encrypt(passphrase string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.encrypted {
		return errors.New("wallet is already encrypted")
	}
	if len(s.keys) == 0 {
		return errors.New("nothing to encrypt - create an address first")
	}
	if len(passphrase) < minPassphrase {
		return fmt.Errorf("passphrase too short (min %d)", minPassphrase)
	}
	s.encrypted = true
	s.passphrase = []byte(passphrase)
	s.locked = false
	return s.save()
}

// changePassphrase re-encrypts the wallet under a new passphrase. The wallet must
// be encrypted and unlocked; the old passphrase is verified against the session.
func (s *Store) changePassphrase(oldp, newp string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.encrypted {
		return errors.New("wallet is not encrypted - use EncryptWallet to set a passphrase")
	}
	if err := s.requireOpen(); err != nil {
		return err
	}
	if !passMatch(s.passphrase, []byte(oldp)) {
		return errors.New("old passphrase is incorrect")
	}
	if len(newp) < minPassphrase {
		return fmt.Errorf("new passphrase too short (min %d)", minPassphrase)
	}
	s.passphrase = []byte(newp)
	return s.save()
}

// ----------------------------------------------------------- crypto: PBKDF2

// pbkdf2 implements PBKDF2-HMAC-SHA256 (RFC 2898) with only the stdlib, keeping
// the project's zero-dependency promise. Byte-identical to the CLI wallet.
func pbkdf2(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen
	dk := make([]byte, 0, numBlocks*hashLen)
	var blockIdx [4]byte
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(blockIdx[:], uint32(block))
		prf.Write(blockIdx[:])
		T := prf.Sum(nil)
		U := make([]byte, len(T))
		copy(U, T)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(U)
			U = prf.Sum(U[:0])
			for x := range T {
				T[x] ^= U[x]
			}
		}
		dk = append(dk, T...)
	}
	return dk[:keyLen]
}

// passMatch is a constant-time []byte equality (length-safe).
func passMatch(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
