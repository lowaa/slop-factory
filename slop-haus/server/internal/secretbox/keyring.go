// Package secretbox seals secrets for the database under a master key, and
// keeps secrets out of logs. It names no consumer: what it seals is the
// caller's business (today, code host credentials).
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// KeySize is the master key's length: AES-256.
const KeySize = 32

const (
	blobVersion = 1
	keyIDSize   = 8
	nonceSize   = 12
	headerSize  = 1 + keyIDSize + nonceSize
)

// ErrCannotOpen means a blob was not sealed by any key the keyring holds,
// or has been altered, or belongs to another tenant or row. The cases are
// not told apart: a caller can do nothing different for each, and a
// decryption oracle is not worth the detail.
var ErrCannotOpen = errors.New("secretbox: stored credential cannot be decrypted with the configured keys")

// Binding is what a blob is sealed for. It is authenticated with the
// ciphertext but not stored in it, so a blob moved to another tenant's row
// -- or to another row of the same tenant -- fails to open.
type Binding struct {
	Tenant uuid.UUID
	// Row is the credential row's id.
	Row uuid.UUID
	// Purpose names which secret of the row this is, so two secrets of one
	// row cannot be swapped for each other. Empty is the row's original
	// secret (the App private key), whose sealed form predates purposes.
	Purpose string
}

// Keyring seals and opens credential secrets with AES-256-GCM under a
// current key, and opens what a previous key sealed, so the key can be
// rotated without a flag day: replacing or verifying a credential
// re-seals it under the current key.
type Keyring struct {
	current  keyEntry
	previous *keyEntry
}

type keyEntry struct {
	id   [keyIDSize]byte
	aead cipher.AEAD
}

// ParseKey reads a master key given as base64 of exactly KeySize bytes.
func ParseKey(b64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, errors.New("not base64")
	}
	if len(raw) != KeySize {
		return nil, fmt.Errorf("must decode to %d bytes, got %d", KeySize, len(raw))
	}
	return raw, nil
}

// NewKeyring holds the current key and, optionally, the previous one.
func NewKeyring(current, previous []byte) (*Keyring, error) {
	cur, err := newKeyEntry(current)
	if err != nil {
		return nil, err
	}
	ring := &Keyring{current: cur}
	if len(previous) > 0 {
		prev, err := newKeyEntry(previous)
		if err != nil {
			return nil, err
		}
		if prev.id == cur.id {
			return nil, errors.New("secretbox: the previous key is the current key")
		}
		ring.previous = &prev
	}
	return ring, nil
}

func newKeyEntry(key []byte) (keyEntry, error) {
	if len(key) != KeySize {
		return keyEntry{}, fmt.Errorf("secretbox: key must be %d bytes", KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return keyEntry{}, fmt.Errorf("secretbox: key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return keyEntry{}, fmt.Errorf("secretbox: key: %w", err)
	}
	sum := sha256.Sum256(key)
	e := keyEntry{aead: aead}
	copy(e.id[:], sum[:keyIDSize])
	return e, nil
}

// CurrentID names the key Seal uses, as stored beside each blob.
func (k *Keyring) CurrentID() string { return hex.EncodeToString(k.current.id[:]) }

// Seal encrypts plaintext under the current key. The blob is
// version | key id | nonce | ciphertext+tag, with a fresh random nonce
// every time: a nonce reused under one key breaks GCM, and a credential is
// sealed on every replace and verify.
func (k *Keyring) Seal(plaintext []byte, b Binding) ([]byte, error) {
	blob := make([]byte, headerSize, headerSize+len(plaintext)+k.current.aead.Overhead())
	blob[0] = blobVersion
	copy(blob[1:], k.current.id[:])
	nonce := blob[1+keyIDSize : headerSize]
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secretbox: nonce: %w", err)
	}
	return k.current.aead.Seal(blob, nonce, plaintext, additionalData(blob[:1+keyIDSize], b)), nil
}

// Open decrypts a blob Seal made for b.
func (k *Keyring) Open(blob []byte, b Binding) ([]byte, error) {
	if len(blob) < headerSize || blob[0] != blobVersion {
		return nil, ErrCannotOpen
	}
	entry := k.entryFor(blob[1 : 1+keyIDSize])
	if entry == nil {
		return nil, ErrCannotOpen
	}
	plain, err := entry.aead.Open(nil, blob[1+keyIDSize:headerSize], blob[headerSize:], additionalData(blob[:1+keyIDSize], b))
	if err != nil {
		return nil, ErrCannotOpen
	}
	return plain, nil
}

// BlobKeyID is the id of the key a blob names, for finding what needs
// re-sealing.
func BlobKeyID(blob []byte) string {
	if len(blob) < headerSize {
		return ""
	}
	return hex.EncodeToString(blob[1 : 1+keyIDSize])
}

func (k *Keyring) entryFor(id []byte) *keyEntry {
	if string(id) == string(k.current.id[:]) {
		return &k.current
	}
	if k.previous != nil && string(id) == string(k.previous.id[:]) {
		return k.previous
	}
	return nil
}

// additionalData is what a blob's tag covers beyond its ciphertext: a
// domain separator, the header (version and key id, so neither can be
// changed to steer a decryption), the tenant and the row. Length prefixes
// are not needed: every part but the first is fixed-size.
func additionalData(header []byte, b Binding) []byte {
	// Part of the blob format, which is already stored: changing it makes
	// every sealed credential unreadable (see the fixed-blob test).
	// Part of the blob format, which is already stored: changing it makes
	// every sealed credential unreadable (see the fixed-blob test).
	const domain = "slop-haus codehost credential"
	ad := make([]byte, 0, len(domain)+len(header)+32+8)
	ad = append(ad, domain...)
	ad = binary.BigEndian.AppendUint16(ad, uint16(len(header)))
	ad = append(ad, header...)
	ad = append(ad, b.Tenant[:]...)
	ad = append(ad, b.Row[:]...)
	if b.Purpose != "" {
		ad = append(ad, b.Purpose...)
	}
	return ad
}
