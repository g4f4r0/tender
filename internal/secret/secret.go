// Package secret encrypts the values the hub keeps in its database: AES-256-GCM with a key from
// the environment. Each value has its own random nonce; the stored form is nonce || sealed.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

const keyBytes = 32

// ParseKey decodes a key from standard base64. It must be 32 bytes, as "openssl rand -base64 32" makes.
func ParseKey(s string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("the key is not base64")
	}
	if len(key) != keyBytes {
		return nil, fmt.Errorf("the key is %d bytes, want %d", len(key), keyBytes)
	}
	return key, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != keyBytes {
		return nil, fmt.Errorf("the key is %d bytes, want %d", len(key), keyBytes)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts plaintext under key with a fresh random nonce.
func Seal(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts a value that Seal made. A wrong key or a damaged value is an error that never
// holds any of the data.
func Open(key, sealed []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("the stored value is too short to be encrypted data")
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("the key does not decrypt this value: it is the wrong key or the value is damaged")
	}
	return plain, nil
}
