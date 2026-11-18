package oauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

// DefaultRefreshReuseGrace is how long after a refresh token is rotated out
// that presenting it again is answered with the pair already issued, rather
// than as a stolen copy. It covers a client whose refresh response was lost
// in transit and which retries. Short, because every second of it is a
// second in which a stolen token works once more -- though only ever to
// fetch the pair its legitimate holder was already sent.
const DefaultRefreshReuseGrace = 10 * time.Second

// sealResponse encrypts resp under a key only the holder of oldRefresh can
// derive. The database keeps just a hash of that token, so a reader of the
// database cannot open the blob and recover live tokens from it. oldHash is
// bound in as associated data, so a blob cannot be moved to another row.
func sealResponse(resp *TokenResponse, oldRefresh, oldHash string) ([]byte, error) {
	plain, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("oauth: seal replay: %w", err)
	}
	aead, err := replayAEAD(oldRefresh)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("oauth: seal replay: %w", err)
	}
	return aead.Seal(nonce, nonce, plain, []byte(oldHash)), nil
}

func openResponse(blob []byte, oldRefresh, oldHash string) (*TokenResponse, error) {
	aead, err := replayAEAD(oldRefresh)
	if err != nil {
		return nil, err
	}
	if len(blob) < aead.NonceSize() {
		return nil, fmt.Errorf("oauth: open replay: short blob")
	}
	plain, err := aead.Open(nil, blob[:aead.NonceSize()], blob[aead.NonceSize():], []byte(oldHash))
	if err != nil {
		return nil, fmt.Errorf("oauth: open replay: %w", err)
	}
	var resp TokenResponse
	if err := json.Unmarshal(plain, &resp); err != nil {
		return nil, fmt.Errorf("oauth: open replay: %w", err)
	}
	return &resp, nil
}

func replayAEAD(oldRefresh string) (cipher.AEAD, error) {
	mac := hmac.New(sha256.New, []byte(oldRefresh))
	mac.Write([]byte("slop-haus oauth refresh replay v1"))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return nil, fmt.Errorf("oauth: replay cipher: %w", err)
	}
	return cipher.NewGCM(block)
}
