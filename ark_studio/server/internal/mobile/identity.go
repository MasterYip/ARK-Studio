package mobile

// Server identity (Go only): a long-lived P-256 key per ARK server. A phone
// stores the server id and public key when it pairs. A Cloudflare quick
// tunnel gets a new address on every restart; before the phone sends its
// session token to a new address it asks that address to sign a fresh nonce
// and checks the signature against the stored key, so a look-alike URL
// cannot collect tokens. The key never leaves the server.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// IdentityContext prefixes every signed message (domain separation).
const IdentityContext = "ark-server-identity/1"

// Identity is the server's signing key.
type Identity struct {
	ID        string // first 20 hex digits of SHA-256(public key DER)
	PublicKey string // base64 (standard) SubjectPublicKeyInfo DER
	key       *ecdsa.PrivateKey
}

type identityFile struct {
	PrivateKey string `json:"private_key"` // base64 PKCS#8 DER
}

var identityMu sync.Mutex

// LoadIdentity reads path, creating a new key (0600) the first time.
func LoadIdentity(path string) (*Identity, error) {
	identityMu.Lock()
	defer identityMu.Unlock()
	if raw, err := os.ReadFile(path); err == nil {
		var f identityFile
		if json.Unmarshal(raw, &f) == nil {
			if der, err := base64.StdEncoding.DecodeString(f.PrivateKey); err == nil {
				if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
					if ek, ok := k.(*ecdsa.PrivateKey); ok && ek.Curve == elliptic.P256() {
						return newIdentity(ek)
					}
				}
			}
		}
		return nil, errors.New("server identity file is damaged: " + path)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(identityFile{base64.StdEncoding.EncodeToString(der)})
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return newIdentity(key)
}

func newIdentity(key *ecdsa.PrivateKey) (*Identity, error) {
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(pub)
	return &Identity{ID: hex.EncodeToString(sum[:])[:20], PublicKey: base64.StdEncoding.EncodeToString(pub), key: key}, nil
}

var nonceRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// ValidNonce reports whether a phone's nonce is acceptable (16-128 URL-safe
// characters).
func ValidNonce(nonce string) bool { return nonceRe.MatchString(nonce) }

// IdentityMessage is the signed text: context, server id, the host the phone
// connected to, and its nonce, one per line.
func IdentityMessage(id, host, nonce string) []byte {
	return []byte(IdentityContext + "\n" + id + "\n" + host + "\n" + nonce)
}

// Sign returns the base64 ASN.1 DER ECDSA-SHA256 signature of
// IdentityMessage(id, host, nonce).
func (i *Identity) Sign(host, nonce string) (string, error) {
	sum := sha256.Sum256(IdentityMessage(i.ID, host, nonce))
	sig, err := ecdsa.SignASN1(rand.Reader, i.key, sum[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// VerifyIdentity checks a signature against a stored public key (what the
// phone does; used by tests and `ark-server` tooling).
func VerifyIdentity(publicKey, id, host, nonce, signature string) bool {
	der, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		return false
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return false
	}
	ek, ok := k.(*ecdsa.PublicKey)
	if !ok {
		return false
	}
	sum := sha256.Sum256(der)
	if hex.EncodeToString(sum[:])[:20] != id {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	msg := sha256.Sum256(IdentityMessage(id, host, nonce))
	return ecdsa.VerifyASN1(ek, msg[:], sig)
}
