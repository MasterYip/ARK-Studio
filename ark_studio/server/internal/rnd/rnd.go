// Package rnd is Python's “secrets“ module for identifiers and tokens.
//
// Normally every call reads crypto/rand.  Under ARK_REPLAY_CLOCK=1 (the
// differential replay, see tests/python/contract/replayclock.py) each named
// stream is deterministic: call n of stream "org" returns the bytes
// sha256("org:n:0") || sha256("org:n:1") || ..., exactly what the Python
// shim substitutes for the “secrets“ module of the same name.  Both servers
// then mint the same user ids, team ids and tokens for the same requests.
package rnd

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"strconv"
	"sync"
)

var (
	replay  = os.Getenv("ARK_REPLAY_CLOCK") == "1"
	mu      sync.Mutex
	counter = map[string]int{}
)

// Bytes is secrets.token_bytes(n) of one stream.
func Bytes(stream string, n int) []byte {
	if !replay {
		buf := make([]byte, n)
		rand.Read(buf) //nolint:errcheck
		return buf
	}
	mu.Lock()
	call := counter[stream]
	counter[stream] = call + 1
	mu.Unlock()
	var out []byte
	for block := 0; len(out) < n; block++ {
		sum := sha256.Sum256([]byte(stream + ":" + strconv.Itoa(call) + ":" + strconv.Itoa(block)))
		out = append(out, sum[:]...)
	}
	return out[:n]
}

// Hex is secrets.token_hex(n).
func Hex(stream string, n int) string { return hex.EncodeToString(Bytes(stream, n)) }

// URLSafe is secrets.token_urlsafe(n).
func URLSafe(stream string, n int) string {
	return base64.RawURLEncoding.EncodeToString(Bytes(stream, n))
}

// UUID4Hex is uuid.uuid4().hex drawn from the "uuid" stream.
func UUID4Hex() string {
	b := Bytes("uuid", 16)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b)
}
