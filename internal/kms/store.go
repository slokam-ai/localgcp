package kms

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"hash"
	"sort"
	"strings"
	"sync"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// storedKeyRing holds a key ring in memory.
type storedKeyRing struct {
	Name       string
	CreateTime *timestamppb.Timestamp
}

// storedCryptoKey holds a crypto key and its versions.
type storedCryptoKey struct {
	Name       string
	Purpose    kmspb.CryptoKey_CryptoKeyPurpose
	Algorithm  kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm
	CreateTime *timestamppb.Timestamp
	PrimaryVer int32 // version number of the primary version
}

// storedVersion holds the actual key material.
type storedVersion struct {
	Name       string
	State      kmspb.CryptoKeyVersion_CryptoKeyVersionState
	Algorithm  kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm
	CreateTime *timestamppb.Timestamp

	// Key material (only one is set depending on purpose).
	symKey     []byte           // for ENCRYPT_DECRYPT
	rsaKey     *rsa.PrivateKey  // for ASYMMETRIC_SIGN/DECRYPT with RSA
	ecKey      *ecdsa.PrivateKey // for ASYMMETRIC_SIGN with EC
	hmacKey    []byte           // for MAC
}

// Store is the in-memory KMS key store.
type Store struct {
	mu       sync.RWMutex
	keyRings map[string]*storedKeyRing    // full name -> key ring
	keys     map[string]*storedCryptoKey  // full name -> crypto key
	versions map[string]*storedVersion    // full name -> version
	nextVer  map[string]int32             // crypto key name -> next version number
}

func NewStore() *Store {
	return &Store{
		keyRings: make(map[string]*storedKeyRing),
		keys:     make(map[string]*storedCryptoKey),
		versions: make(map[string]*storedVersion),
		nextVer:  make(map[string]int32),
	}
}

// --- KeyRing ---

func (s *Store) CreateKeyRing(name string) (*storedKeyRing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.keyRings[name]; exists {
		return nil, fmt.Errorf("already exists")
	}
	kr := &storedKeyRing{Name: name, CreateTime: timestamppb.Now()}
	s.keyRings[name] = kr
	return kr, nil
}

func (s *Store) GetKeyRing(name string) (*storedKeyRing, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	kr, ok := s.keyRings[name]
	return kr, ok
}

func (s *Store) ListKeyRings(parent string) []*storedKeyRing {
	s.mu.RLock()
	defer s.mu.RUnlock()
	prefix := parent + "/keyRings/"
	var result []*storedKeyRing
	for name, kr := range s.keyRings {
		if strings.HasPrefix(name, prefix) {
			result = append(result, kr)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// --- CryptoKey ---

func (s *Store) CreateCryptoKey(name string, purpose kmspb.CryptoKey_CryptoKeyPurpose, algorithm kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm) (*storedCryptoKey, *storedVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.keys[name]; exists {
		return nil, nil, fmt.Errorf("already exists")
	}

	if algorithm == kmspb.CryptoKeyVersion_CRYPTO_KEY_VERSION_ALGORITHM_UNSPECIFIED {
		algorithm = defaultAlgorithm(purpose)
	}

	now := timestamppb.Now()
	ck := &storedCryptoKey{
		Name:       name,
		Purpose:    purpose,
		Algorithm:  algorithm,
		CreateTime: now,
		PrimaryVer: 1,
	}
	s.keys[name] = ck
	s.nextVer[name] = 1

	// Create the first version.
	ver, err := s.createVersionLocked(name, algorithm, now)
	if err != nil {
		delete(s.keys, name)
		return nil, nil, err
	}

	return ck, ver, nil
}

func (s *Store) GetCryptoKey(name string) (*storedCryptoKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ck, ok := s.keys[name]
	return ck, ok
}

func (s *Store) ListCryptoKeys(parent string) []*storedCryptoKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	prefix := parent + "/cryptoKeys/"
	var result []*storedCryptoKey
	for name, ck := range s.keys {
		if strings.HasPrefix(name, prefix) {
			result = append(result, ck)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// --- CryptoKeyVersion ---

func (s *Store) createVersionLocked(keyName string, alg kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm, now *timestamppb.Timestamp) (*storedVersion, error) {
	verNum := s.nextVer[keyName]
	if verNum == 0 {
		verNum = 1
		s.nextVer[keyName] = 2
	}
	verName := fmt.Sprintf("%s/cryptoKeyVersions/%d", keyName, verNum)
	s.nextVer[keyName] = verNum + 1

	ver := &storedVersion{
		Name:       verName,
		State:      kmspb.CryptoKeyVersion_ENABLED,
		Algorithm:  alg,
		CreateTime: now,
	}

	// Generate key material.
	switch {
	case isSymmetric(alg):
		key := make([]byte, 32)
		rand.Read(key)
		ver.symKey = key
	case isRSA(alg):
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, fmt.Errorf("generate RSA key: %w", err)
		}
		ver.rsaKey = key
	case isEC(alg):
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate EC key: %w", err)
		}
		ver.ecKey = key
	case isHMAC(alg):
		key := make([]byte, 32)
		rand.Read(key)
		ver.hmacKey = key
	default:
		// Default to symmetric.
		key := make([]byte, 32)
		rand.Read(key)
		ver.symKey = key
	}

	s.versions[verName] = ver
	return ver, nil
}

func (s *Store) GetVersion(name string) (*storedVersion, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.versions[name]
	return v, ok
}

func (s *Store) ListVersions(parent string) []*storedVersion {
	s.mu.RLock()
	defer s.mu.RUnlock()
	prefix := parent + "/cryptoKeyVersions/"
	var result []*storedVersion
	for name, v := range s.versions {
		if strings.HasPrefix(name, prefix) {
			result = append(result, v)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) DestroyVersion(name string) (*storedVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.versions[name]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	v.State = kmspb.CryptoKeyVersion_DESTROYED
	v.symKey = nil
	v.rsaKey = nil
	v.ecKey = nil
	v.hmacKey = nil
	return v, nil
}

// PrimaryVersion returns the primary version for a crypto key.
func (s *Store) PrimaryVersion(keyName string) (*storedVersion, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ck, ok := s.keys[keyName]
	if !ok {
		return nil, false
	}
	verName := fmt.Sprintf("%s/cryptoKeyVersions/%d", keyName, ck.PrimaryVer)
	v, ok := s.versions[verName]
	return v, ok
}

// --- Crypto operations ---

// Encrypt performs symmetric encryption using XOR with the key (sufficient for emulation).
func (s *Store) Encrypt(versionName string, plaintext []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.versions[versionName]
	if !ok {
		return nil, fmt.Errorf("version not found")
	}
	if v.State != kmspb.CryptoKeyVersion_ENABLED {
		return nil, fmt.Errorf("version is not enabled")
	}
	if v.symKey == nil {
		return nil, fmt.Errorf("not a symmetric key")
	}
	return xorCrypt(v.symKey, plaintext), nil
}

// Decrypt performs symmetric decryption.
func (s *Store) Decrypt(versionName string, ciphertext []byte) ([]byte, error) {
	// XOR is its own inverse.
	return s.Encrypt(versionName, ciphertext)
}

// AsymmetricSign signs data with the version's private key.
func (s *Store) AsymmetricSign(versionName string, digest []byte, digestType crypto.Hash) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.versions[versionName]
	if !ok {
		return nil, fmt.Errorf("version not found")
	}
	if v.State != kmspb.CryptoKeyVersion_ENABLED {
		return nil, fmt.Errorf("version is not enabled")
	}

	if v.rsaKey != nil {
		return rsa.SignPKCS1v15(rand.Reader, v.rsaKey, digestType, digest)
	}
	if v.ecKey != nil {
		return ecdsa.SignASN1(rand.Reader, v.ecKey, digest)
	}
	return nil, fmt.Errorf("not an asymmetric key")
}

// GetPublicKeyPEM returns the PEM-encoded public key for an asymmetric version.
func (s *Store) GetPublicKeyPEM(versionName string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.versions[versionName]
	if !ok {
		return "", fmt.Errorf("version not found")
	}
	if v.State != kmspb.CryptoKeyVersion_ENABLED {
		return "", fmt.Errorf("version is not enabled")
	}

	var pubKeyBytes []byte
	var err error
	if v.rsaKey != nil {
		pubKeyBytes, err = x509.MarshalPKIXPublicKey(&v.rsaKey.PublicKey)
	} else if v.ecKey != nil {
		pubKeyBytes, err = x509.MarshalPKIXPublicKey(&v.ecKey.PublicKey)
	} else {
		return "", fmt.Errorf("not an asymmetric key")
	}
	if err != nil {
		return "", fmt.Errorf("marshal public key: %w", err)
	}

	block := &pem.Block{Type: "PUBLIC KEY", Bytes: pubKeyBytes}
	return string(pem.EncodeToMemory(block)), nil
}

// MacSign computes an HMAC.
func (s *Store) MacSign(versionName string, data []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.versions[versionName]
	if !ok {
		return nil, fmt.Errorf("version not found")
	}
	if v.State != kmspb.CryptoKeyVersion_ENABLED {
		return nil, fmt.Errorf("version is not enabled")
	}
	if v.hmacKey == nil {
		return nil, fmt.Errorf("not a MAC key")
	}
	return computeHMAC(v.hmacKey, data), nil
}

// --- Helpers ---

func xorCrypt(key, data []byte) []byte {
	result := make([]byte, len(data))
	for i := range data {
		result[i] = data[i] ^ key[i%len(key)]
	}
	return result
}

func computeHMAC(key, data []byte) []byte {
	h := sha256.New()
	h.Write(key)
	h.Write(data)
	return h.Sum(nil)
}

// DigestHash returns the hash function and digest for the given data based on algorithm.
func DigestHash(alg kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm, data []byte) (crypto.Hash, []byte) {
	var h hash.Hash
	var hashType crypto.Hash
	if strings.Contains(alg.String(), "SHA384") || strings.Contains(alg.String(), "SHA_384") {
		h = sha512.New384()
		hashType = crypto.SHA384
	} else if strings.Contains(alg.String(), "SHA512") || strings.Contains(alg.String(), "SHA_512") {
		h = sha512.New()
		hashType = crypto.SHA512
	} else {
		h = sha256.New()
		hashType = crypto.SHA256
	}
	h.Write(data)
	return hashType, h.Sum(nil)
}

func defaultAlgorithm(purpose kmspb.CryptoKey_CryptoKeyPurpose) kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm {
	switch purpose {
	case kmspb.CryptoKey_ENCRYPT_DECRYPT:
		return kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION
	case kmspb.CryptoKey_ASYMMETRIC_SIGN:
		return kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256
	case kmspb.CryptoKey_ASYMMETRIC_DECRYPT:
		return kmspb.CryptoKeyVersion_RSA_DECRYPT_OAEP_2048_SHA256
	case kmspb.CryptoKey_MAC:
		return kmspb.CryptoKeyVersion_HMAC_SHA256
	default:
		return kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION
	}
}

func isSymmetric(alg kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm) bool {
	return alg == kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION
}

func isRSA(alg kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm) bool {
	return strings.HasPrefix(alg.String(), "RSA_")
}

func isEC(alg kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm) bool {
	return strings.HasPrefix(alg.String(), "EC_")
}

func isHMAC(alg kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm) bool {
	return strings.HasPrefix(alg.String(), "HMAC_")
}
