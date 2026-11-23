// Package mobile ports the pairing and device-session store of
// arkstudio/mobile.py (PairingManager) plus its URL helpers.
package mobile

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/rnd"
)

const (
	DefaultPermission = "full"
	PairingTTLDefault = 600
	PairingTTLMax     = 3600
	SessionTTLDefault = 12 * 3600
	SessionTTLMax     = 365 * 24 * 3600
	SessionPermanent  = 0
	PermanentHorizon  = 100 * 365 * 24 * 3600
	claimLimit        = 8
	claimWindow       = 60.0
	createLimit       = 30
	createWindow      = 3600.0
)

var permanentWords = map[string]bool{"permanent": true, "never": true, "forever": true, "until_revoked": true, "0": true}

// Error is MobileError.
type Error struct {
	Status        int
	Message, Code string
}

func (e *Error) Error() string { return e.Message }

func merr(status int, msg, code string) *Error { return &Error{status, msg, code} }

// Clock is PairingManager.clock (time.time()); the replay pins it.
var Clock = func() float64 {
	if t, ok := clock.Replay(); ok {
		return float64(t.UnixNano()) / 1e9
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func sha(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// ISO is mobile._iso.
func ISO(epoch float64) string {
	sec := math.Floor(epoch)
	return time.Unix(int64(sec), 0).UTC().Format("2006-01-02T15:04:05") + "+00:00"
}

// NormalizePublicURL is mobile.normalize_public_url.
func NormalizePublicURL(value string) (string, error) {
	text := pytext.Strip(value)
	if text == "" {
		return "", merr(400, "public URL is empty", "INVALID_PUBLIC_URL")
	}
	if pytext.Len(text) > 500 || strings.ContainsAny(text, "\r\n\t ") {
		return "", merr(400, "public URL contains whitespace", "INVALID_PUBLIC_URL")
	}
	p := pyurl.Parse(text)
	if p.Scheme != "http" && p.Scheme != "https" {
		return "", merr(400, "public URL must start with http:// or https://", "INVALID_PUBLIC_URL")
	}
	host, port, userinfo, perr := splitNetloc(p.Netloc)
	if host == "" {
		return "", merr(400, "public URL has no host", "INVALID_PUBLIC_URL")
	}
	if userinfo {
		return "", merr(400, "public URL must not contain credentials", "INVALID_PUBLIC_URL")
	}
	if p.Query != "" || p.Fragment != "" {
		return "", merr(400, "public URL must not contain a query or fragment", "INVALID_PUBLIC_URL")
	}
	if perr {
		return "", merr(400, "public URL has an invalid port", "INVALID_PUBLIC_URL")
	}
	if port != nil && (*port < 1 || *port > 65535) {
		return "", merr(400, "public URL port is out of range", "INVALID_PUBLIC_URL")
	}
	path := p.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	netloc := host
	if port != nil && *port != 0 {
		netloc += ":" + strconv.Itoa(*port)
	}
	return p.Scheme + "://" + netloc + path, nil
}

// splitNetloc is urlparse's hostname/port/username view of a netloc:
// hostname lower-cased without brackets, port (nil when absent), whether
// credentials are present and whether the port is invalid (ValueError).
func splitNetloc(netloc string) (host string, port *int, userinfo bool, bad bool) {
	if i := strings.LastIndex(netloc, "@"); i >= 0 {
		userinfo = netloc[:i] != "" || true
		netloc = netloc[i+1:]
	}
	hostport := netloc
	portText := ""
	if strings.HasPrefix(hostport, "[") {
		end := strings.Index(hostport, "]")
		if end < 0 {
			return "", nil, userinfo, true
		}
		host = hostport[1:end]
		rest := hostport[end+1:]
		if strings.HasPrefix(rest, ":") {
			portText = rest[1:]
		}
	} else {
		if i := strings.LastIndex(hostport, ":"); i >= 0 {
			host, portText = hostport[:i], hostport[i+1:]
		} else {
			host = hostport
		}
	}
	host = strings.ToLower(host)
	if portText != "" {
		for _, r := range portText {
			if r < '0' || r > '9' {
				return host, nil, userinfo, true
			}
		}
		n, err := strconv.Atoi(portText)
		if err != nil || n > 65535 {
			return host, nil, userinfo, true
		}
		port = &n
	}
	return host, port, userinfo, false
}

var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

func isIPv4(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil || strings.Contains(s, ":") {
		return false
	}
	// ipaddress.IPv4Address rejects leading zeros.
	for _, part := range strings.Split(s, ".") {
		if len(part) > 1 && part[0] == '0' {
			return false
		}
	}
	return true
}

// ValidateTunnelHost is mobile.validate_tunnel_host.
func ValidateTunnelHost(value string) (string, error) {
	host := pytext.Strip(value)
	if host == "" {
		return "", merr(400, "tunnel host is required", "INVALID_TUNNEL_HOST")
	}
	if pytext.Len(host) > 253 || strings.ContainsAny(host, "/\\ \r\n\t@") {
		return "", merr(400, "tunnel host must be a bare hostname or IP address", "INVALID_TUNNEL_HOST")
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		inner := host[1 : len(host)-1]
		ip := net.ParseIP(inner)
		if ip == nil || !strings.Contains(inner, ":") {
			return "", merr(400, "tunnel host is not a valid IPv6 address", "INVALID_TUNNEL_HOST")
		}
		return "[" + inner + "]", nil
	}
	if strings.Contains(host, ":") {
		return "", merr(400, "tunnel host must not include a port", "INVALID_TUNNEL_HOST")
	}
	if isIPv4(host) {
		return host, nil
	}
	if !hostnameRe.MatchString(host) {
		return "", merr(400, "tunnel host is not a valid hostname", "INVALID_TUNNEL_HOST")
	}
	return host, nil
}

// pyIntPort is int(value) for ports (ok=false: TypeError/ValueError).
func pyIntPort(v any) (int64, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case int64:
		return x, true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return int64(x), true
	case *big.Int:
		return math.MaxInt64, true
	case string:
		n, err := pytext.Int(x)
		if err != nil {
			if _, ok := err.(*pytext.IntError); ok {
				return 0, false
			}
			return math.MaxInt64, true
		}
		return n, true
	}
	return 0, false
}

// ValidatePort is mobile.validate_port (hasDefault: default is not None).
func ValidatePort(value any, name string, def int, hasDefault bool) (int, error) {
	n, ok := pyIntPort(value)
	if !ok {
		if hasDefault && (value == nil || value == "") {
			return def, nil
		}
		return 0, merr(400, name+" must be a number", "INVALID_PORT")
	}
	if n < 1 || n > 65535 {
		return 0, merr(400, name+" must be between 1 and 65535", "INVALID_PORT")
	}
	return int(n), nil
}

// IsLoopbackHost is mobile.is_loopback_host.
func IsLoopbackHost(host string) bool {
	text := strings.ToLower(strings.Trim(pytext.Strip(host), "[]"))
	return text == "localhost" || text == "127.0.0.1" || text == "::1" || text == "0:0:0:0:0:0:0:1"
}

// BuildPairingURL is mobile.build_pairing_url.
func BuildPairingURL(base, token string) (string, error) {
	normalized, err := NormalizePublicURL(base)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(normalized, "/") + "/#pair=" + url.QueryEscape(token), nil
}

// ------------------------------------------------------------------ records

// Pairing is PairingRecord.
type Pairing struct {
	ID          string   `json:"id"`
	TokenHash   string   `json:"token_hash"`
	ProjectID   string   `json:"project_id"`
	ProjectName string   `json:"project_name"`
	Permission  string   `json:"permission"`
	CreatedAt   float64  `json:"created_at"`
	ExpiresAt   float64  `json:"expires_at"`
	ClaimedAt   *float64 `json:"claimed_at"`
	RevokedAt   *float64 `json:"revoked_at"`
	SessionTTL  int64    `json:"session_ttl"`
	User        string   `json:"user"`
	Source      string   `json:"source"`
}

// Public is PairingRecord.public.
func (r *Pairing) Public() *pyjson.Object {
	state := "pending"
	if r.RevokedAt != nil && *r.RevokedAt != 0 {
		state = "revoked"
	} else if r.ClaimedAt != nil && *r.ClaimedAt != 0 {
		state = "claimed"
	}
	isoOpt := func(v *float64) any {
		if v == nil || *v == 0 {
			return nil
		}
		return ISO(*v)
	}
	var ttl any = r.SessionTTL
	if r.SessionTTL == 0 {
		ttl = nil
	}
	return pyjson.NewObject("id", r.ID, "permission", r.Permission, "project", r.ProjectName, "user", r.User,
		"created_at", ISO(r.CreatedAt), "expires_at", ISO(r.ExpiresAt), "claimed_at", isoOpt(r.ClaimedAt),
		"revoked_at", isoOpt(r.RevokedAt), "state", state, "session_permanent", r.SessionTTL == SessionPermanent,
		"session_ttl", ttl, "source", r.Source)
}

// Session is MobileSession.
type Session struct {
	TokenHash   string  `json:"token_hash"`
	PairingID   string  `json:"pairing_id"`
	ProjectID   string  `json:"project_id"`
	ProjectName string  `json:"project_name"`
	Permission  string  `json:"permission"`
	CreatedAt   float64 `json:"created_at"`
	ExpiresAt   float64 `json:"expires_at"`
	LastSeenAt  float64 `json:"last_seen_at"`
	UserAgent   string  `json:"user_agent"`
	User        string  `json:"user"`
}

// Manager is PairingManager.
type Manager struct {
	statePath  string
	pairingTTL int64

	mu       sync.Mutex
	pairings map[string]*Pairing
	order    []string // insertion order of pairings (dict order)
	byToken  map[string]string
	sessions map[string]*Session
	sorder   []string
	attempts map[string][]float64
}

// New is PairingManager(state_path=...).
func New(statePath string) *Manager {
	m := &Manager{statePath: statePath, pairingTTL: PairingTTLDefault, pairings: map[string]*Pairing{},
		byToken: map[string]string{}, sessions: map[string]*Session{}, attempts: map[string][]float64{}}
	m.load()
	return m
}

func (m *Manager) addPairing(p *Pairing) {
	if _, ok := m.pairings[p.ID]; !ok {
		m.order = append(m.order, p.ID)
	}
	m.pairings[p.ID] = p
}

func (m *Manager) addSession(s *Session) {
	if _, ok := m.sessions[s.TokenHash]; !ok {
		m.sorder = append(m.sorder, s.TokenHash)
	}
	m.sessions[s.TokenHash] = s
}

func (m *Manager) dropSession(hash string) bool {
	if _, ok := m.sessions[hash]; !ok {
		return false
	}
	delete(m.sessions, hash)
	for i, h := range m.sorder {
		if h == hash {
			m.sorder = append(m.sorder[:i], m.sorder[i+1:]...)
			break
		}
	}
	return true
}

func (m *Manager) dropPairing(id string) {
	delete(m.pairings, id)
	for i, x := range m.order {
		if x == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
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

func optNum(v any) *float64 {
	if f, ok := num(v); ok {
		return &f
	}
	return nil
}

func (m *Manager) load() {
	if m.statePath == "" {
		return
	}
	raw, err := os.ReadFile(m.statePath)
	if err != nil {
		return
	}
	v, err := pyjson.Loads(string(raw))
	data, ok := v.(*pyjson.Object)
	if err != nil || !ok {
		return
	}
	now := Clock()
	str := func(o *pyjson.Object, k string) string { s, _ := o.Value(k).(string); return s }
	if list, ok := data.Value("pairings").([]any); ok {
		for _, item := range list {
			o, ok := item.(*pyjson.Object)
			if !ok {
				m.reset()
				return
			}
			created, _ := num(o.Value("created_at"))
			expires, _ := num(o.Value("expires_at"))
			ttl, _ := num(o.Value("session_ttl"))
			src := str(o, "source")
			if src == "" {
				src = "qr"
			}
			p := &Pairing{ID: str(o, "id"), TokenHash: str(o, "token_hash"), ProjectID: str(o, "project_id"),
				ProjectName: str(o, "project_name"), Permission: str(o, "permission"), CreatedAt: created,
				ExpiresAt: expires, ClaimedAt: optNum(o.Value("claimed_at")), RevokedAt: optNum(o.Value("revoked_at")),
				SessionTTL: int64(ttl), User: str(o, "user"), Source: src}
			m.addPairing(p)
			m.byToken[p.TokenHash] = p.ID
		}
	}
	if list, ok := data.Value("sessions").([]any); ok {
		for _, item := range list {
			o, ok := item.(*pyjson.Object)
			if !ok {
				m.reset()
				return
			}
			f := func(k string) float64 { n, _ := num(o.Value(k)); return n }
			s := &Session{TokenHash: str(o, "token_hash"), PairingID: str(o, "pairing_id"), ProjectID: str(o, "project_id"),
				ProjectName: str(o, "project_name"), Permission: str(o, "permission"), CreatedAt: f("created_at"),
				ExpiresAt: f("expires_at"), LastSeenAt: f("last_seen_at"), UserAgent: str(o, "user_agent"), User: str(o, "user")}
			record := m.pairings[s.PairingID]
			if s.ExpiresAt > now && !(record != nil && record.RevokedAt != nil && *record.RevokedAt != 0) {
				m.addSession(s)
			}
		}
	}
}

func (m *Manager) reset() {
	m.pairings, m.order, m.byToken = map[string]*Pairing{}, nil, map[string]string{}
	m.sessions, m.sorder = map[string]*Session{}, nil
}

func floatOrNil(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func pairingDict(r *Pairing) *pyjson.Object {
	return pyjson.NewObject("id", r.ID, "token_hash", r.TokenHash, "project_id", r.ProjectID, "project_name", r.ProjectName,
		"permission", r.Permission, "created_at", r.CreatedAt, "expires_at", r.ExpiresAt, "claimed_at", floatOrNil(r.ClaimedAt),
		"revoked_at", floatOrNil(r.RevokedAt), "session_ttl", r.SessionTTL, "user", r.User, "source", r.Source)
}

func sessionDict(s *Session) *pyjson.Object {
	return pyjson.NewObject("token_hash", s.TokenHash, "pairing_id", s.PairingID, "project_id", s.ProjectID,
		"project_name", s.ProjectName, "permission", s.Permission, "created_at", s.CreatedAt, "expires_at", s.ExpiresAt,
		"last_seen_at", s.LastSeenAt, "user_agent", s.UserAgent, "user", s.User)
}

// save is PairingManager._save (json.dumps default separators).
func (m *Manager) save() {
	if m.statePath == "" {
		return
	}
	live := map[string]bool{}
	for _, s := range m.sessions {
		live[s.PairingID] = true
	}
	pairings := []any{}
	for _, id := range m.order {
		r := m.pairings[id]
		claimed := r.ClaimedAt != nil && *r.ClaimedAt != 0
		revoked := r.RevokedAt != nil && *r.RevokedAt != 0
		if claimed && (live[r.ID] || revoked) {
			pairings = append(pairings, pairingDict(r))
		}
	}
	sessions := []any{}
	for _, h := range m.sorder {
		sessions = append(sessions, sessionDict(m.sessions[h]))
	}
	data := pyjson.NewObject("format", "ark-mobile-sessions", "version", int64(1), "pairings", pairings, "sessions", sessions)
	os.MkdirAll(filepath.Dir(m.statePath), 0o755) //nolint:errcheck
	tmp := strings.TrimSuffix(m.statePath, filepath.Ext(m.statePath)) + ".tmp"
	if err := os.WriteFile(tmp, pyjson.DumpsASCII(data), 0o600); err != nil {
		return
	}
	os.Rename(tmp, m.statePath) //nolint:errcheck
}

// SessionTTL is PairingManager._session_ttl.
func SessionTTL(value any) (int64, error) {
	if value == nil || value == "" {
		return SessionPermanent, nil
	}
	word := strings.ReplaceAll(pytext.Lower(pytext.Strip(pyjson.Str(value))), "-", "_")
	if permanentWords[word] {
		return SessionPermanent, nil
	}
	n, ok := pyIntPort(value)
	if !ok {
		return 0, merr(400, "session_ttl must be seconds or 'permanent'", "INVALID_SESSION_TTL")
	}
	return max(300, min(SessionTTLMax, n)), nil
}

func (m *Manager) rate(key string, limit int, window float64) error {
	now := Clock()
	bucket := m.attempts[key]
	for len(bucket) > 0 && now-bucket[0] >= window {
		bucket = bucket[1:]
	}
	if len(bucket) >= limit {
		m.attempts[key] = bucket
		return merr(429, "Too many pairing attempts. Wait a minute and try again.", "RATE_LIMITED")
	}
	m.attempts[key] = append(bucket, now)
	return nil
}

func (m *Manager) cleanup() {
	now := Clock()
	for _, id := range append([]string(nil), m.order...) {
		r := m.pairings[id]
		if r.ExpiresAt < now-3600 && r.ClaimedAt == nil {
			m.dropPairing(id)
			delete(m.byToken, r.TokenHash)
		}
	}
	for _, h := range append([]string(nil), m.sorder...) {
		if m.sessions[h].ExpiresAt < now-3600 {
			m.dropSession(h)
		}
	}
}

// Permission is PairingManager._permission.
func Permission(value any) (string, error) {
	text := DefaultPermission
	if pyjson.Truthy(value) {
		text = pyjson.Str(value)
	}
	p := strings.ReplaceAll(pytext.Lower(pytext.Strip(text)), "-", "_")
	if p != "read_only" && p != "full" {
		return "", merr(400, "permission must be read_only or full", "INVALID_PERMISSION")
	}
	return p, nil
}

// Create is PairingManager.create.
func (m *Manager) Create(projectID, projectName string, permission string, ttlSeconds, sessionTTL any, client, user, source string) (*pyjson.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup()
	if client == "" {
		client = "local"
	}
	if err := m.rate("create:"+client, createLimit, createWindow); err != nil {
		return nil, err
	}
	perm, err := Permission(permission)
	if err != nil {
		return nil, err
	}
	sessionSeconds, err := SessionTTL(sessionTTL)
	if err != nil {
		return nil, err
	}
	ttl := m.pairingTTL
	if ttlSeconds != nil {
		n, ok := pyIntPort(ttlSeconds)
		if !ok {
			return nil, merr(400, "expires_in must be a number", "INVALID_TTL")
		}
		ttl = n
	}
	ttl = max(30, min(PairingTTLMax, ttl))
	now := Clock()
	token := "arkp_" + rnd.URLSafe("mobile", 32)
	pairID := "pair_" + rnd.Hex("mobile", 6)
	if projectName == "" {
		projectName = "Project"
	}
	src := "qr"
	if source == "password" || source == SourceDesktop {
		src = source
	}
	r := &Pairing{ID: pairID, TokenHash: sha(token), ProjectID: projectID, ProjectName: projectName, Permission: perm,
		CreatedAt: now, ExpiresAt: now + float64(ttl), User: user, SessionTTL: sessionSeconds, Source: src}
	m.addPairing(r)
	m.byToken[r.TokenHash] = pairID
	var st any = sessionSeconds
	if sessionSeconds == 0 {
		st = nil
	}
	return pyjson.NewObject("id", pairID, "token", token, "permission", perm, "project_id", r.ProjectID,
		"project_name", r.ProjectName, "user", r.User, "created_at", ISO(r.CreatedAt), "expires_at", ISO(r.ExpiresAt),
		"expires_in", ttl, "session_permanent", sessionSeconds == SessionPermanent, "session_ttl", st), nil
}

// Find is PairingManager.find.
func (m *Manager) Find(token string) *Pairing {
	raw := pytext.Strip(token)
	if raw == "" || pytext.Len(raw) > 200 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byToken[sha(raw)]
	if !ok {
		return nil
	}
	r := m.pairings[id]
	if r == nil || !hmac.Equal([]byte(r.TokenHash), []byte(sha(raw))) {
		return nil
	}
	return r
}

// Claim is PairingManager.claim.
func (m *Manager) Claim(token, projectID, client, userAgent string) (*pyjson.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup()
	if client == "" {
		client = "local"
	}
	if err := m.rate("claim:"+client, claimLimit, claimWindow); err != nil {
		return nil, err
	}
	raw := pytext.Strip(token)
	if raw == "" || pytext.Len(raw) > 200 {
		return nil, merr(401, "The pairing code is invalid or has expired.", "PAIRING_INVALID")
	}
	hash := sha(raw)
	id := m.byToken[hash]
	r := m.pairings[id]
	if r == nil || !hmac.Equal([]byte(r.TokenHash), []byte(hash)) {
		return nil, merr(401, "The pairing code is invalid or has expired.", "PAIRING_INVALID")
	}
	now := Clock()
	if r.RevokedAt != nil && *r.RevokedAt != 0 {
		return nil, merr(401, "This pairing session was revoked.", "PAIRING_REVOKED")
	}
	if r.ClaimedAt != nil && *r.ClaimedAt != 0 {
		return nil, merr(401, "This pairing code has already been used.", "PAIRING_USED")
	}
	if r.ExpiresAt <= now {
		return nil, merr(401, "The pairing code has expired.", "PAIRING_EXPIRED")
	}
	if projectID != r.ProjectID {
		return nil, merr(403, "This pairing code belongs to a different project. "+
			"A code is only valid for the project that created it.", "MOBILE_WRONG_PROJECT")
	}
	claimed := now
	r.ClaimedAt = &claimed
	lifetime := float64(r.SessionTTL)
	if r.SessionTTL == SessionPermanent {
		lifetime = PermanentHorizon
	}
	sessionToken := "arkm_" + rnd.URLSafe("mobile", 32)
	s := &Session{TokenHash: sha(sessionToken), PairingID: r.ID, ProjectID: r.ProjectID, ProjectName: r.ProjectName,
		Permission: r.Permission, CreatedAt: now, ExpiresAt: now + lifetime, LastSeenAt: now,
		UserAgent: pytext.Prefix(userAgent, 200), User: r.User}
	m.addSession(s)
	m.save()
	var expiresAt, expiresIn any
	if r.SessionTTL != SessionPermanent {
		expiresAt, expiresIn = ISO(s.ExpiresAt), r.SessionTTL
	}
	return pyjson.NewObject("mobile_token", sessionToken, "token_type", "X-ARK-Mobile-Token", "permission", s.Permission,
		"read_only", s.Permission == "read_only", "project", r.ProjectName, "user", r.User, "expires_at", expiresAt,
		"expires_in", expiresIn, "permanent", r.SessionTTL == SessionPermanent, "pairing_id", r.ID), nil
}

// Authenticate is PairingManager.authenticate.
func (m *Manager) Authenticate(token, projectID, method, path string) (*Session, error) {
	raw := pytext.Strip(token)
	if raw == "" {
		return nil, merr(401, "Pairing is required for this device.", "MOBILE_TOKEN_REQUIRED")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sha(raw)]
	if s == nil {
		return nil, merr(401, "The mobile session is invalid or has expired.", "MOBILE_TOKEN_INVALID")
	}
	now := Clock()
	if s.ExpiresAt <= now {
		m.dropSession(s.TokenHash)
		return nil, merr(401, "The mobile session has expired. Pair again.", "MOBILE_TOKEN_EXPIRED")
	}
	r := m.pairings[s.PairingID]
	if r != nil && r.RevokedAt != nil && *r.RevokedAt != 0 {
		m.dropSession(s.TokenHash)
		return nil, merr(401, "This mobile session was revoked.", "MOBILE_TOKEN_REVOKED")
	}
	if projectID != s.ProjectID {
		return nil, merr(403, "This phone is paired with a different project. "+
			"Switch the desktop back or pair again.", "MOBILE_WRONG_PROJECT")
	}
	verb := strings.ToUpper(method)
	if verb == "" {
		verb = "GET"
	}
	// A desktop link is read-only on its project but may create projects
	// (DesktopLinkPathAllowed; the server checks the account's role).
	desktopWrite := r != nil && r.Source == SourceDesktop && DesktopLinkPathAllowed(path, verb)
	if s.Permission == "read_only" && verb != "GET" && verb != "HEAD" && !switchPath(path) && !desktopWrite {
		return nil, merr(403, "This phone has read-only access. Generate a full-access pairing "+
			"code on the desktop to allow edits.", "MOBILE_READ_ONLY")
	}
	s.LastSeenAt = now
	return s, nil
}

// switchPath: moving the session to another project is not a write, so a
// read-only phone may do it (native switcher or the web app's project list).
func switchPath(path string) bool {
	return path == "/api/mobile/session/project" || path == "/api/projects/open"
}

// SessionForToken is PairingManager.session_for_token.
func (m *Manager) SessionForToken(token string) *Session {
	raw := pytext.Strip(token)
	if raw == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessionForHash(sha(raw))
}

func (m *Manager) sessionForHash(hash string) *Session {
	s := m.sessions[hash]
	if s == nil {
		return nil
	}
	if s.ExpiresAt <= Clock() {
		m.dropSession(s.TokenHash)
		return nil
	}
	if r := m.pairings[s.PairingID]; r != nil && r.RevokedAt != nil && *r.RevokedAt != 0 {
		m.dropSession(s.TokenHash)
		return nil
	}
	return s
}

// PairingFor is PairingManager.pairing_for.
func (m *Manager) PairingFor(s *Session) *Pairing {
	if s == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pairings[s.PairingID]
}

// Rebind moves a phone's session to another project of its account.  A
// pairing logs the phone in as the user, not into one project: the session's
// project is only where it currently is, and the account's role in each
// project decides what it may do there.  The pairing's permission (full or
// read_only) stays a ceiling for every project.  Desktop links stay where
// they are (they only list and create projects).
func (m *Manager) Rebind(token, projectID, projectName string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw := pytext.Strip(token)
	var s *Session
	if raw != "" {
		s = m.sessionForHash(sha(raw))
	}
	if s == nil {
		return nil, merr(401, "The mobile session is invalid or has expired.", "MOBILE_TOKEN_INVALID")
	}
	return s, m.moveLocked(s, projectID, projectName)
}

// Move is Rebind for a session the caller already holds (a project that
// was deleted or is no longer readable for the account).
func (m *Manager) Move(s *Session, projectID, projectName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[s.TokenHash] != s {
		return merr(401, "The mobile session is invalid or has expired.", "MOBILE_TOKEN_INVALID")
	}
	return m.moveLocked(s, projectID, projectName)
}

func (m *Manager) moveLocked(s *Session, projectID, projectName string) error {
	r := m.pairings[s.PairingID]
	if r != nil && r.Source == SourceDesktop {
		return merr(403, "A linked desktop stays on its project.", "MOBILE_PROJECT_PINNED")
	}
	if projectName == "" {
		projectName = "Project"
	}
	if s.ProjectID == projectID && s.ProjectName == projectName {
		return nil
	}
	s.ProjectID, s.ProjectName = projectID, projectName
	if r != nil {
		r.ProjectID, r.ProjectName = projectID, projectName
	}
	m.save()
	return nil
}

// Revoke is PairingManager.revoke.
func (m *Manager) Revoke(pairID, projectID string) (*pyjson.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.pairings[pairID]
	if r == nil || r.ProjectID != projectID {
		return nil, merr(404, "Pairing session not found", "PAIRING_NOT_FOUND")
	}
	if r.RevokedAt == nil || *r.RevokedAt == 0 {
		now := Clock()
		r.RevokedAt = &now
	}
	for _, h := range append([]string(nil), m.sorder...) {
		if m.sessions[h].PairingID == r.ID {
			m.dropSession(h)
		}
	}
	m.save()
	return r.Public(), nil
}

// RevokeSession is PairingManager.revoke_session.
func (m *Manager) RevokeSession(token string) bool {
	raw := pytext.Strip(token)
	if raw == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := m.dropSession(sha(raw))
	if removed {
		m.save()
	}
	return removed
}

// RevokeUser is PairingManager.revoke_user.
func (m *Manager) RevokeUser(username, exceptToken string) int {
	target := pytext.Strip(username)
	if target == "" {
		return 0
	}
	keep := ""
	if t := pytext.Strip(exceptToken); t != "" {
		keep = sha(t)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	for _, h := range append([]string(nil), m.sorder...) {
		if h != keep && pytext.Strip(m.sessions[h].User) == target {
			m.dropSession(h)
			removed++
		}
	}
	if removed > 0 {
		m.save()
	}
	return removed
}

// List is PairingManager.list.
func (m *Manager) List(projectID string) []any {
	m.mu.Lock()
	defer m.mu.Unlock()
	var items []*pyjson.Object
	for _, id := range m.order {
		if r := m.pairings[id]; r.ProjectID == projectID {
			items = append(items, r.Public())
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return pyjson.Str(items[i].Value("created_at")) > pyjson.Str(items[j].Value("created_at"))
	})
	out := []any{}
	for _, i := range items {
		out = append(out, i)
	}
	return out
}

// PendingCount is PairingManager.pending_count.
func (m *Manager) PendingCount(projectID string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := Clock()
	n := int64(0)
	for _, r := range m.pairings {
		if r.ProjectID == projectID && (r.RevokedAt == nil || *r.RevokedAt == 0) && (r.ClaimedAt == nil || *r.ClaimedAt == 0) && r.ExpiresAt > now {
			n++
		}
	}
	return n
}
