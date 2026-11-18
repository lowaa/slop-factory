// Package auth wraps the crypto/JWT primitives shared across services
// (password hashing, opaque tokens, JWT signing) behind an
// application-specific API rather than calling the libraries inline. It has no
// DB dependency.
package auth

import "golang.org/x/crypto/bcrypt"

// ErrPasswordTooLong is bcrypt's 72-byte ceiling. Bytes, not characters,
// so a length check on a form field cannot stand in for it.
var ErrPasswordTooLong = bcrypt.ErrPasswordTooLong

func HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func VerifyPassword(hash, plain string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
}
