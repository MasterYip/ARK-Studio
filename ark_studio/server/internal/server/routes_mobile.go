package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/mobile"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/qr"
)

// mobilePorted are the contract routes this file answers (router.go registers them).
var mobilePorted = []PortedRoute{
	{"exact:/api/mobile/login", "POST"},
	{"exact:/api/mobile/pair", "POST"},
	{"exact:/api/mobile/pair/claim", "POST"},
	{"exact:/api/mobile/session/project", "POST"},
	{"regex:^/api/mobile/pairs/([^/]+)/revoke$", "POST"},
}

// Paired phones: mobile.connection_info, the /api/mobile/* routes and the
// device-session parts of _guard_public_auth / _guard_mobile.

var reMobileRevoke = pyRe(`^/api/mobile/pairs/([^/]+)/revoke$`, false)

// reMobilePairPermission is Go only: Settings → Mobile sets a device to
// full access as its user or read-only.
var reMobilePairPermission = pyRe(`^/api/mobile/pairs/([^/]+)/permission$`, false)

var mobileGoOnly = []PortedRoute{{`goonly:regex:^/api/mobile/pairs/([^/]+)/permission$`, "POST"},
	{"goonly:exact:" + mobileMergePath, "POST"}, {"goonly:exact:" + mobileStalePath, "POST"}}

// Go only (mobile/deviceid.go): merge one person's devices into one, and
// list or revoke devices unseen for 30 days.
const (
	mobileMergePath = "/api/mobile/pairs/merge"
	mobileStalePath = "/api/mobile/pairs/stale"
)

// clientDeviceID is the phone's device id (Go only, mobile/deviceid.go):
// the sign-in body's device_id, the X-ARK-Device header or the ark_device
// cookie (the web app and the Android WebView).
func (c *reqCtx) clientDeviceID(body *pyjson.Object) string {
	if body != nil {
		if id := pytext.Strip(strOrEmptyAny(body.Value("device_id"))); id != "" {
			return id
		}
	}
	if id := pytext.Strip(c.header("X-ARK-Device")); id != "" {
		return id
	}
	return auth.ParseCookie(c.header("Cookie"), "ark_device")
}

// ----------------------------------------------------------- addresses

type parsedV4 struct{ a, b, c, d byte }

func v4(s string) (parsedV4, bool) {
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil || strings.Contains(s, ":") {
		return parsedV4{}, false
	}
	x := ip.To4()
	return parsedV4{x[0], x[1], x[2], x[3]}, true
}

func (p parsedV4) in(cidr string) bool {
	_, n, _ := net.ParseCIDR(cidr)
	return n.Contains(net.IPv4(p.a, p.b, p.c, p.d))
}

// ipv4Private is ipaddress.IPv4Address.is_private (CPython 3.10 table).
func (p parsedV4) private() bool {
	for _, c := range []string{"0.0.0.0/8", "10.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/29", "192.0.0.170/31", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32"} {
		if p.in(c) {
			return true
		}
	}
	return false
}

func (p parsedV4) loopback() bool    { return p.a == 127 }
func (p parsedV4) linkLocal() bool   { return p.in("169.254.0.0/16") }
func (p parsedV4) unspecified() bool { return p == parsedV4{} }

func outboundIPv4() string {
	conn, err := net.DialTimeout("udp4", "192.0.2.1:9", 200*time.Millisecond)
	if err != nil {
		return ""
	}
	defer conn.Close()
	host, _, _ := net.SplitHostPort(conn.LocalAddr().String())
	p, ok := v4(host)
	if !ok || p.loopback() || p.linkLocal() || p.unspecified() {
		return ""
	}
	return host
}

var ipCache struct {
	sync.Mutex
	at    time.Time
	addrs []string
}

// privateIPv4 is mobile._private_ipv4 (cached for 60 s).
func privateIPv4() []string {
	ipCache.Lock()
	defer ipCache.Unlock()
	if len(ipCache.addrs) > 0 && time.Since(ipCache.at) < 60*time.Second {
		return append([]string(nil), ipCache.addrs...)
	}
	var addrs []string
	if out := outboundIPv4(); out != "" {
		addrs = append(addrs, out)
	}
	if host, err := os.Hostname(); err == nil {
		if ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip4", host); err == nil {
			for _, ip := range ips {
				s := ip.String()
				if s == "" || contains(addrs, s) {
					continue
				}
				p, ok := v4(s)
				if !ok || p.loopback() || p.linkLocal() || p.unspecified() {
					continue
				}
				if p.private() {
					addrs = append(addrs, s)
				}
			}
		}
	}
	if len(addrs) == 0 && goos == "windows" {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		out, _ := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command",
			"(Get-NetIPAddress -AddressFamily IPv4 | Where-Object "+
				"{ $_.IPAddress -notlike '127.*' -and $_.IPAddress -notlike '169.254.*' } "+
				"| Select-Object -ExpandProperty IPAddress) -join ','").Output()
		cancel()
		for _, ip := range strings.Split(pytext.Strip(string(out)), ",") {
			if ip = pytext.Strip(ip); ip != "" && !contains(addrs, ip) {
				addrs = append(addrs, ip)
			}
		}
	}
	ipCache.at, ipCache.addrs = time.Now(), append([]string(nil), addrs...)
	return addrs
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var publicIP struct {
	sync.Mutex
	at                time.Time
	ip, source, error string
}

// detectPublicIP is mobile.detect_public_ip (opt-in).
func detectPublicIP(enabled bool) *pyjson.Object {
	if !enabled {
		return pyjson.NewObject("ip", "", "source", "", "error", "", "checked", false, "enabled", false)
	}
	publicIP.Lock()
	defer publicIP.Unlock()
	ttl := 60 * time.Second
	if publicIP.ip != "" {
		ttl = 600 * time.Second
	}
	if !publicIP.at.IsZero() && time.Since(publicIP.at) < ttl {
		return pyjson.NewObject("ip", publicIP.ip, "source", publicIP.source, "error", publicIP.error, "checked", true, "enabled", true)
	}
	ip, source, lastErr := "", "", ""
	client := &http.Client{Timeout: 1500 * time.Millisecond, Transport: &http.Transport{Proxy: nil}}
	for _, u := range []string{"https://api.ipify.org", "https://ifconfig.me/ip"} {
		req, _ := http.NewRequest("GET", u, nil)
		req.Header.Set("User-Agent", "ARK-Studio/2.0 public-ip opt-in")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = pytext.Prefix(err.Error(), 200)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
		resp.Body.Close()
		text := pytext.Strip(string(body))
		parsed := net.ParseIP(text)
		if parsed == nil {
			lastErr = pytext.Prefix("'"+text+"' does not appear to be an IPv4 or IPv6 address", 200)
			continue
		}
		if parsed.IsGlobalUnicast() && !parsed.IsPrivate() {
			ip, source, lastErr = parsed.String(), u, ""
			break
		}
		lastErr = "provider returned a non-public address"
	}
	if ip == "" && lastErr == "" {
		lastErr = "public IP lookup failed"
	}
	publicIP.at, publicIP.ip, publicIP.source, publicIP.error = time.Now(), ip, source, lastErr
	return pyjson.NewObject("ip", ip, "source", source, "error", lastErr, "checked", true, "enabled", true)
}

func isUnspecified(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsUnspecified()
}

// baseURL is mobile._base_url.
func baseURL(host, scheme string, port any) (string, error) {
	clean, err := mobile.ValidateTunnelHost(host)
	if err != nil {
		return "", err
	}
	s := "http"
	if strings.ToLower(scheme) == "https" {
		s = "https"
	}
	def := 80
	if s == "https" {
		def = 443
	}
	p := def
	switch x := port.(type) {
	case int:
		p = x
	case nil:
	}
	if p == def {
		return s + "://" + clean + "/", nil
	}
	return s + "://" + clean + ":" + strconv.Itoa(p) + "/", nil
}

type candidate struct{ url, source string }

func (a *App) explicitPublicIP() string {
	text := strings.Trim(pytext.Strip(a.Cfg.PublicIP), "[]")
	if net.ParseIP(text) == nil {
		return ""
	}
	return text
}

func (a *App) publicIPURL(ip string) string {
	port := a.Port
	if port == 0 {
		port = 80
	}
	u, err := baseURL(ip, "http", port)
	if err != nil {
		return ""
	}
	return u
}

func portOrNil(port int) any {
	if port == 0 {
		return nil
	}
	return port
}

// pairingCandidates is mobile._pairing_candidates.
func (a *App) pairingCandidates(requestHost, requestScheme string, detection *pyjson.Object) []candidate {
	if public := pytext.Strip(a.mobilePublicURL()); public != "" {
		if u, err := mobile.NormalizePublicURL(public); err == nil {
			return []candidate{{u, a.mobilePublicSource()}}
		}
	}
	var out []candidate
	explicit := strings.Trim(pytext.Strip(a.Cfg.PublicIP), "[]")
	if explicit != "" && net.ParseIP(explicit) != nil {
		if u := a.publicIPURL(explicit); u != "" {
			out = append(out, candidate{u, "public_ip"})
		}
	}
	host := pytext.Strip(requestHost)
	if host != "" && !mobile.IsLoopbackHost(host) && !isUnspecified(host) {
		scheme := requestScheme
		if scheme == "" {
			scheme = "http"
		}
		if u, err := baseURL(host, scheme, portOrNil(a.Port)); err == nil {
			out = append(out, candidate{u, "request_host"})
		}
	}
	for _, ip := range privateIPv4() {
		if u, err := baseURL(ip, "http", portOrNil(a.Port)); err == nil {
			out = append(out, candidate{u, "lan"})
		}
	}
	if detection == nil && explicit == "" {
		detection = detectPublicIP(a.Cfg.DetectPublicIP)
	}
	if detection != nil && pyjson.Truthy(detection.Value("ip")) && explicit == "" {
		if u := a.publicIPURL(pyjson.Str(detection.Value("ip"))); u != "" {
			out = append(out, candidate{u, "detected_public_ip"})
		}
	}
	seen := map[string]bool{}
	var unique []candidate
	for _, c := range out {
		if !seen[c.url] {
			seen[c.url] = true
			unique = append(unique, c)
		}
	}
	return unique
}

// hostHeaderPort is mobile._host_header_port.
func hostHeaderPort(header string, fallback int) (string, int) {
	text := pytext.Strip(header)
	if text == "" {
		return "", fallback
	}
	host := urlHostname(text)
	port, ok, valid := urlPort(text)
	if !valid || !ok || port == 0 {
		return host, fallback
	}
	return host, port
}

var shlexSafe = regexp.MustCompile(`^[\w@%+=:,./-]+$`)

// shlexQuote is shlex.quote (ASCII \w).
func shlexQuote(s string) string {
	if s == "" {
		return "''"
	}
	if shlexSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func stringsToAny(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// connectionInfo is mobile.connection_info.
func (a *App) connectionInfo(hostHeader, requestScheme string) (*pyjson.Object, []candidate, error) {
	bind := a.BindHost
	if bind == "" {
		bind = "127.0.0.1"
	}
	port := a.Port
	public := pytext.Strip(a.mobilePublicURL()) // or the Cloudflare Tunnel's (mobile_tunnel.go)
	expose := a.Cfg.Expose
	loopback := mobile.IsLoopbackHost(bind)
	var lan []string
	if !loopback {
		lan = privateIPv4()
	}
	lanURLs := []string{}
	if port != 0 {
		for _, ip := range lan {
			lanURLs = append(lanURLs, "http://"+ip+":"+strconv.Itoa(port)+"/")
		}
	}
	reqHost, reqPort := hostHeaderPort(hostHeader, port)
	requestURL := ""
	if hostHeader != "" && !mobile.IsLoopbackHost(reqHost) && !isUnspecified(reqHost) {
		scheme := requestScheme
		if scheme == "" {
			scheme = "http"
		}
		if u, err := baseURL(reqHost, scheme, reqPort); err == nil {
			requestURL = u
		}
	}
	explicit := a.explicitPublicIP()
	detection := detectPublicIP(a.Cfg.DetectPublicIP && !loopback && public == "" && explicit == "")
	var candidates []candidate
	if !loopback || public != "" {
		rh := ""
		if requestURL != "" {
			rh = reqHost
		}
		candidates = a.pairingCandidates(rh, requestScheme, detection)
	}
	candidateURLs := []string{}
	for _, c := range candidates {
		candidateURLs = append(candidateURLs, c.url)
	}
	mode := "loopback"
	advertised := []string{}
	switch {
	case public != "":
		mode, advertised = "public", []string{public}
	case expose && !loopback:
		mode, advertised = "public", candidateURLs
	case !loopback:
		mode, advertised = "lan", lanURLs
	}
	tunnelPort := 8891
	if port != 0 {
		tunnelPort = port + 1
	}
	if raw := os.Getenv("ARK_TUNNEL_PORT"); raw != "" {
		n, err := pytext.Int(raw)
		if err != nil {
			return nil, nil, &pyerr.ValueError{Message: err.Error()}
		}
		tunnelPort = int(n)
	}
	sshTarget := pytext.Strip(os.Getenv("ARK_SSH_TARGET"))
	if sshTarget == "" {
		sshTarget = "user@server"
	}
	arkPort := port
	if arkPort == 0 {
		arkPort = 8890
	}
	if _, err := mobile.ValidatePort(int64(tunnelPort), "local port", 0, false); err != nil {
		return nil, nil, err
	}
	if _, err := mobile.ValidatePort(int64(arkPort), "ARK port", 0, false); err != nil {
		return nil, nil, err
	}
	if strings.ContainsAny(sshTarget, "\r\n\t ") {
		return nil, nil, &mobile.Error{Status: 400, Message: "SSH target must not contain whitespace", Code: "INVALID_SSH_TARGET"}
	}
	command := []string{"ssh", "-N", "-L", "0.0.0.0:" + strconv.Itoa(tunnelPort) + ":127.0.0.1:" + strconv.Itoa(arkPort), sshTarget}
	quoted := make([]string, len(command))
	for i, c := range command {
		quoted[i] = shlexQuote(c)
	}
	autoBase, autoSource := "", ""
	if len(candidates) > 0 {
		autoBase, autoSource = candidates[0].url, candidates[0].source
	}
	if mode == "loopback" {
		autoBase, autoSource = "", "loopback"
	}
	authRequired := a.Auth != nil
	authUser, authNote := "", "No account is configured. ARK is loopback-only by default; it "+
		"refuses a public bind without authentication."
	if authRequired {
		authUser = a.Auth.Username
		authNote = "Sign in with the ARK account before the app or API responds. " +
			"The phone opens the login page first, then claims the pairing code."
	}
	detected := ""
	if len(lan) > 0 {
		detected = lan[0]
	}
	var publicOut any
	if public != "" {
		publicOut = public
	}
	var pairingBase any
	if autoBase != "" {
		pairingBase = autoBase
	}
	publicIPOut := explicit
	if publicIPOut == "" {
		publicIPOut = pyjson.Str(detection.Value("ip"))
	}
	ipSource := ""
	if explicit != "" {
		ipSource = "configured"
	} else if pyjson.Truthy(detection.Value("ip")) {
		ipSource = pyjson.Str(detection.Value("source"))
	}
	secure := mode == "loopback"
	https := false
	if public != "" {
		https = strings.HasPrefix(public, "https://")
		secure = https
	}
	info := pyjson.NewObject("mode", mode, "expose", expose, "loopback_only", mode == "loopback", "bind_host", bind,
		"port", int64(port), "public_url", publicOut, "advertised_urls", stringsToAny(advertised),
		"candidate_urls", stringsToAny(candidateURLs), "lan_urls", stringsToAny(lanURLs), "request_url", requestURL,
		"detected_ip", detected, "detected_port", int64(port), "detected_public_ip", detection.Value("ip"),
		"detected_public_ip_source", detection.Value("source"), "detected_public_ip_error", detection.Value("error"),
		"public_ip", publicIPOut, "public_ip_source", ipSource, "detect_public_ip_enabled", a.Cfg.DetectPublicIP,
		"pairing_auto", mode != "loopback" && autoBase != "", "pairing_base", pairingBase, "pairing_source", autoSource,
		"auth_required", authRequired, "auth_user", authUser, "auth_note", authNote,
		"tunnel", pyjson.NewObject("required", mode == "loopback", "command", strings.Join(quoted, " "),
			"argv", stringsToAny(command), "ssh_target", sshTarget, "local_port", int64(tunnelPort), "ark_port", int64(arkPort),
			"bind", "0.0.0.0", "helper", "python -m arkstudio mobile-tunnel --ssh "+sshTarget+" --local-port "+
				strconv.Itoa(tunnelPort)+" --ark-port "+strconv.Itoa(arkPort),
			"warning", "Run this on a phone-reachable computer, not on the phone. "+
				"It listens on every interface; use a trusted LAN, keep the "+
				"pairing read-only unless you need writes, and stop it afterwards."),
		"https", https, "secure_context", secure,
		"secure_context_note", "The desktop loopback URL is a secure context; a phone-facing LAN "+
			"HTTP URL is not, so install/offline caching may be unavailable.",
		"pwa_note", "Install and offline caching require a secure context (HTTPS or "+
			"localhost). Plain HTTP on a LAN may load the app but the service "+
			"worker and install prompt can be unavailable.",
		"reachability_note", "Addresses are detected from configuration, the request or this "+
			"machine's interfaces. NAT, port forwarding and firewall rules are "+
			"unknown to ARK; test the URL from the phone before relying on it.")
	return info, candidates, nil
}

func mobileErr(err error) error {
	if me, ok := err.(*mobile.Error); ok {
		return apiErr(me.Status, me.Message, me.Code)
	}
	return err
}

// hostHeaderFor is mobile._host_header.
func hostHeaderFor(host string, port int) string {
	text := pytext.Strip(host)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]") {
		if port != 0 {
			return text + ":" + strconv.Itoa(port)
		}
		return text
	}
	if strings.Contains(text, ":") {
		if port != 0 {
			return "[" + text + "]:" + strconv.Itoa(port)
		}
		return "[" + text + "]"
	}
	if port != 0 {
		return text + ":" + strconv.Itoa(port)
	}
	return text
}

// resolvePairingBase is mobile.resolve_pairing_base.
func (a *App) resolvePairingBase(overrideHost string, overridePort any, overrideBase, requestHost string, requestPort int,
	requestScheme string) (string, string, error) {
	if public := pytext.Strip(a.mobilePublicURL()); public != "" {
		u, err := mobile.NormalizePublicURL(public)
		return u, a.mobilePublicSource(), err
	}
	if pytext.Strip(overrideBase) != "" {
		u, err := mobile.NormalizePublicURL(overrideBase)
		return u, "override_base", err
	}
	if host := pytext.Strip(overrideHost); host != "" {
		clean, err := mobile.ValidateTunnelHost(host)
		if err != nil {
			return "", "", err
		}
		info, _, err := a.connectionInfo("", "")
		if err != nil {
			return "", "", err
		}
		def := int(info.Value("tunnel").(*pyjson.Object).Value("local_port").(int64))
		if def == 0 {
			def = a.Port
		}
		port, err := mobile.ValidatePort(overridePort, "tunnel port", def, true)
		if err != nil {
			return "", "", err
		}
		u, err := baseURL(clean, "http", port)
		return u, "tunnel_target", err
	}
	info, _, err := a.connectionInfo(hostHeaderFor(requestHost, requestPort), requestScheme)
	if err != nil {
		return "", "", err
	}
	if b := info.Value("pairing_base"); pyjson.Truthy(b) {
		return pyjson.Str(b), pyjson.Str(info.Value("pairing_source")), nil
	}
	if m := info.Value("mode"); m == "public" || m == "lan" {
		return "", "lan_unresolved", nil
	}
	return "", "loopback", nil
}

// requestHostPort is Handler._request_host_port.
func (c *reqCtx) requestHostPort() (string, int) {
	header := c.header("Host")
	host := urlHostname(header)
	port, ok, valid := urlPort(header)
	if !valid || !ok || port == 0 {
		return host, c.app.Port
	}
	return host, port
}

// ----------------------------------------------------------- guards

// mobileSessionForProject is Handler._mobile_session_for_project.  A
// paired phone is signed in as its user (not into one project): the session
// is valid while the account can read some project, and mobileProjectOf
// moves it off a project that is gone or no longer readable.
func (c *reqCtx) mobileSessionForProject() *mobile.Session {
	token := c.mobileToken()
	if token == "" {
		return nil
	}
	s := c.app.Mobile.SessionForToken(token)
	if s == nil {
		return nil
	}
	if c.app.Auth != nil && s.User == "" {
		return nil
	}
	if p, err := c.mobileProjectOf(s); err != nil || p == nil {
		return nil
	}
	return s
}

// mobileProjectOf is the project a phone's session is on: its current
// project while the account may read it, otherwise the account's first
// readable project (the session moves there), or nil when there is none.
func (c *reqCtx) mobileProjectOf(s *mobile.Session) (*project.Project, error) {
	p, err := c.projectByID(s.ProjectID)
	if err != nil {
		return nil, err
	}
	if c.app.Auth == nil {
		if p == nil {
			c.app.mu.Lock()
			p = c.app.project
			c.app.mu.Unlock()
		}
		return p, nil
	}
	readable := func(q *project.Project) (bool, error) {
		if q == nil || q.ID() == demoID {
			return false, nil
		}
		perm, err := c.app.Org.ProjectPermission(c.ctx, s.User, q.ID())
		return perm != "", err
	}
	ok, err := readable(p)
	if err != nil || ok {
		return p, err
	}
	if c.app.Mobile.IsDesktopLink(s) {
		return nil, nil
	}
	visible, err := c.app.Org.VisibleProjectIDs(c.ctx, s.User)
	if err != nil {
		return nil, err
	}
	for _, summary := range c.app.Projects.ListProjects() {
		id, _ := summary.Value("id").(string)
		if id == demoID || !visible[id] {
			continue
		}
		q, err := c.projectByID(id)
		if err != nil {
			return nil, err
		}
		ok, err := readable(q)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if err := c.app.Mobile.Move(s, q.ID(), q.Name()); err != nil {
			return nil, nil
		}
		return q, nil
	}
	return nil, nil
}

func (c *reqCtx) bindMobile(user string, s *mobile.Session) {
	c.user = user
	c.forcedProject = s.ProjectID
	c.mobileSession = s
	c.resolved = false
}

var (
	mobileDevicePrefixes = []string{"/api/bootstrap", "/api/project", "/api/revision", "/api/units",
		"/api/links", "/api/media", "/api/graph", "/api/timeline", "/api/topics",
		"/api/activity", "/api/chat", "/api/skills", "/api/infohub",
		"/api/import", "/api/export", "/api/assistant/insights",
		"/api/assistant/dispatch", "/api/assistant/apply",
		"/api/assistant/extract", "/api/agent-sessions", "/api/workspace",
		"/api/mobile/session", "/api/health", "/api/changes", "/api/events",
		"/api/unit-details",
		"/api/trash"} // Go only: the recycle bin (routes_trash.go)
	mobileScopedGet = map[string]bool{"/api/org": true, "/api/projects": true, "/api/gpu/dashboard": true,
		notificationsPath:  true, // Go only (notifications.go)
		"/api/mobile/apps": true} // the app downloads (/download/android-apps.json lists them publicly)
)

// mobileDevicePathAllowed is server.mobile_device_path_allowed.
func mobileDevicePathAllowed(path, method string) bool {
	verb := strings.ToUpper(method)
	if path == "/api/me" || path == "/api/me/avatar" || path == "/api/me/password" || reAvatar.MatchString(path) {
		if path == "/api/me/password" {
			return false
		}
		return verb == "GET" || verb == "HEAD" || path == "/api/me" || path == "/api/me/avatar"
	}
	if mobileScopedGet[path] {
		return verb == "GET" || verb == "HEAD"
	}
	for _, prefix := range mobileDevicePrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// guardMobile is Handler._guard_mobile.
func (c *reqCtx) guardMobile() error {
	path := c.path
	if !strings.HasPrefix(path, "/api/") {
		return nil
	}
	if path == "/api/health" || path == "/api/mobile/pair/claim" || path == "/api/mobile/login" {
		return nil
	}
	if c.token != nil && c.personalAgentPathAllowed(path) {
		return nil
	}
	if strings.HasPrefix(path, "/api/task-agent/") && c.taskReadSession() != nil {
		return nil
	}
	if path == "/api/mobile/session" && c.r.Method != "DELETE" {
		return nil
	}
	token := c.mobileToken()
	var ms *mobile.Session
	var p *project.Project
	if token != "" {
		ms = c.app.Mobile.SessionForToken(token)
		if ms == nil {
			return apiErr(401, "The mobile session is invalid or has expired.", "MOBILE_TOKEN_INVALID")
		}
		var err error
		if p, err = c.mobileProjectOf(ms); err != nil {
			return err
		}
		if p == nil {
			return apiErr(404, "No project is available for this account yet. Ask an "+
				"organization admin for a project grant.", "NO_PROJECT_ACCESS")
		}
	}
	session, err := c.authSession()
	if err != nil {
		return err
	}
	local := c.trustedLocal()
	management := path == "/api/mobile/connection" || path == "/api/mobile/pair" || path == "/api/mobile/pairs" ||
		strings.HasPrefix(path, "/api/mobile/pairs/")
	if management {
		if ms != nil {
			return apiErr(403, "A paired phone cannot manage pairing codes.", "MOBILE_SCOPE")
		}
		if session == nil && !local {
			return apiErr(401, "Sign in to manage pairing codes.", "AUTH_REQUIRED")
		}
		return nil
	}
	if path == "/api/mobile/session" && c.r.Method == "DELETE" {
		if ms == nil {
			return apiErr(401, "The mobile session is invalid or has expired.", "MOBILE_TOKEN_INVALID")
		}
		c.session = session
		c.bindMobile(ms.User, ms)
		return nil
	}
	if ms != nil {
		summaryID := p.ID()
		if ms, err = c.app.Mobile.Authenticate(token, summaryID, c.r.Method, path); err != nil {
			return mobileErr(err)
		}
		c.mobileSession = ms
		user := ms.User
		if user == "" && session != nil {
			user = session.Username
		}
		if user == "" && c.app.Auth != nil {
			user = c.app.Auth.Username
		}
		if session != nil && user != "" && strings.ToLower(session.Username) != strings.ToLower(user) {
			return apiErr(403, "This mobile session belongs to another account.", "PAIRING_WRONG_USER")
		}
		c.session = session
		c.bindMobile(user, ms)
		if id := c.clientDeviceID(nil); id != "" {
			c.app.Mobile.AdoptDevice(ms, id)
		}
		// A desktop link (Go only, mobile/desktop.go) lists and creates
		// projects, nothing else.
		if c.app.Mobile.IsDesktopLink(ms) {
			if !mobile.DesktopLinkPathAllowed(path, c.r.Method) {
				return apiErr(403, "A linked ARK Studio desktop may only list and create projects on this server.",
					"DESKTOP_LINK_SCOPE")
			}
			return nil
		}
		switch {
		case path == "/api/agent/token":
			return apiErr(403, "The desktop agent token is never exposed to paired phones.", "MOBILE_SECRET_BLOCKED")
		case path == "/api/open":
			return apiErr(403, "A paired phone cannot open files or applications on the host.", "MOBILE_HOST_ACTION_BLOCKED")
		case path == "/api/projects/open":
			// The web app's project list: the session moves (switchProject).
			return nil
		}
		if !mobileDevicePathAllowed(path, c.r.Method) {
			return apiErr(403, "This API is not available to a paired device. Use the "+
				"desktop ARK window to manage the organization, personal "+
				"credentials or providers.", "MOBILE_SCOPE")
		}
		return nil
	}
	if session != nil {
		c.session, c.user = session, session.Username
		return nil
	}
	if !local {
		return apiErr(401, "Pair this phone from ARK Settings → Mobile on the desktop. "+
			"The pairing link was opened without its one-time code.", "MOBILE_TOKEN_REQUIRED")
	}
	return nil
}

// ----------------------------------------------------------- routes

// androidApps lists the Android app.  Go-only: url is the APK asset of
// the latest GitHub release (app_release.go), or the release page when it
// cannot be resolved; qr_svg encodes url for a phone.  The APK always comes
// from GitHub: local is false (a server does not serve its own builds; the
// fields stay for older pages).
func (a *App) androidApps(rel *appRelease) *pyjson.Object {
	out := pyjson.NewObject()
	for _, item := range []struct{ key, path, appID, title string }{
		{"native", "/download/ARK-Studio-android.apk", "com.masteryip.arkstudio", "ARK Studio (native)"},
	} {
		url, file, direct := rel.url(item.key)
		svg, err := qr.SVG(url)
		if err != nil {
			svg = ""
		}
		out.Set(item.key, pyjson.NewObject("available", true, "url", url, "file", file, "direct", direct,
			"version", rel.Version, "qr_svg", svg, "app_id", item.appID, "title", item.title,
			"local", false, "local_url", "."+item.path, "size", int64(0), "updated", nil))
	}
	return out
}

// mobileGet handles the GET /api/mobile/* routes.
func (c *reqCtx) mobileGet(path string) (bool, error) {
	switch path {
	case "/api/mobile/apps":
		rel := c.app.appRelease.resolve(c.ctx)
		c.ok(pyjson.NewObject("apps", c.app.androidApps(rel), "version", rel.Version, "release_url", rel.Page))
		return true, nil
	case "/api/mobile/connection":
		info, _, err := c.app.connectionInfo(c.header("Host"), c.requestScheme())
		if err != nil {
			return true, mobileErr(err)
		}
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		info.Set("pairs_pending", c.app.Mobile.PendingCount(p.ID()))
		c.app.mu.Lock()
		hosts := make([]string, 0, len(c.app.allowedHosts))
		for h := range c.app.allowedHosts {
			hosts = append(hosts, h)
		}
		c.app.mu.Unlock()
		sort.Strings(hosts)
		info.Set("allowed_hosts", stringsToAny(hosts))
		c.ok(info)
		return true, nil
	case "/api/mobile/pairs":
		// The person's devices on every project (a pairing is the user's).
		c.ok(pyjson.NewObject("pairs", c.userDevices(), "scope", "user"))
		return true, nil
	case "/api/mobile/session/projects":
		out, err := c.mobileProjects()
		if err == nil {
			c.ok(out)
		}
		return true, err
	case "/api/mobile/session":
		if c.app.Auth != nil {
			session, err := c.authSession()
			if err != nil {
				return true, err
			}
			if session == nil && c.mobileSessionForProject() == nil {
				c.ok(pyjson.NewObject("required", true, "authenticated", false, "token_present", c.mobileToken() != "",
					"reason", "login_required", "login_required", true, "permission", nil, "read_only", nil,
					"expires_at", nil, "project", nil, "auth_required", true, "auth_user", nil,
					"cache_id", nil, "cache_schema", clientCacheSchema, "connection", pyjson.NewObject("mode", "public", "loopback_only", false, "public_url", nil,
						"advertised_urls", []any{}, "auth_required", true)))
				return true, nil
			}
		}
		session, err := c.authSession()
		if err != nil {
			return true, err
		}
		required := !c.trustedLocal() && session == nil
		token := c.mobileToken()
		out, err := c.mobileStatus(token, required)
		if err == nil {
			// Refused for where it comes from, and not plausibly a phone:
			// the page names that reason instead of asking for a code.
			if required && token == "" && !c.phonePlausible() {
				out.Set("reason", "not_local")
			}
			c.ok(out)
		}
		return true, err
	}
	return false, nil
}

// mobileUA matches phone and tablet browsers and the Android app.
var mobileUA = regexp.MustCompile(`(?i)android|iphone|ipad|ipod|mobile|harmonyos|arkstudioandroid`)

// phonePlausible reports whether a request that needs pairing may come
// from a phone: through the Cloudflare Tunnel, or from a phone browser to a
// server phones can reach (bound beyond loopback or with a public URL). A
// desktop browser, or the desktop window through a proxy, is not.
func (c *reqCtx) phonePlausible() bool {
	if viaTunnel(c.r) || cloudflareForwarded(c.r) {
		return true
	}
	bind := c.app.BindHost
	if (bind == "" || mobile.IsLoopbackHost(bind)) && pytext.Strip(c.app.mobilePublicURL()) == "" {
		return false
	}
	return mobileUA.MatchString(c.header("User-Agent"))
}

// mobileStatus is App.mobile_status.
func (c *reqCtx) mobileStatus(token string, required bool) (*pyjson.Object, error) {
	p, err := c.require("read")
	if err != nil {
		return nil, err
	}
	s := c.app.Mobile.SessionForToken(token)
	authenticated := s != nil
	if authenticated {
		if id := c.clientDeviceID(nil); id != "" {
			c.app.Mobile.AdoptDevice(s, id)
		}
	}
	reason := ""
	if token != "" && s == nil {
		reason = "invalid_or_expired"
	}
	info, _, err := c.app.connectionInfo(c.header("Host"), c.requestScheme())
	if err != nil {
		return nil, mobileErr(err)
	}
	var perm, readOnly, expires, source, user, access any
	switchable := false
	if authenticated {
		access = c.mobileAccess(s, p)
		switchable = !c.app.Mobile.IsDesktopLink(s)
		perm, readOnly, expires = s.Permission, s.Permission == "read_only", mobile.ISO(s.ExpiresAt)
		source = "qr"
		if r := c.app.Mobile.PairingFor(s); r != nil {
			source = r.Source
		}
		if s.User != "" {
			user = s.User
		}
	}
	var authUser any
	if c.app.Auth != nil {
		authUser = c.actor()
	}
	return pyjson.NewObject("required", required, "authenticated", authenticated, "token_present", token != "",
		"reason", reason, "permission", perm, "read_only", readOnly, "expires_at", expires,
		"project", pyjson.NewObject("id", p.ID(), "name", p.Name()), "source", source, "user", user,
		"scope", "user", "switchable", switchable, "access", access,
		"auth_required", c.app.Auth != nil, "auth_user", authUser,
		"cache_id", c.cacheIDOrNil(), "cache_schema", clientCacheSchema,
		"connection", pyjson.NewObject("mode", info.Value("mode"), "loopback_only", info.Value("loopback_only"),
			"public_url", info.Value("public_url"), "advertised_urls", info.Value("advertised_urls"),
			"auth_required", info.Value("auth_required"), "pwa_note", info.Value("pwa_note"))), nil
}

// mobileAccountPermission is Handler._mobile_account_permission.
func (c *reqCtx) mobileAccountPermission(user string, p *project.Project) (string, error) {
	if p == nil || user == "" || p.ID() == demoID {
		return "", nil
	}
	return c.app.Org.ProjectPermission(c.ctx, user, p.ID())
}

// devicePermission is what a new device may do on every project: "full"
// (all its account may do in each project) unless read_only is asked for.
// The account's role in each project still applies (checkProjectAccess), so
// full never exceeds it.  An account without any project gets read_only.
func devicePermission(requested any, account string) (string, error) {
	text := mobile.DefaultPermission
	if pyjson.Truthy(requested) {
		text = pyjson.Str(requested)
	}
	wanted := strings.ReplaceAll(pytext.Lower(pytext.Strip(text)), "-", "_")
	if wanted != "read_only" && wanted != "full" {
		return "", apiErr(400, "permission must be read_only or full", "INVALID_PERMISSION")
	}
	if _, ok := org.PermissionRank[account]; !ok {
		return "read_only", nil
	}
	return wanted, nil
}

// mobileAccess is what the phone may do on p now: the account's role there
// ("read", "write", "admin"), read at most for a read-only device.
func (c *reqCtx) mobileAccess(s *mobile.Session, p *project.Project) any {
	if s == nil || p == nil {
		return nil
	}
	perm := "admin"
	if c.app.Auth != nil {
		var err error
		if perm, err = c.mobileAccountPermission(s.User, p); err != nil || perm == "" {
			return nil
		}
	}
	if s.Permission == "read_only" {
		return "read"
	}
	return perm
}

// userDevices lists the pairings of the signed-in person (every pairing on
// a server without accounts).
func (c *reqCtx) userDevices() []any {
	return c.app.Mobile.UserDevices(c.actor(), c.app.Auth == nil)
}

// mobileProjectList is Handler._mobile_project_list.
func (c *reqCtx) mobileProjectList(user string) ([]any, error) {
	if c.app.Auth != nil {
		c.user = user
		c.resolved = false
	}
	visible, err := c.visibleProjects()
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, item := range visible {
		o := item.(*pyjson.Object)
		if o.Value("id") == demoID {
			continue
		}
		out = append(out, pyjson.NewObject("id", o.Value("id"), "name", o.Value("name"), "permission", o.Value("permission")))
	}
	return out, nil
}

// mobileProjects lists the projects a phone can switch to: every project
// its account can read (all projects on a server without accounts).
func (c *reqCtx) mobileProjects() (*pyjson.Object, error) {
	s := c.mobileSession
	if s == nil {
		return nil, apiErr(401, "A mobile session is required.", "MOBILE_TOKEN_REQUIRED")
	}
	source := "qr"
	if r := c.app.Mobile.PairingFor(s); r != nil {
		source = r.Source
	}
	switchable := !c.app.Mobile.IsDesktopLink(s)
	var projects []any
	if switchable {
		var err error
		if projects, err = c.mobileProjectList(s.User); err != nil {
			return nil, err
		}
	} else {
		projects = []any{pyjson.NewObject("id", s.ProjectID, "name", s.ProjectName, "permission", "read")}
	}
	if s.Permission == "read_only" {
		for _, item := range projects {
			item.(*pyjson.Object).Set("permission", "read")
		}
	}
	var user any
	if s.User != "" {
		user = s.User
	}
	return pyjson.NewObject("projects", projects, "current", s.ProjectID, "switchable", switchable,
		"permission", s.Permission, "source", source, "user", user, "scope", "user"), nil
}

// mobilePost handles the POST /api/mobile/* routes of do_POST.
func (c *reqCtx) mobilePost(path string, body *pyjson.Object) (bool, error) {
	switch {
	case path == "/api/mobile/pair":
		host, port := c.requestHostPort()
		return true, c.createPairing(body, host, port, c.requestScheme())
	case path == "/api/mobile/pair/claim":
		result, err := c.claimPairing(body)
		if err != nil {
			return true, err
		}
		var headers headerList
		if token := pyjson.Str(result.Value("mobile_token")); token != "" {
			expires, _ := result.Value("expires_in").(int64)
			parts := []string{"ark_mobile=" + token, "Path=" + c.externalPrefix(), "HttpOnly", "SameSite=Lax",
				"Max-Age=" + strconv.FormatInt(expires, 10)}
			if c.secure() {
				parts = append(parts, "Secure")
			}
			headers = headerList{{"Set-Cookie", strings.Join(parts, "; ")}}
			result.Set("mobile_user", strOrEmptyAny(result.Value("user")))
		}
		payload := pyjson.NewObject("ok", true)
		for _, k := range result.Keys() {
			payload.Set(k, result.Value(k))
		}
		c.sendJSON(200, payload, headers)
		return true, nil
	case path == "/api/mobile/login":
		return true, c.mobileLogin(body)
	case path == "/api/mobile/session/project":
		out, err := c.mobileSwitchProject(body)
		if err == nil {
			c.ok(out)
		}
		return true, err
	case path == mobileMergePath:
		return true, c.mobileMerge(body)
	case path == mobileStalePath:
		return true, c.mobileStale(body)
	case reMobileRevoke.MatchString(path):
		// A person revokes their own devices (a pairing is the user's, not a
		// project's); audited in the org log.
		revoked, err := c.app.Mobile.RevokeOwned(reMobileRevoke.FindStringSubmatch(path)[1], c.actor(), c.app.Auth == nil)
		if err != nil {
			return true, mobileErr(err)
		}
		if err := c.auditPair("mobile.pair.revoke", revoked); err != nil {
			return true, err
		}
		c.ok(pyjson.NewObject("revoked", revoked, "pairs", c.userDevices()))
		return true, nil
	}
	return false, nil
}

func strOrEmptyAny(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

// createPairing is App.create_pairing.
func (c *reqCtx) createPairing(body *pyjson.Object, requestHost string, requestPort int, scheme string) error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	// "full" is all the account may do in each project (its role there
	// still applies), so a viewer may pair a full-access phone too.
	permission := pytext.Lower(pytext.Strip(strOrEmptyAny(body.Value("permission"))))
	if permission == "" {
		permission = mobile.DefaultPermission
	}
	actor := c.actor()
	overrideHost := strOrEmptyAny(body.Value("tunnel_host"))
	if overrideHost == "" {
		overrideHost = strOrEmptyAny(body.Value("advertised_host"))
	}
	overrideHost = pytext.Strip(overrideHost)
	overridePort := body.Value("tunnel_port")
	if !pyjson.Truthy(overridePort) {
		overridePort = body.Value("advertised_port")
	}
	overrideBase := pytext.Strip(strOrEmptyAny(body.Value("public_url")))
	base, source, err := c.app.resolvePairingBase(overrideHost, overridePort, overrideBase, requestHost, requestPort, scheme)
	if err != nil {
		return mobileErr(err)
	}
	if base == "" {
		if source == "lan_unresolved" {
			return apiErr(409, "ARK is reachable on a non-loopback interface but no usable "+
				"address could be selected. Set --public-url/ARK_PUBLIC_URL, "+
				"or use the loopback SSH tunnel with a tunnel host.", "MOBILE_LAN_UNRESOLVED")
		}
		return apiErr(409, "ARK is loopback-only. Start the SSH tunnel on a phone-reachable "+
			"computer and enter that computer's LAN address, or expose ARK "+
			"with --expose/--public-url and account authentication. A phone "+
			"cannot run SSH by itself.", "MOBILE_LOOPBACK_ONLY")
	}
	pair, err := c.app.Mobile.Create(p.ID(), p.Name(), permission, body.Value("expires_in"), body.Value("session_ttl"),
		"desktop", actor, "qr")
	if err != nil {
		return mobileErr(err)
	}
	pairingURL, err := mobile.BuildPairingURL(base, pyjson.Str(pair.Value("token")))
	if err != nil {
		return mobileErr(err)
	}
	svg, err := qr.SVG(pairingURL)
	if err != nil {
		return &pyerr.InternalError{Message: err.Error()}
	}
	c.app.allowAdvertisedURL(base)
	// A pairing code changes nothing in the project: it is audited in the
	// org log only.  A project activity row would bump the project revision,
	// and every open tab of the project would refetch and repaint (Settings
	// generated a code on each paint, so it repainted every ~2 s).  Claiming
	// the code (a device joins) is logged in the project.
	actorID, err := c.app.Org.UserIDFor(c.ctx, actor)
	if err != nil {
		return err
	}
	if err := c.app.Org.Log(c.ctx, actorID, "mobile.pair.create", p.ID(), pyjson.NewObject("pair", pair.Value("id"),
		"permission", pair.Value("permission"))); err != nil {
		return err
	}
	user := pair.Value("user")
	if !pyjson.Truthy(user) {
		user = actor
	}
	summary, err := p.Summary()
	if err != nil {
		return err
	}
	c.ok(pyjson.NewObject("pair", pyjson.NewObject("id", pair.Value("id"), "permission", pair.Value("permission"),
		"project", pair.Value("project_name"), "user", user, "created_at", pair.Value("created_at"),
		"expires_at", pair.Value("expires_at"), "expires_in", pair.Value("expires_in"),
		"session_permanent", pair.Value("session_permanent"), "session_ttl", pair.Value("session_ttl")),
		"code", pair.Value("token"), "pairing_url", pairingURL, "payload", pairingURL, "qr_svg", svg,
		"base_url", base, "base_source", source, "auto", overrideHost == "" && overrideBase == "",
		"auth_required", c.app.Auth != nil, "requires_login", c.app.Auth != nil, "project", summary))
	return nil
}

// claimPairing is Handler._claim_pairing.
func (c *reqCtx) claimPairing(body *pyjson.Object) (*pyjson.Object, error) {
	tokenArg := body.Value("token")
	if !pyjson.Truthy(tokenArg) {
		tokenArg = body.Value("code")
	}
	token := strOrEmptyAny(tokenArg)
	record := c.app.Mobile.Find(token)
	if record == nil {
		if _, err := c.app.Mobile.Claim(token, "", c.clientIP(), c.header("User-Agent")); err != nil {
			return nil, mobileErr(err)
		}
		return nil, apiErr(401, "The pairing code is invalid or has expired.", "PAIRING_INVALID")
	}
	p, err := c.projectByID(record.ProjectID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, apiErr(404, "The paired project is no longer available.", "MOBILE_WRONG_PROJECT")
	}
	current := ""
	if c.app.Auth != nil {
		current = c.actor()
	}
	if current != "" && record.User != "" && strings.ToLower(current) != strings.ToLower(record.User) {
		return nil, apiErr(403, "This pairing code belongs to a different ARK account.", "PAIRING_WRONG_USER")
	}
	if c.app.Auth != nil {
		if record.User == "" {
			return nil, apiErr(403, "This pairing code has no bound account.", "PAIRING_UNBOUND")
		}
		if _, err := c.app.Org.RequireProjectPermission(c.ctx, record.User, p.ID(), "read"); err != nil {
			return nil, err
		}
	}
	ua := c.header("User-Agent")
	if device := pytext.Prefix(pytext.Strip(strOrEmptyAny(body.Value("device"))), 80); device != "" {
		ua = device + " · " + ua
	}
	result, err := c.app.Mobile.ClaimDevice(token, p.ID(), c.clientIP(), ua, c.clientDeviceID(body))
	if err != nil {
		return nil, mobileErr(err)
	}
	if err := p.Store.LogActivity(c.ctx, "mobile:pair", "mobile.pair.claim", nil, pyjson.NewObject("pair", result.Value("pairing_id"),
		"permission", result.Value("permission"))); err != nil {
		return nil, err
	}
	userID, err := c.app.Org.UserIDFor(c.ctx, record.User)
	if err != nil {
		return nil, err
	}
	if err := c.app.Org.Log(c.ctx, userID, "mobile.pair.claim", p.ID(), pyjson.NewObject("pair", result.Value("pairing_id"),
		"permission", result.Value("permission"))); err != nil {
		return nil, err
	}
	result.Set("user", record.User).Set("scope", "user").Set("project_id", p.ID())
	return result, nil
}

// mobileLogin is Handler._mobile_login.
func (c *reqCtx) mobileLogin(body *pyjson.Object) error {
	if c.app.Auth == nil {
		return apiErr(400, "This ARK server has no account sign-in. Pair the phone with the "+
			"QR code from Settings → Mobile instead.", "AUTH_NOT_CONFIGURED")
	}
	ctype := pytext.Lower(pytext.Strip(strings.SplitN(c.header("Content-Type"), ";", 2)[0]))
	if ctype != "application/json" {
		return apiErr(415, "Send the sign-in as application/json.", "UNSUPPORTED_MEDIA_TYPE")
	}
	client := c.clientIP()
	user, err := c.app.Auth.VerifyCredentials(c.ctx, strOrEmptyAny(body.Value("username")), strOrEmptyAny(body.Value("password")), client)
	if err != nil {
		if ae, ok := err.(*auth.Error); ok {
			var headers headerList
			if ae.Status == 429 {
				headers = headerList{{"Retry-After", strconv.Itoa(max(1, ae.RetryAfter))}}
			}
			c.sendJSON(ae.Status, pyjson.NewObject("ok", false, "error", pyjson.NewObject("status", int64(ae.Status),
				"code", ae.Code, "message", ae.Message)), headers)
			return nil
		}
		return err
	}
	c.user = user
	c.resolved = false
	wanted := pytext.Strip(strOrEmptyAny(body.Value("project_id")))
	var p *project.Project
	if wanted != "" {
		p, err = c.projectByID(wanted)
	} else {
		p, err = c.defaultProjectFor(user)
	}
	if err != nil {
		return err
	}
	account, err := c.mobileAccountPermission(user, p)
	if err != nil {
		return err
	}
	if p == nil || account == "" {
		if wanted != "" {
			return apiErr(404, "No project is available for this account yet. Ask an "+
				"organization admin for a project grant.", "PROJECT_NOT_ACCESSIBLE")
		}
		return apiErr(409, "No project is available for this account yet. Ask an "+
			"organization admin for a project grant.", "NO_PROJECT_ACCESS")
	}
	permission, err := devicePermission(body.Value("permission"), account)
	if err != nil {
		return err
	}
	agent := c.header("User-Agent")
	device := pytext.Prefix(pytext.Strip(strOrEmptyAny(body.Value("device"))), 80)
	pair, err := c.app.Mobile.Create(p.ID(), p.Name(), permission, int64(60), body.Value("session_ttl"), "login:"+client, user, "password")
	if err != nil {
		return mobileErr(err)
	}
	ua := agent
	if device != "" {
		ua = device + " · " + agent
	}
	result, err := c.app.Mobile.ClaimDevice(pyjson.Str(pair.Value("token")), p.ID(), "login:"+client, ua, c.clientDeviceID(body))
	if err != nil {
		return mobileErr(err)
	}
	if err := p.Store.LogActivity(c.ctx, "human:mobile", "mobile.login", nil, pyjson.NewObject("pair", pair.Value("id"),
		"permission", permission)); err != nil {
		return err
	}
	userID, err := c.app.Org.UserIDFor(c.ctx, user)
	if err != nil {
		return err
	}
	if err := c.app.Org.Log(c.ctx, userID, "mobile.login", p.ID(), pyjson.NewObject("pair", pair.Value("id"), "permission", permission)); err != nil {
		return err
	}
	projects, err := c.mobileProjectList(user)
	if err != nil {
		return err
	}
	result.Set("source", "password").Set("project_id", p.ID()).Set("account_permission", account).Set("projects", projects)
	c.ok(result)
	return nil
}

// mobileSwitchProject moves a phone to another project of its account
// (native project switcher).  Every paired phone may switch, QR or password:
// the pairing is the user's.  The device permission does not change; the
// account's role in the new project applies.
func (c *reqCtx) mobileSwitchProject(body *pyjson.Object) (*pyjson.Object, error) {
	s := c.mobileSession
	if s == nil {
		return nil, apiErr(401, "A mobile session is required.", "MOBILE_TOKEN_REQUIRED")
	}
	p, err := c.projectByID(strOrEmptyAny(body.Value("project_id")))
	if err != nil {
		return nil, err
	}
	if p == nil || p.ID() == demoID {
		return nil, apiErr(404, "Project not found or not accessible for this account.", "PROJECT_NOT_ACCESSIBLE")
	}
	if c.app.Auth != nil {
		account, err := c.mobileAccountPermission(s.User, p)
		if err != nil {
			return nil, err
		}
		if account == "" {
			return nil, apiErr(404, "Project not found or not accessible for this account.", "PROJECT_NOT_ACCESSIBLE")
		}
	}
	if err := c.moveMobile(p); err != nil {
		return nil, err
	}
	if err := p.Store.LogActivity(c.ctx, "human:mobile", "mobile.switch_project", nil, pyjson.NewObject("pair", s.PairingID,
		"permission", s.Permission)); err != nil {
		return nil, err
	}
	return pyjson.NewObject("project_id", p.ID(), "project", p.Name(), "permission", s.Permission,
		"read_only", s.Permission == "read_only", "access", c.mobileAccess(s, p)), nil
}

// moveMobile points this request's phone session at p (the caller checked
// access).  Without accounts the server has one current project, which the
// phone's owner switches like on the desktop.
func (c *reqCtx) moveMobile(p *project.Project) error {
	s := c.mobileSession
	if _, err := c.app.Mobile.Rebind(c.mobileToken(), p.ID(), p.Name()); err != nil {
		return mobileErr(err)
	}
	if c.app.Auth == nil {
		c.app.mu.Lock()
		c.app.project = p
		c.app.mu.Unlock()
	} else if userID, err := c.app.Org.UserIDFor(c.ctx, s.User); err != nil {
		return err
	} else if userID != "" {
		if err := c.app.Org.SetLastProject(c.ctx, userID, p.ID()); err != nil {
			return err
		}
	}
	c.forcedProject = p.ID()
	c.project, c.resolved = p, true
	return nil
}

// mobilePairPermission sets what one of the person's devices may do
// (Settings → Mobile): full access as the user or read-only.
func (c *reqCtx) mobilePairPermission(pairID string, body *pyjson.Object) error {
	if c.mobileSession != nil {
		return apiErr(403, "A paired phone cannot manage pairing codes.", "MOBILE_SCOPE")
	}
	changed, err := c.app.Mobile.SetPermission(pairID, c.actor(), c.app.Auth == nil, body.Value("permission"))
	if err != nil {
		return mobileErr(err)
	}
	if err := c.auditPair("mobile.pair.permission", changed); err != nil {
		return err
	}
	c.ok(pyjson.NewObject("pair", changed, "pairs", c.userDevices()))
	return nil
}

// mobileMerge merges the person's devices into one (Settings → Mobile and
// the resources graph): {ids: [pair ids]} or {like: pair id} (every active
// device of the same type), dry_run previews.  The most recently used
// device stays signed in; the others are revoked.
func (c *reqCtx) mobileMerge(body *pyjson.Object) error {
	if c.mobileSession != nil {
		return apiErr(403, "A paired phone cannot manage pairing codes.", "MOBILE_SCOPE")
	}
	var ids []string
	if list, ok := body.Value("ids").([]any); ok {
		for _, v := range list {
			ids = append(ids, pyjson.Str(v))
		}
	}
	like := pytext.Strip(strOrEmptyAny(body.Value("like")))
	dryRun := pyjson.Truthy(body.Value("dry_run"))
	plan, err := c.app.Mobile.MergeDevices(c.actor(), c.app.Auth == nil, ids, like, dryRun)
	if err != nil {
		return mobileErr(err)
	}
	if !dryRun {
		if revoked, ok := plan.Value("revoke").([]any); ok {
			for _, r := range revoked {
				if err := c.auditPair("mobile.pair.merge", r.(*pyjson.Object)); err != nil {
					return err
				}
			}
		}
	}
	plan.Set("pairs", c.userDevices())
	c.ok(plan)
	return nil
}

// mobileStale lists (dry_run) or revokes the person's devices unseen for 30
// days.  Nothing is revoked without this call.
func (c *reqCtx) mobileStale(body *pyjson.Object) error {
	if c.mobileSession != nil {
		return apiErr(403, "A paired phone cannot manage pairing codes.", "MOBILE_SCOPE")
	}
	dryRun := pyjson.Truthy(body.Value("dry_run"))
	stale := c.app.Mobile.StaleDevices(c.actor(), c.app.Auth == nil, dryRun)
	if !dryRun {
		for _, r := range stale {
			if err := c.auditPair("mobile.pair.revoke_stale", r.(*pyjson.Object)); err != nil {
				return err
			}
		}
	}
	c.ok(pyjson.NewObject("stale", stale, "dry_run", dryRun, "pairs", c.userDevices()))
	return nil
}

// auditPair writes a device change to the org log.
func (c *reqCtx) auditPair(action string, pair *pyjson.Object) error {
	actorID, err := c.app.Org.UserIDFor(c.ctx, c.actor())
	if err != nil {
		return err
	}
	return c.app.Org.Log(c.ctx, actorID, action, "", pyjson.NewObject("pair", pair.Value("id"),
		"permission", pair.Value("permission")))
}

// mobileDelete is DELETE /api/mobile/session.
func (c *reqCtx) mobileDelete() error {
	removed := c.app.Mobile.RevokeSession(c.mobileToken())
	parts := []string{"ark_mobile=", "Path=" + c.externalPrefix(), "HttpOnly", "SameSite=Lax", "Max-Age=0"}
	if c.secure() {
		parts = append(parts, "Secure")
	}
	c.sendJSON(200, pyjson.NewObject("ok", true, "logged_out", removed), headerList{{"Set-Cookie", strings.Join(parts, "; ")}})
	return nil
}

type apkInfo struct{ file, title string }

// apkByPath is ANDROID_APKS by download path.  The WebView app's old path
// (no longer built) serves the native app, so old links and QR codes still
// install ARK Studio.
var apkByPath = map[string]apkInfo{
	"/download/ARK-Studio-android.apk": {"ARK-Studio-android.apk", "ARK Studio (native)"},
	"/download/ark.apk":                {"ARK-Studio-android.apk", "ARK Studio (native)"},
	"/download/ARK-Studio-webapp.apk":  {"ARK-Studio-android.apk", "ARK Studio (native)"},
}

// sendAPK answers the old download paths (do_GET's ANDROID_APKS branch).
// Go-only: a 302 to the APK asset of the latest GitHub release, or to the
// release page when it cannot be resolved.  The server never serves an APK
// itself.
func (c *reqCtx) sendAPK(apk apkInfo) error {
	url, _, _ := c.app.appRelease.resolve(c.ctx).url("native")
	c.redirect(url, 302, nil)
	return nil
}

// remoteInfo is App.remote_info.
func (c *reqCtx) remoteInfo() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	header := c.header("Host")
	host, port := "127.0.0.1", 8890
	if header != "" {
		if h := urlHostname(header); h != "" {
			host = h
		}
		if n, ok, valid := urlPort(header); valid && ok && n != 0 {
			port = n
		}
	}
	urls := []string{}
	seen := map[string]bool{}
	if name, err := os.Hostname(); err == nil {
		if ips, err := net.DefaultResolver.LookupIP(c.ctx, "ip4", name); err == nil {
			for _, ip := range ips {
				s := ip.String()
				if s != "" && !strings.HasPrefix(s, "127.") && !seen[s] {
					seen[s] = true
					urls = append(urls, "http://"+s+":"+strconv.Itoa(port)+"/")
				}
			}
		}
	}
	roots := []any{}
	for _, r := range c.app.mediaRoots() {
		roots = append(roots, r)
	}
	requestURL := ""
	if header != "" {
		requestURL = "http://" + header + "/"
	}
	c.ok(pyjson.NewObject("project", p.Name(), "path", p.Path, "host", host, "port", int64(port),
		"urls", stringsToAny(urls), "media_roots", roots, "request_url", requestURL))
	return nil
}
