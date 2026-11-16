// Package auth ports arkstudio/auth.py: PBKDF2 password hashes, signed
// session cookies, remembered sessions on disk, login rate limits and the
// login CSRF token.
//
// Compatibility with the Python server is by construction: the same
// <root>/.auth-secret signing key, the same token format
// (v1.<sid>.<expires>.<hmac>), the same sid hashing and the same
// .auth-sessions.json file.  A remembered cookie issued by either server is
// valid on the other.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/platform/pathx"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
)

// Constants from auth.py.
const (
	Algorithm          = "pbkdf2_sha256"
	Iterations         = 260_000
	PasswordMaxLength  = 1024
	SessionTTLDefault  = 12 * 3600
	SessionTTLMin      = 300
	SessionTTLMax      = 7 * 24 * 3600
	RememberTTL        = 7 * 24 * 3600
	LoginLimitDefault  = 10
	LoginWindowDefault = 300.0
	LoginCSRFTTL       = 600
	CookieName         = "ark_session"
)

// UsernameRe is USERNAME_RE.
var UsernameRe = regexp.MustCompile(`^[A-Za-z0-9_.@+-]{1,64}\z`)

// Error is AuthError.
type Error struct {
	Status        int
	Message, Code string
	RetryAfter    int
}

func (e *Error) Error() string { return e.Message }

// ConfigError is AuthConfigError.
type ConfigError struct{ Message string }

func (e *ConfigError) Error() string { return e.Message }

func b64(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

func unb64(text string) ([]byte, error) {
	return base64.URLEncoding.DecodeString(text + strings.Repeat("=", (4-len(text)%4)%4))
}

func sha(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// ValidateUsername is validate_username.
func ValidateUsername(value string) (string, error) {
	u := pytext.Strip(value)
	if u == "" {
		return "", &ConfigError{"auth username is required"}
	}
	if !UsernameRe.MatchString(u) {
		return "", &ConfigError{"auth username must be 1-64 characters: letters, digits, . _ @ + -"}
	}
	return u, nil
}

// HashPassword is hash_password.
func HashPassword(password string, iterations int) (string, error) {
	if password == "" {
		return "", &ConfigError{"auth password must not be empty"}
	}
	if pytext.Len(password) > PasswordMaxLength {
		return "", &ConfigError{fmt.Sprintf("auth password must be at most %d characters", PasswordMaxLength)}
	}
	if iterations < 1000 || iterations > 2_000_000 {
		return "", &ConfigError{"PBKDF2 iteration count must be between 1000 and 2000000"}
	}
	salt := make([]byte, 16)
	rand.Read(salt) //nolint:errcheck
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s$%d$%s$%s", Algorithm, iterations, b64(salt), b64(key)), nil
}

func splitHash(stored string) (int, []byte, []byte, bool) {
	parts := strings.SplitN(stored, "$", 4)
	if len(parts) != 4 || parts[0] != Algorithm {
		return 0, nil, nil, false
	}
	n, err := strconv.Atoi(pytext.Strip(parts[1]))
	if err != nil || n < 1000 || n > 2_000_000 {
		return 0, nil, nil, false
	}
	salt, err1 := unb64(parts[2])
	digest, err2 := unb64(parts[3])
	if err1 != nil || err2 != nil {
		return 0, nil, nil, false
	}
	return n, salt, digest, true
}

// IsPasswordHash is is_password_hash.
func IsPasswordHash(value string) bool {
	_, salt, digest, ok := splitHash(value)
	return ok && len(salt) >= 8 && len(digest) == 32
}

// VerifyPassword is verify_password (constant time).
func VerifyPassword(stored, password string) bool {
	if password == "" || pytext.Len(password) > PasswordMaxLength {
		return false
	}
	n, salt, digest, ok := splitHash(stored)
	if !ok || len(digest) != 32 {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, n, 32)
	return err == nil && hmac.Equal(key, digest)
}

// ReadPasswordFile is read_password_file.
func ReadPasswordFile(path string) (string, error) {
	path = pathx.ExpandUser(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", &ConfigError{"cannot read auth password file: " + pyOSError(path, err)}
	}
	text := strings.TrimRight(string(raw), "\r\n")
	if text == "" {
		return "", &ConfigError{"auth password file is empty"}
	}
	if pytext.Len(text) > PasswordMaxLength {
		return "", &ConfigError{"auth password file is larger than the password limit"}
	}
	return text, nil
}

// Session is AuthSession.
type Session struct {
	SidHash   string
	Username  string
	CreatedAt float64
	ExpiresAt int64
	ProjectID string
	Remember  bool
	order     int64 // insertion order of the Python dict (saved file order)
}

// Accounts is the org account backend.
type Accounts interface {
	// Account returns the canonical username and password hash of an
	// enabled account with a password, or ok=false.
	Account(ctx context.Context, username string) (canonical, hash string, ok bool, err error)
}

// Manager is AuthManager.
type Manager struct {
	Username     string
	PasswordHash string
	SessionTTL   int
	LoginLimit   int
	LoginWindow  float64
	CookiePath   string
	Secure       bool
	Clock        func() float64

	accounts  Accounts
	dummyHash string
	secret    []byte
	statePath string

	mu       sync.RWMutex
	sessions map[string]*Session
	inserted int64 // insertion counter (Session.order)
	failMu   sync.Mutex
	failures map[string][]float64
	saveMu   sync.Mutex
}

// Now is time.time().
func Now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// NewManager is AuthManager(username, password_hash=...).
func NewManager(username, passwordHash string, sessionTTL int) (*Manager, error) {
	u, err := ValidateUsername(username)
	if err != nil {
		return nil, err
	}
	if !IsPasswordHash(pytext.Strip(passwordHash)) {
		return nil, &ConfigError{"auth password hash must look like pbkdf2_sha256$<iterations>$<salt>$<digest>"}
	}
	dummy, err := HashPassword(TokenURLSafe(16), Iterations)
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	rand.Read(secret) //nolint:errcheck
	return &Manager{Username: u, PasswordHash: pytext.Strip(passwordHash),
		SessionTTL: max(SessionTTLMin, min(SessionTTLMax, sessionTTL)), LoginLimit: LoginLimitDefault,
		LoginWindow: LoginWindowDefault, CookiePath: "/", Clock: Now, dummyHash: dummy, secret: secret,
		sessions: map[string]*Session{}, failures: map[string][]float64{}}, nil
}

// TokenURLSafe is secrets.token_urlsafe(n).
func TokenURLSafe(n int) string {
	b := make([]byte, n)
	rand.Read(b) //nolint:errcheck
	return b64(b)
}

// SetAccounts attaches the organization backend.
func (m *Manager) SetAccounts(a Accounts) { m.accounts = a }

// EnablePersistence is AuthManager.enable_persistence(folder).
func (m *Manager) EnablePersistence(folder string) error {
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return err
	}
	secretPath := filepath.Join(folder, ".auth-secret")
	var secret []byte
	if raw, err := os.ReadFile(secretPath); err == nil {
		t := strings.TrimSpace(string(raw))
		if t != "" {
			secret, _ = unb64(strings.TrimRight(t, "="))
		}
	}
	if len(secret) < 32 {
		secret = make([]byte, 32)
		rand.Read(secret) //nolint:errcheck
		if err := writePrivate(secretPath, []byte(base64.URLEncoding.EncodeToString(secret))); err != nil {
			return err
		}
	}
	m.secret = secret
	m.statePath = filepath.Join(folder, ".auth-sessions.json")
	m.loadSessions()
	return nil
}

func writePrivate(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

func (m *Manager) loadSessions() {
	raw, err := os.ReadFile(m.statePath)
	if err != nil {
		return
	}
	data, ok := pyjson.LoadsObject(string(raw))
	if !ok {
		return
	}
	items, _ := data.Value("sessions").([]any)
	now := m.Clock()
	for _, it := range items {
		o, ok := it.(*pyjson.Object)
		if !ok {
			return // Python aborts the load on a malformed entry
		}
		created, _ := num(o.Value("created_at"))
		expires, _ := num(o.Value("expires_at"))
		s := &Session{SidHash: pyjson.Str(o.Value("sid_hash")), Username: pyjson.Str(o.Value("username")),
			CreatedAt: created, ExpiresAt: int64(expires), Remember: pyjson.Truthy(o.Value("remember"))}
		if p, ok := o.Value("project_id").(string); ok {
			s.ProjectID = p
		}
		if s.Remember && expires > now {
			m.inserted++
			s.order = m.inserted
			m.sessions[s.SidHash] = s
		}
	}
}

// SetSessionProject is set_session_project: remember the project a browser
// session last opened (persisted for remembered sessions).
func (m *Manager) SetSessionProject(s *Session, projectID string) {
	if s == nil {
		return
	}
	m.mu.Lock()
	if s.ProjectID == projectID {
		m.mu.Unlock()
		return
	}
	s.ProjectID = projectID
	_, stored := m.sessions[s.SidHash]
	m.mu.Unlock()
	if s.Remember && stored {
		m.saveSessions()
	}
}

// saveSessions is _save_sessions (json.dumps defaults: ensure_ascii=True).
func (m *Manager) saveSessions() {
	if m.statePath == "" {
		return
	}
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	now := m.Clock()
	items := []any{}
	m.mu.RLock()
	var keep []*Session
	for _, s := range m.sessions {
		if s.Remember && float64(s.ExpiresAt) > now {
			keep = append(keep, s)
		}
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].order < keep[j].order })
	for _, s := range keep {
		items = append(items, pyjson.NewObject("sid_hash", s.SidHash, "username", s.Username,
			"created_at", s.CreatedAt, "expires_at", s.ExpiresAt, "project_id", s.ProjectID, "remember", s.Remember))
	}
	m.mu.RUnlock()
	body := pyjson.Dumps(pyjson.NewObject("format", "ark-auth-sessions", "version", int64(1), "sessions", items))
	writePrivate(m.statePath, asciiEscape(body)) //nolint:errcheck
}

// asciiEscape turns raw UTF-8 JSON into ensure_ascii=True output.
func asciiEscape(b []byte) []byte {
	var out strings.Builder
	for _, r := range string(b) {
		switch {
		case r < 0x80:
			out.WriteRune(r)
		case r < 0x10000:
			fmt.Fprintf(&out, `\u%04x`, r)
		default:
			r -= 0x10000
			fmt.Fprintf(&out, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		}
	}
	return []byte(out.String())
}

func (m *Manager) sign(payload string) string {
	h := hmac.New(sha256.New, m.secret)
	h.Write([]byte(payload))
	return b64(h.Sum(nil))
}

func (m *Manager) tokenParts(token string) (string, int64, bool) {
	if token == "" || len(token) > 500 {
		return "", 0, false
	}
	i := strings.LastIndex(token, ".")
	if i < 0 {
		return "", 0, false
	}
	payload, sig := token[:i], token[i+1:]
	parts := strings.Split(payload, ".")
	if len(parts) != 3 {
		return "", 0, false
	}
	exp, err := pytext.Int(parts[2])
	if err != nil || parts[0] != "v1" || parts[1] == "" || len(parts[1]) > 200 {
		return "", 0, false
	}
	if !hmac.Equal([]byte(m.sign(payload)), []byte(sig)) {
		return "", 0, false
	}
	return parts[1], exp, true
}

// IssueSession is issue_session.
func (m *Manager) IssueSession(username string, remember bool) (string, *Session) {
	now := m.Clock()
	sid := TokenURLSafe(24)
	ttl := m.SessionTTL
	if remember {
		ttl = RememberTTL
	}
	expires := int64(now) + int64(ttl)
	payload := fmt.Sprintf("v1.%s.%d", sid, expires)
	token := payload + "." + m.sign(payload)
	s := &Session{SidHash: sha(sid), Username: username, CreatedAt: now, ExpiresAt: expires, Remember: remember}
	m.mu.Lock()
	m.inserted++
	s.order = m.inserted
	m.sessions[s.SidHash] = s
	for k, old := range m.sessions {
		if float64(old.ExpiresAt) <= now-3600 {
			delete(m.sessions, k)
		}
	}
	m.mu.Unlock()
	if remember {
		m.saveSessions()
	}
	return token, s
}

// Authenticate is authenticate(token).
func (m *Manager) Authenticate(ctx context.Context, token string) (*Session, error) {
	sid, exp, ok := m.tokenParts(token)
	if !ok {
		return nil, nil
	}
	m.mu.RLock()
	s := m.sessions[sha(sid)]
	m.mu.RUnlock()
	if s == nil {
		return nil, nil
	}
	now := m.Clock()
	if float64(exp) <= now || float64(s.ExpiresAt) <= now {
		m.drop(s.SidHash)
		return nil, nil
	}
	canonical, _, found, err := m.account(ctx, s.Username)
	if err != nil {
		return nil, err
	}
	if !found || canonical != s.Username {
		m.drop(s.SidHash)
		return nil, nil
	}
	return s, nil
}

func (m *Manager) account(ctx context.Context, username string) (string, string, bool, error) {
	candidate := pytext.Strip(username)
	if candidate == "" {
		return "", "", false, nil
	}
	if m.accounts != nil {
		name, hash, ok, err := m.accounts.Account(ctx, candidate)
		if err != nil || !ok || name != candidate {
			return "", "", false, err
		}
		return name, hash, true, nil
	}
	if candidate == m.Username {
		return candidate, m.PasswordHash, true, nil
	}
	return "", "", false, nil
}

func (m *Manager) drop(key string) bool {
	m.mu.Lock()
	s, ok := m.sessions[key]
	delete(m.sessions, key)
	m.mu.Unlock()
	if ok && s.Remember {
		m.saveSessions()
	}
	return ok
}

// RevokeUser is revoke_user(username, except_sid): drop every session of
// one user except the given sid hash; returns the count.
func (m *Manager) RevokeUser(username, exceptSid string) int {
	target := strings.TrimFunc(username, pytext.IsSpace)
	removed := 0
	m.mu.Lock()
	for key, s := range m.sessions {
		if key == exceptSid {
			continue
		}
		if s.Username == target {
			delete(m.sessions, key)
			removed++
		}
	}
	m.mu.Unlock()
	if removed > 0 {
		m.saveSessions()
	}
	return removed
}

// Revoke is revoke(token).
func (m *Manager) Revoke(token string) bool {
	sid, _, ok := m.tokenParts(token)
	if !ok {
		return false
	}
	return m.drop(sha(sid))
}

func (m *Manager) bucket(key string, prune bool) int {
	now := m.Clock()
	b := m.failures[key]
	for len(b) > 0 && now-b[0] >= m.LoginWindow {
		b = b[1:]
	}
	if prune && len(b) == 0 {
		delete(m.failures, key)
	} else {
		m.failures[key] = b
	}
	return len(b)
}

func (m *Manager) checkLimit(key string) error {
	if len(m.failures) > 4096 {
		now := m.Clock()
		for name, b := range m.failures {
			if len(b) == 0 || now-b[len(b)-1] >= m.LoginWindow {
				delete(m.failures, name)
			}
		}
	}
	if m.bucket(key, false) >= m.LoginLimit {
		retry := max(1, int(m.LoginWindow-(m.Clock()-m.failures[key][0])))
		return &Error{429, "Too many sign-in attempts. Wait a few minutes and try again.", "AUTH_RATE_LIMITED", retry}
	}
	return nil
}

// VerifyCredentials is verify_credentials.
func (m *Manager) VerifyCredentials(ctx context.Context, username, password, clientIP string) (string, error) {
	if clientIP == "" {
		clientIP = "unknown"
	}
	ipKey := "ip:" + clientIP
	userKey := "user:" + strings.ToLower(pytext.Strip(username))
	m.failMu.Lock()
	if err := m.checkLimit(ipKey); err != nil {
		m.failMu.Unlock()
		return "", err
	}
	if err := m.checkLimit(userKey); err != nil {
		m.failMu.Unlock()
		return "", err
	}
	m.failMu.Unlock()
	name, stored, found, err := m.account(ctx, username)
	if err != nil {
		return "", err
	}
	if !found {
		stored = m.dummyHash
	}
	ok := VerifyPassword(stored, password) // PBKDF2 runs outside every lock
	m.failMu.Lock()
	defer m.failMu.Unlock()
	if found && ok {
		m.bucket(ipKey, true)
		return name, nil
	}
	now := m.Clock()
	m.bucket(ipKey, false)
	m.bucket(userKey, false)
	m.failures[ipKey] = append(m.failures[ipKey], now)
	m.failures[userKey] = append(m.failures[userKey], now)
	return "", &Error{401, "Invalid username or password.", "AUTH_INVALID", 0}
}

// Login is login().
func (m *Manager) Login(ctx context.Context, username, password, clientIP string, remember bool) (string, *Session, error) {
	name, err := m.VerifyCredentials(ctx, username, password, clientIP)
	if err != nil {
		return "", nil, err
	}
	token, s := m.IssueSession(name, remember)
	return token, s, nil
}

// LoginCSRF is login_csrf().
func (m *Manager) LoginCSRF() string {
	payload := fmt.Sprintf("csrf.%s.%d", TokenURLSafe(18), int64(m.Clock())+LoginCSRFTTL)
	return payload + "." + m.sign(payload)
}

// VerifyLoginCSRF is verify_login_csrf.
func (m *Manager) VerifyLoginCSRF(token string) bool {
	if token == "" || len(token) > 300 {
		return false
	}
	i := strings.LastIndex(token, ".")
	if i < 0 {
		return false
	}
	payload, sig := token[:i], token[i+1:]
	parts := strings.Split(payload, ".")
	if len(parts) != 3 || parts[0] != "csrf" {
		return false
	}
	exp, err := pytext.Int(parts[2])
	if err != nil || !hmac.Equal([]byte(m.sign(payload)), []byte(sig)) {
		return false
	}
	return float64(exp) > m.Clock()
}

// CookieHeader is cookie_header(token, secure=...).
func (m *Manager) CookieHeader(token string, secure bool) string {
	maxAge := int64(m.SessionTTL)
	if _, exp, ok := m.tokenParts(token); ok {
		maxAge = exp - int64(m.Clock())
	}
	parts := []string{CookieName + "=" + token, "Path=" + m.CookiePath, "HttpOnly", "SameSite=Lax",
		"Max-Age=" + strconv.FormatInt(max(0, maxAge), 10)}
	if secure {
		parts = append(parts, "Secure")
	}
	return strings.Join(parts, "; ")
}

// ClearCookieHeader is clear_cookie_header.
func (m *Manager) ClearCookieHeader(secure bool) string {
	parts := []string{CookieName + "=", "Path=" + m.CookiePath, "HttpOnly", "SameSite=Lax", "Max-Age=0",
		"Expires=Thu, 01 Jan 1970 00:00:00 GMT"}
	if secure {
		parts = append(parts, "Secure")
	}
	return strings.Join(parts, "; ")
}

// ParseCookie is parse_cookie(header, name).
func ParseCookie(header, name string) string {
	for _, part := range strings.Split(header, ";") {
		key, value, ok := strings.Cut(pytext.Strip(part), "=")
		if !ok || key != name {
			continue
		}
		value = pytext.Strip(value)
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		return value
	}
	return ""
}

// NormalizeCookiePath is normalize_cookie_path.
func NormalizeCookiePath(value string) string {
	text := pytext.Strip(value)
	if text == "" {
		text = "/"
	}
	if strings.ContainsAny(text, "\r\n\t \\;,") {
		return "/"
	}
	if !strings.HasPrefix(text, "/") || strings.Contains(text, "//") {
		return "/"
	}
	for _, seg := range strings.Split(text, "/") {
		if seg == ".." {
			return "/"
		}
	}
	if !strings.HasSuffix(text, "/") {
		text += "/"
	}
	return text
}

// SafeNextPath is safe_next_path.
func SafeNextPath(value, def string) string {
	text := pytext.Strip(value)
	if text == "" || pytext.Len(text) > 2000 || strings.ContainsAny(text, "\r\n\t\\\x00") {
		return def
	}
	if !strings.HasPrefix(text, "/") || strings.HasPrefix(text, "//") {
		return def
	}
	lowered := strings.ToLower(text)
	if strings.HasPrefix(lowered, "/%2f") || strings.HasPrefix(lowered, "/%5c") {
		return def
	}
	p := pyurl.Split(text)
	if p.Scheme != "" || p.Netloc != "" || !strings.HasPrefix(p.Path, "/") {
		return def
	}
	for _, seg := range strings.Split(p.Path, "/") {
		if seg == ".." {
			return def
		}
	}
	return text
}
