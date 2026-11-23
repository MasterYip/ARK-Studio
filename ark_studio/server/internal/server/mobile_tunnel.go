package server

// Phones reach a local ARK server through a Cloudflare Tunnel (Go only).
//
//   GET  /api/mobile/tunnel        state of the tunnel (computer only)
//   POST /api/mobile/tunnel        {enabled, mode, hostname, token, clear_token,
//                                   require_confirm, cloudflared_path, restart}
//   GET  /api/mobile/identity?nonce=…   the server id and public key, and a
//                                   signature over the host and nonce (public)
//   POST /api/mobile/pair/claim/status  {claim_id, claim_secret[, cancel]}: a
//                                   phone waits for the computer to confirm,
//                                   or withdraws its request (public)
//   GET  /api/mobile/pairs/pending      claims waiting for confirmation
//   POST /api/mobile/pairs/pending/{id}/confirm  {approve: true|false}
//
// Settings › Mobile on a local server (loopback bind, no public URL: the
// desktop app or `ark-server serve`) turns the tunnel on. ARK then opens a
// second loopback listener (tunnel_gate.go) and runs cloudflared against it:
// a quick tunnel on *.trycloudflare.com (no account, new address after every
// restart) or the user's named tunnel (token, fixed hostname). The tunnel
// address becomes the mobile public URL, so pairing QR codes use it.
//
// A pairing code claimed through the tunnel waits for the computer: the
// phone shows a 6-digit code, the computer shows the same code next to the
// request, and only a confirmed request gets a device token (on by default;
// a photographed QR alone is not enough). When a quick tunnel's address
// changes, Settings shows a re-link QR (the new address and the server id);
// the phone checks the server's signature with the key it stored at pairing
// and keeps its token.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	crand "crypto/rand"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/mobile"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/platform/cftunnel"
	"arkstudio/go/internal/qr"
	"arkstudio/go/internal/rnd"
)

const (
	mobileTunnelPath      = "/api/mobile/tunnel"
	mobileIdentityPath    = "/api/mobile/identity"
	mobileClaimStatusPath = "/api/mobile/pair/claim/status"
	mobilePendingPath     = "/api/mobile/pairs/pending"
	mobileTunnelOption    = "mobile_tunnel"
	pendingClaimTTL       = 3 * time.Minute
)

var reMobilePendingConfirm = regexp.MustCompile(`^/api/mobile/pairs/pending/([^/]+)/confirm$`)

// mobileTunnelGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var mobileTunnelGoOnly = []PortedRoute{
	{"goonly:exact:" + mobileTunnelPath, "GET,HEAD,POST"},
	{"goonly:exact:" + mobileIdentityPath, "GET,HEAD"},
	{"goonly:exact:" + mobileClaimStatusPath, "POST"},
	{"goonly:exact:" + mobilePendingPath, "GET,HEAD"},
	{`goonly:regex:^/api/mobile/pairs/pending/([^/]+)/confirm$`, "POST"},
}

// tunnelSettings is the stored option (the named-tunnel token is a file).
type tunnelSettings struct {
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode"` // quick or named
	Hostname       string `json:"hostname,omitempty"`
	RequireConfirm *bool  `json:"require_confirm,omitempty"`
	Binary         string `json:"cloudflared_path,omitempty"`
	LocalPort      int    `json:"local_port,omitempty"`
}

func (s tunnelSettings) confirm() bool { return s.RequireConfirm == nil || *s.RequireConfirm }

// mobileTunnel is the App's tunnel state.
type mobileTunnel struct {
	mu        sync.Mutex
	settings  tunnelSettings
	loaded    bool
	gen       int // bumped by every start/stop; stale starters give up
	phase     string
	phaseErr  string
	dlDone    int64 // cloudflared download progress (bytes)
	dlTotal   int64
	binary    cftunnel.Binary
	tunnel    *cftunnel.Tunnel
	listener  net.Listener
	srv       *http.Server
	port      int
	url, host string
	prevURL   string
	changedAt time.Time

	identityOnce sync.Once
	identity     *mobile.Identity
	identityErr  error

	limits  tunnelLimiter
	pending pendingClaims
}

// --------------------------------------------------------------- settings

func (a *App) tunnelSettingsLocked(ctx context.Context) tunnelSettings {
	t := &a.cfTunnel
	if !t.loaded {
		t.loaded = true
		if raw, ok, err := a.Org.Option(ctx, mobileTunnelOption); err == nil && ok {
			json.Unmarshal([]byte(raw), &t.settings) //nolint:errcheck
		}
		if t.settings.Mode != "named" {
			t.settings.Mode = "quick"
		}
	}
	return t.settings
}

func (a *App) tunnelRequireConfirm() bool {
	a.cfTunnel.mu.Lock()
	defer a.cfTunnel.mu.Unlock()
	return a.tunnelSettingsLocked(context.Background()).confirm()
}

// tunnelPrivateDir holds the named-tunnel token: ARK_PRIVATE_DIR (desktop),
// else next to the organization database.
func (a *App) tunnelPrivateDir() string {
	if d := pytext.Strip(os.Getenv("ARK_PRIVATE_DIR")); d != "" {
		return d
	}
	return filepath.Dir(org.DefaultPath(a.Root))
}

func (a *App) tunnelTokenPath() string { return filepath.Join(a.tunnelPrivateDir(), "cf-tunnel-token") }

// tunnelToolsDir holds a downloaded cloudflared: ARK_TOOLS_DIR, else
// tools/ next to the organization database (the desktop data folder).
func (a *App) tunnelToolsDir() string {
	if d := pytext.Strip(os.Getenv("ARK_TOOLS_DIR")); d != "" {
		return d
	}
	return filepath.Join(filepath.Dir(org.DefaultPath(a.Root)), "tools")
}

func (a *App) readTunnelToken() string {
	raw, err := os.ReadFile(a.tunnelTokenPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// tunnelAvailable: only a local server (loopback bind, no public URL) runs
// a tunnel; a server with its own address does not need one.
func (a *App) tunnelAvailable() (bool, string) {
	if !IsLoopbackHost(a.BindHost) {
		return false, "not_loopback"
	}
	if a.publicURL != "" {
		return false, "public_url"
	}
	return true, ""
}

// ------------------------------------------------------------- addresses

// tunnelHost is the hostname the tunnel listener accepts ("" when off).
func (a *App) tunnelHost() string {
	a.cfTunnel.mu.Lock()
	defer a.cfTunnel.mu.Unlock()
	if a.cfTunnel.listener == nil {
		return ""
	}
	return a.cfTunnel.host
}

// isTunnelHostname: quick-tunnel names and the configured named host never
// become allowed Hosts of the app port.
func (a *App) isTunnelHostname(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if strings.HasSuffix(host, ".trycloudflare.com") {
		return true
	}
	a.cfTunnel.mu.Lock()
	defer a.cfTunnel.mu.Unlock()
	named := strings.ToLower(a.cfTunnel.settings.Hostname)
	return named != "" && host == named || host == a.cfTunnel.host && host != ""
}

// mobilePublicURL is the address phones use: the configured public URL, or
// the tunnel's while it has one.
func (a *App) mobilePublicURL() string {
	if a.publicURL != "" {
		return a.publicURL
	}
	a.cfTunnel.mu.Lock()
	defer a.cfTunnel.mu.Unlock()
	if a.cfTunnel.listener == nil {
		return ""
	}
	return a.cfTunnel.url
}

func (a *App) mobilePublicSource() string {
	if a.publicURL == "" && a.mobilePublicURL() != "" {
		return "cf_tunnel"
	}
	return "public_url"
}

// ----------------------------------------------------------------- start

// StartMobileTunnel starts the tunnel when Settings turned it on (serve()
// calls it after Bind).
func (a *App) StartMobileTunnel() {
	a.cfTunnel.mu.Lock()
	s := a.tunnelSettingsLocked(context.Background())
	a.cfTunnel.mu.Unlock()
	if ok, _ := a.tunnelAvailable(); ok && s.Enabled {
		a.restartTunnel()
	}
}

// restartTunnel stops any running tunnel and starts one in the background.
func (a *App) restartTunnel() {
	a.stopTunnel()
	t := &a.cfTunnel
	t.mu.Lock()
	t.gen++
	gen := t.gen
	s := t.settings
	t.phase, t.phaseErr = "starting", ""
	t.mu.Unlock()
	go a.runTunnel(gen, s)
}

func (a *App) tunnelFail(gen int, err error) {
	t := &a.cfTunnel
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.gen == gen {
		t.phase, t.phaseErr = "error", err.Error()
	}
}

func (a *App) runTunnel(gen int, s tunnelSettings) {
	t := &a.cfTunnel
	bin, ok := cftunnel.Find(s.Binary, a.tunnelToolsDir())
	if !ok {
		if s.Binary != "" {
			a.tunnelFail(gen, fmt.Errorf("cloudflared was not found at %s", s.Binary))
			return
		}
		t.mu.Lock()
		if t.gen != gen {
			t.mu.Unlock()
			return
		}
		t.phase = "downloading"
		t.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		var err error
		bin, err = cftunnel.Download(ctx, a.tunnelToolsDir(), func(done, total int64) {
			t.mu.Lock()
			t.dlDone, t.dlTotal = done, total
			t.mu.Unlock()
		})
		cancel()
		if err != nil {
			a.tunnelFail(gen, err)
			return
		}
	}
	token := ""
	if s.Mode == "named" {
		if token = a.readTunnelToken(); token == "" || s.Hostname == "" {
			a.tunnelFail(gen, errors.New("a named tunnel needs its token and public hostname"))
			return
		}
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(s.LocalPort)))
	if err != nil && s.LocalPort != 0 {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		a.tunnelFail(gen, err)
		return
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: a.TunnelHandler(), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second,
		MaxHeaderBytes: 64 << 10}
	t.mu.Lock()
	if t.gen != gen {
		t.mu.Unlock()
		ln.Close()
		return
	}
	t.binary, t.listener, t.srv, t.port = bin, ln, srv, port
	t.phase = ""
	if s.Mode == "named" {
		t.host = strings.ToLower(s.Hostname)
		a.setTunnelURLLocked("https://" + t.host + "/")
		if t.settings.LocalPort != port { // keep the port the named tunnel points at
			t.settings.LocalPort = port
			a.saveTunnelSettingsLocked(context.Background(), "")
		}
	}
	t.mu.Unlock()
	go srv.Serve(ln) //nolint:errcheck
	tun := cftunnel.Start(cftunnel.Config{Binary: bin.Path, Origin: "http://127.0.0.1:" + strconv.Itoa(port),
		Mode: s.Mode, Token: token, Hostname: s.Hostname}, func(u string) {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.gen != gen {
			return
		}
		if parsed := urlHostname(strings.TrimPrefix(u, "https://")); parsed != "" {
			t.host = parsed
		}
		a.setTunnelURLLocked(u)
	})
	t.mu.Lock()
	if t.gen != gen {
		t.mu.Unlock()
		tun.Stop()
		return
	}
	t.tunnel = tun
	t.mu.Unlock()
}

// setTunnelURLLocked records a new tunnel address (and the one before it,
// for the re-link notice).
func (a *App) setTunnelURLLocked(u string) {
	t := &a.cfTunnel
	if u == t.url {
		return
	}
	if t.url != "" {
		t.prevURL = t.url
	} else if last := a.lastTunnelURL(); last != "" && last != u {
		t.prevURL = last
	}
	t.url, t.changedAt = u, time.Now().UTC()
	a.rememberTunnelURL(u)
	fmt.Fprintf(os.Stderr, "mobile tunnel: %s\n", u)
}

// The last tunnel address survives restarts so Settings can say it changed.
func (a *App) lastTunnelPath() string { return filepath.Join(a.Root, ".mobile-tunnel-last") }

func (a *App) lastTunnelURL() string {
	raw, _ := os.ReadFile(a.lastTunnelPath())
	return strings.TrimSpace(string(raw))
}

func (a *App) rememberTunnelURL(u string) { os.WriteFile(a.lastTunnelPath(), []byte(u+"\n"), 0o600) } //nolint:errcheck

// stopTunnel ends cloudflared and the tunnel listener.
func (a *App) stopTunnel() {
	t := &a.cfTunnel
	t.mu.Lock()
	t.gen++
	tun, srv := t.tunnel, t.srv
	t.tunnel, t.srv, t.listener, t.port = nil, nil, nil, 0
	t.url, t.host, t.phase, t.phaseErr = "", "", "", ""
	t.mu.Unlock()
	if tun != nil {
		tun.Stop()
	}
	if srv != nil {
		srv.Close() //nolint:errcheck
	}
}

func (a *App) saveTunnelSettingsLocked(ctx context.Context, actor string) error {
	raw, err := json.Marshal(a.cfTunnel.settings)
	if err != nil {
		return err
	}
	action := ""
	if actor != "" {
		action = "org.mobile_tunnel"
	}
	return a.Org.SetOption(ctx, actor, mobileTunnelOption, string(raw), action,
		pyjson.NewObject("enabled", a.cfTunnel.settings.Enabled, "mode", a.cfTunnel.settings.Mode))
}

// ----------------------------------------------------------------- identity

func (a *App) serverIdentity() (*mobile.Identity, error) {
	t := &a.cfTunnel
	t.identityOnce.Do(func() {
		t.identity, t.identityErr = mobile.LoadIdentity(filepath.Join(a.Root, ".ark-server-identity.json"))
	})
	return t.identity, t.identityErr
}

// mobileIdentity answers GET /api/mobile/identity (before the guards: a
// phone checks a new tunnel address before it sends its token there).
func (c *reqCtx) mobileIdentity() error {
	id, err := c.app.serverIdentity()
	if err != nil {
		return err
	}
	nonce := c.query.First("nonce", "")
	host := urlHostname(strings.ToLower(c.r.Host))
	out := pyjson.NewObject("server_id", id.ID, "public_key", id.PublicKey, "algorithm", "ECDSA-P256-SHA256",
		"context", mobile.IdentityContext, "host", host, "tunnel", viaTunnel(c.r))
	if nonce != "" {
		if !mobile.ValidNonce(nonce) {
			return apiErr(400, "nonce must be 16-128 URL-safe characters", "INVALID_NONCE")
		}
		sig, err := id.Sign(host, nonce)
		if err != nil {
			return err
		}
		out.Set("nonce", nonce).Set("signature", sig)
	}
	c.sendJSON(200, pyjson.NewObject("ok", true).Update(out), headerList{{"Cache-Control", "no-store"}})
	return nil
}

// ------------------------------------------------------------- tunnel API

// requireTunnelOwner: only the computer running ARK (not a phone, not the
// tunnel) reads or changes the tunnel.
func (c *reqCtx) requireTunnelOwner() error {
	if viaTunnel(c.r) || c.mobileSession != nil || c.mobileToken() != "" || !c.trustedLocal() {
		return apiErr(403, "Only the computer running ARK can manage the Cloudflare Tunnel.", "TUNNEL_LOCAL_ONLY")
	}
	return nil
}

func (c *reqCtx) mobileTunnelGet() error {
	if !viaTunnel(c.r) && c.mobileSession == nil && c.mobileToken() == "" && !c.trustedLocal() {
		// Settings on a remote server: nothing to show, nothing revealed.
		c.ok(pyjson.NewObject("available", false, "reason", "not_local"))
		return nil
	}
	if err := c.requireTunnelOwner(); err != nil {
		return err
	}
	c.ok(c.app.tunnelStatus())
	return nil
}

func (a *App) tunnelStatus() *pyjson.Object {
	available, reason := a.tunnelAvailable()
	t := &a.cfTunnel
	t.mu.Lock()
	s := a.tunnelSettingsLocked(context.Background())
	var st cftunnel.Status
	if t.tunnel != nil {
		st = t.tunnel.Status()
	}
	state := "off"
	switch {
	case t.phase == "error":
		state = "error"
	case t.phase != "":
		state = t.phase
	case t.tunnel != nil && st.State == "running" && t.url != "":
		state = "running"
	case t.tunnel != nil:
		state = st.State
	}
	if s.Enabled && state == "off" && available {
		state = "starting"
	}
	errText := t.phaseErr
	if errText == "" && state != "running" {
		errText = st.Error
	}
	url, host, prev, changed, port := t.url, t.host, t.prevURL, t.changedAt, t.port
	dlDone, dlTotal := t.dlDone, t.dlTotal
	bin := t.binary
	t.mu.Unlock()
	token := a.readTunnelToken() != ""
	log := st.Log
	if len(log) > 8 {
		log = log[len(log)-8:]
	}
	logAny := make([]any, len(log))
	for i, l := range log {
		logAny[i] = l
	}
	var changedAt any
	if !changed.IsZero() {
		changedAt = changed.Format("2006-01-02T15:04:05") + "+00:00"
	}
	out := pyjson.NewObject("available", available, "reason", reason, "enabled", s.Enabled, "mode", s.Mode,
		"state", state, "url", url, "host", host, "error", errText, "log", logAny,
		"previous_url", prev, "url_changed_at", changedAt, "restarts", int64(st.Restarts),
		"require_confirm", s.confirm(), "hostname", s.Hostname, "token_set", token, "local_port", int64(port),
		"cloudflared", pyjson.NewObject("path", bin.Path, "source", bin.Source, "custom", s.Binary,
			"version", cftunnel.Version, "download_dir", a.tunnelToolsDir()),
		"pending", int64(a.cfTunnel.pending.count()),
		"download", pyjson.NewObject("done", dlDone, "total", dlTotal))
	if id, err := a.serverIdentity(); err == nil {
		out.Set("server_id", id.ID)
		relinkCode := ""
		if strings.HasSuffix(host, ".trycloudflare.com") {
			relinkCode = strings.TrimSuffix(host, ".trycloudflare.com")
		}
		out.Set("relink_code", relinkCode)
		if url != "" {
			relink := url + "#relink=" + id.ID
			svg, _ := qr.SVG(relink)
			out.Set("relink_url", relink).Set("relink_qr_svg", svg)
		}
	}
	return out
}

func (c *reqCtx) mobileTunnelPost(body *pyjson.Object) error {
	if err := c.requireTunnelOwner(); err != nil {
		return err
	}
	a := c.app
	if ok, reason := a.tunnelAvailable(); !ok && pyjson.Truthy(body.Value("enabled")) {
		msg := "This ARK server already has a public address; it does not need a tunnel."
		if reason == "not_loopback" {
			msg = "The Cloudflare Tunnel is for a local ARK server (bound to 127.0.0.1)."
		}
		return apiErr(409, msg, "TUNNEL_UNAVAILABLE")
	}
	t := &a.cfTunnel
	t.mu.Lock()
	s := a.tunnelSettingsLocked(c.ctx)
	before := s
	if v, ok := body.Get("enabled"); ok {
		s.Enabled = pyjson.Truthy(v)
	}
	if v, ok := body.Get("mode"); ok {
		switch m := pytext.Strip(pyjson.Str(v)); m {
		case "quick", "named":
			s.Mode = m
		default:
			t.mu.Unlock()
			return apiErr(400, "mode must be quick or named", "INVALID_TUNNEL_MODE")
		}
	}
	if v, ok := body.Get("hostname"); ok {
		h := strings.ToLower(pytext.Strip(strOrEmptyAny(v)))
		h = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://"), "/")
		if h != "" {
			if _, err := mobile.ValidateTunnelHost(h); err != nil || strings.ContainsAny(h, ":/") || !strings.Contains(h, ".") {
				t.mu.Unlock()
				return apiErr(400, "Enter the tunnel's public hostname, such as ark.example.com.", "INVALID_TUNNEL_HOST")
			}
		}
		s.Hostname = h
	}
	if v, ok := body.Get("require_confirm"); ok {
		b := pyjson.Truthy(v)
		s.RequireConfirm = &b
	}
	if v, ok := body.Get("cloudflared_path"); ok {
		s.Binary = pytext.Strip(strOrEmptyAny(v))
	}
	t.settings = s
	err := a.saveTunnelSettingsLocked(c.ctx, c.actor())
	t.mu.Unlock()
	if err != nil {
		return err
	}
	if token := pytext.Strip(strOrEmptyAny(body.Value("token"))); token != "" {
		if strings.ContainsAny(token, " \r\n\t") || len(token) > 4096 {
			return apiErr(400, "The tunnel token looks malformed.", "INVALID_TUNNEL_TOKEN")
		}
		if err := os.MkdirAll(a.tunnelPrivateDir(), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(a.tunnelTokenPath(), []byte(token+"\n"), 0o600); err != nil {
			return err
		}
	} else if pyjson.Truthy(body.Value("clear_token")) {
		os.Remove(a.tunnelTokenPath()) //nolint:errcheck
	}
	restart := pyjson.Truthy(body.Value("restart")) || s.Mode != before.Mode || s.Hostname != before.Hostname ||
		s.Binary != before.Binary || pytext.Strip(strOrEmptyAny(body.Value("token"))) != ""
	switch {
	case !s.Enabled && before.Enabled:
		a.stopTunnel()
	case !s.Enabled:
	case !before.Enabled || restart:
		a.restartTunnel()
	}
	c.ok(a.tunnelStatus())
	return nil
}

// ------------------------------------------------------- confirmed claims

type pendingClaim struct {
	ID, secretHash, codeHash, pairID, confirm string
	client, userAgent, device, project, user  string
	created, expires                          time.Time
	state                                     string // pending, approved, rejected
}

type pendingClaims struct {
	mu    sync.Mutex
	items map[string]*pendingClaim
}

func (p *pendingClaims) prune() {
	now := time.Now()
	for id, c := range p.items {
		if now.After(c.expires) {
			delete(p.items, id)
		}
	}
}

func (p *pendingClaims) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune()
	n := 0
	for _, c := range p.items {
		if c.state == "pending" {
			n++
		}
	}
	return n
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sixDigits() string {
	n, err := crand.Int(crand.Reader, big.NewInt(1000000))
	if err != nil {
		return "000000"
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// tunnelClaim takes POST /api/mobile/pair/claim through the tunnel while
// confirmation is on: a valid code starts a pending claim instead of
// issuing a token. Everything else (invalid, used, expired codes) goes to
// the usual claim, which answers and rate-limits as before.
func (c *reqCtx) tunnelClaim(path string) (bool, error) {
	if path != "/api/mobile/pair/claim" || !viaTunnel(c.r) || !c.app.tunnelRequireConfirm() {
		return false, nil
	}
	code := strOrEmptyAny(c.body.Value("token"))
	if code == "" {
		code = strOrEmptyAny(c.body.Value("code"))
	}
	record := c.app.Mobile.Find(code)
	now := mobile.Clock()
	if record == nil || (record.RevokedAt != nil && *record.RevokedAt != 0) ||
		(record.ClaimedAt != nil && *record.ClaimedAt != 0) || record.ExpiresAt <= now {
		return c.mobilePost(path, c.body)
	}
	p := &c.app.cfTunnel.pending
	p.mu.Lock()
	p.prune()
	if p.items == nil {
		p.items = map[string]*pendingClaim{}
	}
	same := 0
	for _, x := range p.items {
		if x.pairID == record.ID && x.state == "pending" {
			same++
		}
	}
	if same >= 3 || len(p.items) >= 32 {
		p.mu.Unlock()
		return true, apiErr(429, "Too many pairing attempts. Wait a minute and try again.", "RATE_LIMITED")
	}
	secret := rnd.URLSafe("mobile", 32)
	pc := &pendingClaim{ID: "claim_" + rnd.Hex("mobile", 8), secretHash: hashHex(secret), codeHash: hashHex(pytext.Strip(code)),
		pairID: record.ID, confirm: sixDigits(), client: c.clientIP(), userAgent: pytext.Prefix(c.header("User-Agent"), 200),
		device: pytext.Prefix(pytext.Strip(strOrEmptyAny(c.body.Value("device"))), 80), project: record.ProjectName,
		user: record.User, created: time.Now(), expires: time.Now().Add(pendingClaimTTL), state: "pending"}
	p.items[pc.ID] = pc
	p.mu.Unlock()
	c.sendJSON(202, pyjson.NewObject("ok", true, "pending", true, "claim_id", pc.ID, "claim_secret", secret,
		"confirm_code", pc.confirm, "expires_in", int64(pendingClaimTTL/time.Second), "poll_after", int64(2),
		"status_path", strings.TrimPrefix(mobileClaimStatusPath, "/")), headerList{{"Cache-Control", "no-store"}})
	return true, nil
}

// mobileClaimStatus answers POST /api/mobile/pair/claim/status (before the
// guards; the 256-bit claim secret is the credential). A confirmed claim is
// completed here with the usual claim, so the phone gets its token.
func (c *reqCtx) mobileClaimStatus() error {
	body, err := c.readJSONBody()
	if err != nil {
		return err
	}
	id := strOrEmptyAny(body.Value("claim_id"))
	secret := strOrEmptyAny(body.Value("claim_secret"))
	p := &c.app.cfTunnel.pending
	p.mu.Lock()
	p.prune()
	pc := p.items[id]
	if pc == nil || secret == "" || !hmac.Equal([]byte(pc.secretHash), []byte(hashHex(secret))) {
		p.mu.Unlock()
		return apiErr(401, "This pairing request has expired. Scan the QR code again.", "PAIRING_CONFIRM_EXPIRED")
	}
	if pyjson.Truthy(body.Value("cancel")) { // the phone gave up: drop it from the computer's list
		delete(p.items, id)
		p.mu.Unlock()
		c.ok(pyjson.NewObject("cancelled", true))
		return nil
	}
	state := pc.state
	if state != "pending" {
		delete(p.items, id)
	}
	confirm, codeHash, left := pc.confirm, pc.codeHash, time.Until(pc.expires)
	p.mu.Unlock()
	switch state {
	case "rejected":
		return apiErr(403, "The computer declined this pairing request.", "PAIRING_REJECTED")
	case "approved":
		code := strOrEmptyAny(body.Value("code"))
		if code == "" {
			code = strOrEmptyAny(body.Value("token"))
		}
		if hashHex(pytext.Strip(code)) != codeHash {
			return apiErr(401, "This pairing request has expired. Scan the QR code again.", "PAIRING_CONFIRM_EXPIRED")
		}
		c.body = pyjson.NewObject("code", code)
		_, err := c.mobilePost("/api/mobile/pair/claim", c.body)
		return err
	}
	c.sendJSON(202, pyjson.NewObject("ok", true, "pending", true, "claim_id", id, "confirm_code", confirm,
		"expires_in", int64(left/time.Second), "poll_after", int64(2)), headerList{{"Cache-Control", "no-store"}})
	return nil
}

// readJSONBody reads the JSON body of a route answered before the router.
func (c *reqCtx) readJSONBody() (*pyjson.Object, error) {
	if !c.sameOrigin() {
		return nil, apiErr(403, "Cross-origin writes are rejected", "")
	}
	if n, _ := contentLength(c.header("Content-Length")); n > 8<<10 {
		return nil, apiErr(413, "Request body is too large", "")
	}
	return c.readJSON()
}

// claimVisible: a person confirms claims of their own pairing codes (every
// claim on a server without accounts).
func (c *reqCtx) claimVisible(pc *pendingClaim) bool {
	return c.app.Auth == nil || strings.EqualFold(pc.user, c.actor())
}

func (c *reqCtx) mobilePendingGet() error {
	if c.mobileSession != nil {
		return apiErr(403, "A paired phone cannot manage pairing codes.", "MOBILE_SCOPE")
	}
	p := &c.app.cfTunnel.pending
	p.mu.Lock()
	p.prune()
	out := []any{}
	for _, pc := range p.items {
		if pc.state != "pending" || !c.claimVisible(pc) {
			continue
		}
		out = append(out, pyjson.NewObject("id", pc.ID, "pair_id", pc.pairID, "confirm_code", pc.confirm,
			"client_ip", pc.client, "user_agent", pc.userAgent, "device", pc.device, "project", pc.project,
			"created_at", pc.created.UTC().Format("2006-01-02T15:04:05")+"+00:00",
			"expires_at", pc.expires.UTC().Format("2006-01-02T15:04:05")+"+00:00"))
	}
	p.mu.Unlock()
	c.ok(pyjson.NewObject("pending", out))
	return nil
}

func (c *reqCtx) mobilePendingConfirm(id string, body *pyjson.Object) error {
	if c.mobileSession != nil || viaTunnel(c.r) && c.session == nil {
		return apiErr(403, "A paired phone cannot manage pairing codes.", "MOBILE_SCOPE")
	}
	approve := pyjson.Truthy(body.Value("approve"))
	p := &c.app.cfTunnel.pending
	p.mu.Lock()
	p.prune()
	pc := p.items[id]
	if pc == nil || pc.state != "pending" || !c.claimVisible(pc) {
		p.mu.Unlock()
		return apiErr(404, "This pairing request has expired.", "PAIRING_CONFIRM_EXPIRED")
	}
	if approve {
		pc.state = "approved"
		for _, other := range p.items { // one code, one phone: decline the rest
			if other != pc && other.pairID == pc.pairID && other.state == "pending" {
				other.state = "rejected"
			}
		}
	} else {
		pc.state = "rejected"
	}
	pairID := pc.pairID
	p.mu.Unlock()
	action := "mobile.pair.reject"
	if approve {
		action = "mobile.pair.confirm"
	}
	actorID, err := c.app.Org.UserIDFor(c.ctx, c.actor())
	if err != nil {
		return err
	}
	if err := c.app.Org.Log(c.ctx, actorID, action, "", pyjson.NewObject("pair", pairID, "claim", id)); err != nil {
		return err
	}
	c.ok(pyjson.NewObject("id", id, "approved", approve))
	return nil
}

// closeTunnel is part of App.Close.
func (a *App) closeTunnel() { a.stopTunnel() }
