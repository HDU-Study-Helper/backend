// Package credential provides encrypted, identity-bound workspace snapshots.
// Callers own key management, exclusive workspace access, and plaintext lifetime.
package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
)

var (
	ErrKey       = errors.New("credential: invalid encryption key")
	ErrIdentity  = errors.New("credential: invalid identity")
	ErrEnvelope  = errors.New("credential: invalid encrypted snapshot")
	ErrArchive   = errors.New("credential: invalid workspace archive")
	ErrWorkspace = errors.New("credential: workspace operation failed")
)

const (
	MaxBytes   = 16 << 20
	MaxEntries = 4096
	// MaxArchiveBytes includes bounded tar headers and padding.
	MaxArchiveBytes      = MaxBytes + MaxEntries*1024 + 1024
	envelopeVersion byte = 1
)

// Vault retains an expanded AES key, independent of the caller's key slice.
// A Vault can be used concurrently. It does not persist keys or plaintext.
type Vault struct{ aead cipher.AEAD }

func New(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, ErrKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrKey
	}
	return &Vault{aead: aead}, nil
}

func identityAAD(user, conn string) ([]byte, error) {
	if user == "" || conn == "" || len(user) > 4096 || len(conn) > 4096 {
		return nil, ErrIdentity
	}
	// Length prefixes prevent ambiguous identity concatenations. The format
	// version is authenticated so future formats cannot reinterpret ciphertext.
	aad := []byte{'D', 'W', 'S', 'V', envelopeVersion}
	aad = binary.BigEndian.AppendUint32(aad, uint32(len(user)))
	aad = append(aad, user...)
	aad = binary.BigEndian.AppendUint32(aad, uint32(len(conn)))
	aad = append(aad, conn...)
	return aad, nil
}

// Seal returns magic, format version, random nonce, and authenticated ciphertext.
func (v *Vault) Seal(user, conn string, plaintext []byte) ([]byte, error) {
	aad, err := identityAAD(user, conn)
	if err != nil {
		return nil, err
	}
	if v == nil || v.aead == nil || len(plaintext) > MaxArchiveBytes {
		return nil, ErrEnvelope
	}
	prefix := []byte{'D', 'W', 'S', 'V', envelopeVersion}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrEnvelope
	}
	prefix = append(prefix, nonce...)
	return v.aead.Seal(prefix, nonce, plaintext, aad), nil
}

func (v *Vault) Open(user, conn string, sealed []byte) ([]byte, error) {
	aad, err := identityAAD(user, conn)
	if err != nil {
		return nil, err
	}
	if v == nil || v.aead == nil {
		return nil, ErrEnvelope
	}
	n := v.aead.NonceSize()
	if len(sealed) < 5+n+v.aead.Overhead() || len(sealed) > 5+n+v.aead.Overhead()+MaxArchiveBytes || string(sealed[:4]) != "DWSV" || sealed[4] != envelopeVersion {
		return nil, ErrEnvelope
	}
	plain, err := v.aead.Open(nil, sealed[5:5+n], sealed[5+n:], aad)
	if err != nil {
		return nil, ErrEnvelope
	}
	return plain, nil
}

func (v *Vault) SealDirectory(user, conn, root string) ([]byte, error) {
	plain, err := Pack(root)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	return v.Seal(user, conn, plain)
}

// OpenDirectory decrypts only in memory before extracting to an empty directory.
// The caller must remove the plaintext workspace after the consuming process exits.
func (v *Vault) OpenDirectory(user, conn string, sealed []byte, destination string) error {
	plain, err := v.Open(user, conn, sealed)
	if err != nil {
		return err
	}
	defer clear(plain)
	return Unpack(plain, destination)
}
