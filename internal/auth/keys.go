package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"
)

type KeyRecord struct {
	Name           string
	TokenHash      []byte
	EncryptedToken []byte
	ExpiresAt      time.Time
}

type KeyManager struct {
	key  []byte
	aead cipher.AEAD
}

func NewKeyManager(key []byte) (*KeyManager, error) {
	if len(key) != 32 {
		return nil, errors.New("encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &KeyManager{key: append([]byte(nil), key...), aead: aead}, nil
}

func (m *KeyManager) Generate(name string, expiresAt time.Time) (string, KeyRecord, error) {
	if name == "" {
		return "", KeyRecord{}, errors.New("key name is required")
	}
	if !expiresAt.After(time.Now()) {
		return "", KeyRecord{}, errors.New("key expiry must be in the future")
	}
	random := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return "", KeyRecord{}, fmt.Errorf("generate token: %w", err)
	}
	token := "em_sk_" + base64.RawURLEncoding.EncodeToString(random)
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", KeyRecord{}, err
	}
	encrypted := m.aead.Seal(nonce, nonce, []byte(token), nil)
	return token, KeyRecord{Name: name, TokenHash: m.Hash(token), EncryptedToken: encrypted, ExpiresAt: expiresAt.UTC()}, nil
}

func (m *KeyManager) Hash(token string) []byte {
	mac := hmac.New(sha256.New, m.key)
	_, _ = mac.Write([]byte(token))
	return mac.Sum(nil)
}
func (m *KeyManager) Matches(token string, expected []byte) bool {
	return hmac.Equal(m.Hash(token), expected)
}
func (m *KeyManager) Decrypt(encrypted []byte) (string, error) {
	if len(encrypted) < m.aead.NonceSize() {
		return "", errors.New("encrypted token is truncated")
	}
	nonce, ciphertext := encrypted[:m.aead.NonceSize()], encrypted[m.aead.NonceSize():]
	plaintext, err := m.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
