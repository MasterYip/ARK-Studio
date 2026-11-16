package sessionwatch

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/pyurl"
)

// This file reproduces what urllib.request.urlopen (CPython 3.10) puts on
// the wire and which exception it raises, so the Go watcher's requests and
// log lines match session_watch.py's.  Raw sockets are used instead of
// net/http because header order/case, "Connection: close", reason phrases
// and the OSError/URLError split are all observable.

// UserAgent is urllib's default "Python-urllib/<major>.<minor>".
var UserAgent = "Python-urllib/3.10"

const (
	socketTimeout = 60 * time.Second
	maxLine       = 65536
	maxHeaders    = 100
)

// UploadError mirrors session_watch.UploadError.
type UploadError struct {
	Status  int
	Code    string
	Message string
}

func (e *UploadError) Error() string { return e.Message }

// osError is any OSError subclass (URLError, socket.timeout, ...): watch()
// logs it and retries.  The text is str(exc).
type osError struct{ text string }

func (e *osError) Error() string { return e.text }

func urlError(reason string) *osError { return &osError{"<urlopen error " + reason + ">"} }

// crash is an exception session_watch.py does not catch (the process dies
// with a traceback and exit status 1).
type crash struct{ typ, msg string }

func throw(typ, msg string) { panic(crash{typ, msg}) }

type interrupted struct{}

// httpError is urllib.error.HTTPError (code, reason, unread body).
type httpError struct {
	code   int
	reason string
	resp   *pyResponse
}

func (e *httpError) Error() string { return "HTTP Error " + strconv.Itoa(e.code) + ": " + e.reason }

type hdr struct{ k, v string }

type hdrs []hdr

func (h hdrs) index(k string) int {
	for i, x := range h {
		if x.k == k {
			return i
		}
	}
	return -1
}

func (h *hdrs) set(k, v string) {
	if i := h.index(k); i >= 0 {
		(*h)[i].v = v
		return
	}
	*h = append(*h, hdr{k, v})
}

// get is email.message.Message.get (case-insensitive, first match).
func (h hdrs) get(k string) (string, bool) {
	k = strings.ToLower(k)
	for _, x := range h {
		if strings.ToLower(x.k) == k {
			return x.v, true
		}
	}
	return "", false
}

// capitalize is str.capitalize for header names.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}

// title is str.title for header names.
func title(s string) string {
	b := []byte(s)
	prev := false
	for i, c := range b {
		letter := (c|0x20) >= 'a' && (c|0x20) <= 'z'
		if letter {
			if prev {
				b[i] = c | 0x20
			} else {
				b[i] = c &^ 0x20
			}
		}
		prev = letter
	}
	return string(b)
}

// ------------------------------------------------------------ Request

type pyRequest struct {
	fullURL, fragment string
	typ, host         string
	selector          string
	method            string
	data              []byte
	headers           hdrs // Request.headers
	unredirected      hdrs // Request.unredirected_hdrs
	tunnelHost        string
	redirectDict      map[string]int
}

func (r *pyRequest) getFullURL() string {
	if r.fragment != "" {
		return r.fullURL + "#" + r.fragment
	}
	return r.fullURL
}

func (r *pyRequest) hasHeader(k string) bool {
	return r.headers.index(k) >= 0 || r.unredirected.index(k) >= 0
}

// newRequest is urllib.request.Request(url, data, headers, method=...).
func newRequest(rawURL string, data []byte, headers hdrs, method string) *pyRequest {
	r := &pyRequest{method: method, data: data}
	u := unwrap(rawURL)
	if i := strings.LastIndex(u, "#"); i >= 0 {
		u, r.fragment = u[:i], u[i+1:]
	}
	r.fullURL = u
	typ, rest, ok := splitType(u)
	if !ok {
		throw("ValueError", "unknown url type: "+pyjson.Repr(r.getFullURL()))
	}
	r.typ = typ
	r.host, r.selector = splitHost(rest)
	if r.host != "" {
		r.host = pyurl.Unquote(r.host)
	}
	for _, h := range headers {
		r.headers.set(capitalize(h.k), h.v)
	}
	return r
}

func unwrap(u string) string {
	u = pytext.Strip(u)
	if strings.HasPrefix(u, "<") && strings.HasSuffix(u, ">") && len(u) >= 2 {
		u = pytext.Strip(u[1 : len(u)-1])
	}
	if strings.HasPrefix(u, "URL:") {
		u = pytext.Strip(u[4:])
	}
	return u
}

// splitType is urllib.parse._splittype: '([^/:]+):(.*)'.
func splitType(u string) (string, string, bool) {
	i := strings.IndexAny(u, "/:")
	if i <= 0 || u[i] != ':' {
		return "", u, false
	}
	return pytext.Lower(u[:i]), u[i+1:], true
}

// splitHost is urllib.parse._splithost: '//([^/#?]*)(.*)'.
func splitHost(rest string) (host, selector string) {
	if !strings.HasPrefix(rest, "//") {
		return "", rest
	}
	s := rest[2:]
	i := strings.IndexAny(s, "/#?")
	if i < 0 {
		return s, ""
	}
	host, path := s[:i], s[i:]
	if path != "" && path[0] != '/' {
		path = "/" + path
	}
	return host, path
}

// ------------------------------------------------------------ proxies

// envProxies is urllib.request.getproxies_environment.
func envProxies() map[string]string {
	proxies := map[string]string{}
	env := os.Environ()
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if name == "" { // Windows "=C:" entries
			continue
		}
		name = strings.ToLower(name)
		if value != "" && strings.HasSuffix(name, "_proxy") {
			proxies[name[:len(name)-6]] = value
		}
	}
	if _, ok := os.LookupEnv("REQUEST_METHOD"); ok {
		delete(proxies, "http")
	}
	if !onWindows { // os.environ keys are upper-cased on Windows
		for _, kv := range env {
			name, value, _ := strings.Cut(kv, "=")
			if strings.HasSuffix(name, "_proxy") {
				name = strings.ToLower(name)
				if value != "" {
					proxies[name[:len(name)-6]] = value
				} else {
					delete(proxies, name[:len(name)-6])
				}
			}
		}
	}
	return proxies
}

func splitPort(host string) (string, string) {
	i := strings.LastIndex(host, ":")
	if i >= 0 {
		port := host[i+1:]
		if strings.Trim(port, "0123456789") == "" && port != "" {
			return host[:i], port
		}
		if port == "" {
			return host[:i], ""
		}
	}
	return host, ""
}

// proxyBypass is proxy_bypass_environment.
func proxyBypass(host string, proxies map[string]string) bool {
	noProxy, ok := proxies["no"]
	if !ok {
		return false
	}
	if noProxy == "*" {
		return true
	}
	host = pytext.Lower(host)
	hostonly, _ := splitPort(host)
	for _, name := range strings.Split(noProxy, ",") {
		name = pytext.Strip(name)
		if name == "" {
			continue
		}
		name = pytext.Lower(strings.TrimLeft(name, "."))
		if hostonly == name || host == name {
			return true
		}
		name = "." + name
		if strings.HasSuffix(hostonly, name) || strings.HasSuffix(host, name) {
			return true
		}
	}
	return false
}

// parseProxy is urllib.request._parse_proxy.
func parseProxy(proxy string) (scheme, user, password string, hasCreds bool, hostport string) {
	s, rest, ok := splitType(proxy)
	authority := proxy
	if ok && strings.HasPrefix(rest, "/") {
		if !strings.HasPrefix(rest, "//") {
			throw("ValueError", "proxy URL with no authority: "+pyjson.Repr(proxy))
		}
		scheme = s
		end := strings.Index(rest[2:], "/")
		if end < 0 {
			authority = rest[2:]
		} else {
			authority = rest[2 : 2+end]
		}
	}
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		userinfo := authority[:i]
		hostport = authority[i+1:]
		u, p, hasP := strings.Cut(userinfo, ":")
		return scheme, u, p, hasP && u != "" && p != "", hostport
	}
	return scheme, "", "", false, authority
}

// ------------------------------------------------------------ client

type client struct {
	ctx                    context.Context
	server, token, project string
}

func newClient(ctx context.Context, server, token, project string) *client {
	return &client{ctx: ctx, server: strings.TrimRight(server, "/"), token: token, project: project}
}

func (c *client) status(sessionID string) (any, error) {
	return c.request("GET", "/api/agent/v1/sessions/"+sessionID, nil)
}

func (c *client) upload(sessionID string, body *pyjson.Object) (any, error) {
	return c.request("POST", "/api/agent/v1/sessions/"+sessionID+"/upload", body)
}

// request is Client._request.
func (c *client) request(method, path string, body *pyjson.Object) (any, error) {
	headers := hdrs{{"Authorization", "Bearer " + c.token}, {"Accept", "application/json"},
		{"X-Agent-Name", "session-watch"}}
	if c.project != "" {
		headers = append(headers, hdr{"X-ARK-Project", c.project})
	}
	var data []byte
	if body != nil {
		data = pyjson.DumpsASCII(body)
		headers = append(headers, hdr{"Content-Type", "application/json"})
	}
	req := newRequest(c.server+path, data, headers, method)
	resp, err := c.open(req)
	var he *httpError
	if errors.As(err, &he) {
		raw, rerr := he.resp.read()
		if rerr != nil {
			return nil, rerr
		}
		var payload any = pyjson.NewObject()
		if text := pyurl.DecodeStrict(raw); text != nil {
			s := *text
			if s == "" {
				s = "{}"
			}
			if v, jerr := pyjson.Loads(s); jerr == nil {
				payload = v
			}
		}
		obj := asDict(payload)
		errv := obj.Value("error")
		if !pyjson.Truthy(errv) {
			errv = pyjson.NewObject()
		}
		eobj := asDict(errv)
		code := ""
		if v := eobj.Value("code"); pyjson.Truthy(v) {
			code = pyjson.Str(v)
		}
		msg := he.reason
		if v := eobj.Value("message"); pyjson.Truthy(v) {
			msg = pyjson.Str(v)
		}
		return nil, &UploadError{he.code, code, msg}
	}
	if err != nil {
		return nil, err
	}
	raw, rerr := resp.read()
	if rerr != nil {
		return nil, rerr
	}
	text := pyurl.DecodeStrict(raw)
	if text == nil {
		throw("UnicodeDecodeError", "'utf-8' codec can't decode response body")
	}
	s := *text
	if s == "" {
		s = "{}"
	}
	v, jerr := pyjson.Loads(s)
	if jerr != nil {
		throw("json.decoder.JSONDecodeError", "invalid JSON in response: "+pyjson.Repr(s))
	}
	return v, nil
}

// asDict returns v as a dict or dies like `v.get(...)` on a non-dict.
func asDict(v any) *pyjson.Object {
	if o, ok := v.(*pyjson.Object); ok {
		return o
	}
	throw("AttributeError", "'"+pyTypeName(v)+"' object has no attribute 'get'")
	return nil
}

func pyTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case float64:
		return "float"
	case []any:
		return "list"
	case *pyjson.Object:
		return "dict"
	}
	return "int"
}

// open is OpenerDirector.open with the default handler chain.
func (c *client) open(req *pyRequest) (*pyResponse, error) {
	if req.typ != "http" && req.typ != "https" {
		return nil, urlError("unknown url type: " + req.typ)
	}
	// AbstractHTTPHandler.do_request_
	if req.host == "" {
		return nil, urlError("no host given")
	}
	if req.data != nil {
		if !req.hasHeader("Content-type") {
			req.unredirected.set("Content-type", "application/x-www-form-urlencoded")
		}
		if !req.hasHeader("Content-length") && !req.hasHeader("Transfer-encoding") {
			req.unredirected.set("Content-length", strconv.Itoa(len(req.data)))
		}
	}
	if !req.hasHeader("Host") {
		req.unredirected.set("Host", req.host)
	}
	if !req.hasHeader("User-agent") {
		req.unredirected.set("User-agent", UserAgent)
	}
	// ProxyHandler.<type>_open
	useTLS := req.typ == "https"
	proxies := envProxies()
	if proxy, ok := proxies[req.typ]; ok && !proxyBypass(req.host, proxies) {
		ptype, user, password, creds, hostport := parseProxy(proxy)
		if ptype == "" {
			ptype = req.typ
		}
		if creds {
			userPass := pyurl.Unquote(user) + ":" + pyurl.Unquote(password)
			req.headers.set("Proxy-authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(userPass)))
		}
		hostport = pyurl.Unquote(hostport)
		if req.typ == "https" && req.tunnelHost == "" {
			req.tunnelHost = req.host
		} else {
			req.typ = ptype
			req.selector = req.getFullURL()
			useTLS = ptype == "https"
		}
		req.host = hostport
	}
	resp, err := c.doOpen(req, useTLS)
	if err != nil {
		return nil, err
	}
	if resp.status >= 200 && resp.status < 300 {
		return resp, nil
	}
	switch resp.status {
	case 301, 302, 303, 307, 308:
		return c.redirect(req, resp)
	}
	return nil, &httpError{resp.status, resp.reason, resp}
}

const infMsg = "The HTTP server returned a redirect error that would lead to an infinite loop.\n" +
	"The last 30x error message was:\n"

// redirect is HTTPRedirectHandler.http_error_302.
func (c *client) redirect(req *pyRequest, resp *pyResponse) (*pyResponse, error) {
	code, msg := resp.status, resp.reason
	newurl, ok := resp.headers.get("location")
	if !ok {
		if newurl, ok = resp.headers.get("uri"); !ok {
			return nil, &httpError{code, msg, resp}
		}
	}
	scheme, netloc, path := urlparseParts(newurl)
	if scheme != "http" && scheme != "https" && scheme != "ftp" && scheme != "" {
		return nil, &httpError{code, msg + " - Redirection to url '" + newurl + "' is not allowed", resp}
	}
	if path == "" && netloc != "" {
		newurl = withRootPath(newurl, scheme, netloc)
	}
	newurl = quoteLatin1(newurl)
	newurl = urljoin(req.getFullURL(), newurl)
	m := req.method
	if !((m == "GET" || m == "HEAD") || ((code == 301 || code == 302 || code == 303) && m == "POST")) {
		return nil, &httpError{code, msg, resp}
	}
	var newHeaders hdrs
	for _, h := range req.headers {
		if l := strings.ToLower(h.k); l != "content-length" && l != "content-type" {
			newHeaders = append(newHeaders, h)
		}
	}
	method := "GET"
	if m == "HEAD" {
		method = "HEAD"
	}
	next := newRequest(strings.ReplaceAll(newurl, " ", "%20"), nil, newHeaders, method)
	if req.redirectDict != nil {
		if req.redirectDict[newurl] >= 4 || len(req.redirectDict) >= 10 {
			return nil, &httpError{code, infMsg + msg, resp}
		}
		next.redirectDict = req.redirectDict
	} else {
		req.redirectDict = map[string]int{}
		next.redirectDict = req.redirectDict
	}
	next.redirectDict[newurl]++
	if _, err := resp.read(); err != nil {
		return nil, err
	}
	return c.open(next)
}

func urlparseParts(u string) (scheme, netloc, path string) {
	rest := u
	if i := strings.Index(u, ":"); i > 0 {
		valid := true
		for j, r := range u[:i] {
			alpha := (r|0x20) >= 'a' && (r|0x20) <= 'z'
			if !(alpha || (j > 0 && (r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.'))) {
				valid = false
				break
			}
		}
		if valid {
			scheme, rest = strings.ToLower(u[:i]), u[i+1:]
		}
	}
	if strings.HasPrefix(rest, "//") {
		s := rest[2:]
		i := strings.IndexAny(s, "/?#")
		if i < 0 {
			return scheme, s, ""
		}
		netloc, rest = s[:i], s[i:]
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	return scheme, netloc, rest
}

func withRootPath(u, scheme, netloc string) string {
	prefix := ""
	if scheme != "" {
		prefix = scheme + ":"
	}
	prefix += "//" + netloc
	i := strings.Index(u, "//"+netloc) + 2 + len(netloc)
	return prefix + "/" + u[i:]
}

// quoteLatin1 is quote(url, encoding="iso-8859-1", safe=string.punctuation).
func quoteLatin1(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 && r > 0x20 && r != 0x7f {
			b.WriteRune(r)
			continue
		}
		if r > 0xff {
			throw("UnicodeEncodeError", "'latin-1' codec can't encode character")
		}
		b.WriteByte('%')
		b.WriteByte("0123456789ABCDEF"[r>>4])
		b.WriteByte("0123456789ABCDEF"[r&15])
	}
	return b.String()
}

func urljoin(base, ref string) string {
	bu, err1 := url.Parse(base)
	ru, err2 := url.Parse(ref)
	if err1 != nil || err2 != nil {
		return ref
	}
	return bu.ResolveReference(ru).String()
}

// ------------------------------------------------------------ connection

type timeoutConn struct{ net.Conn }

func (c timeoutConn) Read(b []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(socketTimeout))
	return c.Conn.Read(b)
}

func (c timeoutConn) Write(b []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(socketTimeout))
	return c.Conn.Write(b)
}

// sockText is str(exc) for a socket-level failure.
func (c *client) sockText(err error) string {
	if c.ctx.Err() != nil {
		panic(interrupted{})
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timed out"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErrorText(dnsErr.IsNotFound)
	}
	if s, ok := socketErrorText(err); ok {
		return s
	}
	var uerr x509.UnknownAuthorityError
	var herr x509.HostnameError
	var cerr x509.CertificateInvalidError
	switch {
	case errors.As(err, &uerr):
		return "[SSL: CERTIFICATE_VERIFY_FAILED] certificate verify failed: unable to get local issuer certificate (_ssl.c:997)"
	case errors.As(err, &herr):
		return "[SSL: CERTIFICATE_VERIFY_FAILED] certificate verify failed: Hostname mismatch, certificate is not valid for '" + herr.Host + "'. (_ssl.c:997)"
	case errors.As(err, &cerr) && cerr.Reason == x509.Expired:
		return "[SSL: CERTIFICATE_VERIFY_FAILED] certificate verify failed: certificate has expired (_ssl.c:997)"
	case errors.As(err, new(tls.RecordHeaderError)):
		return "[SSL: WRONG_VERSION_NUMBER] wrong version number (_ssl.c:997)"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "EOF occurred in violation of protocol (_ssl.c:997)"
	}
	return err.Error()
}

// getHostPort is HTTPConnection._get_hostport (+ _validate_host).
func getHostPort(host string, defaultPort int) (string, int) {
	port := defaultPort
	i := strings.LastIndex(host, ":")
	j := strings.LastIndex(host, "]")
	if i > j {
		p := host[i+1:]
		if n, err := pytext.Int(p); err == nil {
			port = int(n)
		} else if p != "" {
			throw("http.client.InvalidURL", "nonnumeric port: '"+p+"'")
		}
		host = host[:i]
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") && len(host) >= 2 {
		host = host[1 : len(host)-1]
	}
	for _, r := range host {
		if r <= 0x20 || r == 0x7f {
			throw("http.client.InvalidURL", "URL can't contain control characters. "+pyjson.Repr(host))
		}
	}
	if port < 0 || port > 65535 {
		throw("OverflowError", "getsockaddrarg: port must be 0-65535.")
	}
	return host, port
}

// dial is socket.create_connection: every address in resolver order, the
// last error wins.
func (c *client) dial(host string, port int) (net.Conn, error) {
	var addrs []string
	if ip := net.ParseIP(host); ip != nil {
		addrs = []string{ip.String()}
	} else {
		ips, err := net.DefaultResolver.LookupIPAddr(c.ctx, host)
		if err != nil {
			return nil, &osError{c.sockText(err)}
		}
		for _, ip := range ips {
			addrs = append(addrs, ip.String())
		}
	}
	var last error
	for _, a := range addrs {
		d := net.Dialer{Timeout: socketTimeout}
		conn, err := d.DialContext(c.ctx, "tcp", net.JoinHostPort(a, strconv.Itoa(port)))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last == nil {
		return nil, &osError{"getaddrinfo returns an empty list"}
	}
	return nil, &osError{c.sockText(last)}
}

func latin1(s string, what string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xff {
			throw("UnicodeEncodeError", "'latin-1' codec can't encode character in "+what)
		}
		out = append(out, byte(r))
	}
	return out
}

func illegalHeaderValue(v string) bool {
	for i := 0; i < len(v); i++ {
		next := byte(0)
		if i+1 < len(v) {
			next = v[i+1]
		}
		if v[i] == '\n' && next != ' ' && next != '\t' {
			return true
		}
		if v[i] == '\r' && next != ' ' && next != '\t' && next != '\n' {
			return true
		}
	}
	return false
}

// doOpen is AbstractHTTPHandler.do_open + HTTPConnection.request/getresponse.
func (c *client) doOpen(req *pyRequest, useTLS bool) (*pyResponse, error) {
	defaultPort := 80
	if useTLS {
		defaultPort = 443
	}
	host, port := getHostPort(req.host, defaultPort)

	headers := append(hdrs(nil), req.unredirected...)
	for _, h := range req.headers {
		if headers.index(h.k) < 0 {
			headers = append(headers, h)
		}
	}
	headers.set("Connection", "close")
	var titled hdrs
	for _, h := range headers {
		titled.set(title(h.k), h.v)
	}
	var tunnelHeaders hdrs
	if req.tunnelHost != "" {
		if i := titled.index("Proxy-Authorization"); i >= 0 {
			tunnelHeaders = hdrs{titled[i]}
			titled = append(titled[:i:i], titled[i+1:]...)
		}
	}

	// putrequest / putheader validation happens before connecting.
	selector := req.selector
	if selector == "" {
		selector = "/"
	}
	for _, r := range selector {
		if r <= 0x20 || r == 0x7f {
			throw("http.client.InvalidURL", "URL can't contain control characters. "+pyjson.Repr(selector))
		}
		if r >= 0x80 {
			throw("UnicodeEncodeError", "'ascii' codec can't encode characters in request line")
		}
	}
	names := map[string]bool{}
	for _, h := range titled {
		names[strings.ToLower(h.k)] = true
	}
	var head []byte
	head = append(head, req.method+" "+selector+" HTTP/1.1\r\n"...)
	if !names["accept-encoding"] {
		head = append(head, "Accept-Encoding: identity\r\n"...)
	}
	if req.data == nil && !names["content-length"] && (req.method == "POST" || req.method == "PUT" || req.method == "PATCH") {
		head = append(head, "Content-Length: 0\r\n"...)
	}
	for _, h := range titled {
		if illegalHeaderValue(h.v) {
			throw("ValueError", "Invalid header value "+pyjson.Repr(h.v))
		}
		head = append(head, h.k+": "...)
		head = append(head, latin1(h.v, "header "+h.k)...)
		head = append(head, "\r\n"...)
	}
	head = append(head, "\r\n"...)

	// connect + send: OSError here becomes URLError.
	raw, err := c.dial(host, port)
	if err != nil {
		return nil, urlError(err.Error())
	}
	stop := context.AfterFunc(c.ctx, func() { raw.Close() })
	defer stop()
	defer raw.Close()
	var conn net.Conn = timeoutConn{raw}
	if req.tunnelHost != "" {
		if err := c.tunnel(conn, req.tunnelHost, tunnelHeaders); err != nil {
			return nil, err
		}
	}
	if useTLS {
		sni := host
		if req.tunnelHost != "" {
			sni, _ = getHostPort(req.tunnelHost, 443)
		}
		tc := tls.Client(conn, &tls.Config{ServerName: sni, NextProtos: []string{"http/1.1"}})
		if err := tc.HandshakeContext(c.ctx); err != nil {
			return nil, urlError(c.sockText(err))
		}
		conn = tc
	}
	if _, err := conn.Write(head); err != nil {
		return nil, urlError(c.sockText(err))
	}
	if req.data != nil {
		if _, err := conn.Write(req.data); err != nil {
			return nil, urlError(c.sockText(err))
		}
	}

	// getresponse: errors are raised as-is (not wrapped).
	br := bufio.NewReader(conn)
	resp, err := c.readResponse(br, req.method)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// tunnel is HTTPConnection._tunnel.
func (c *client) tunnel(conn net.Conn, tunnelHost string, headers hdrs) error {
	h, p := getHostPort(tunnelHost, 443)
	var b []byte
	b = append(b, "CONNECT "+h+":"+strconv.Itoa(p)+" HTTP/1.0\r\n"...)
	for _, x := range headers {
		b = append(b, latin1(x.k+": "+x.v+"\r\n", "tunnel header")...)
	}
	b = append(b, "\r\n"...)
	if _, err := conn.Write(b); err != nil {
		return urlError(c.sockText(err))
	}
	br := bufio.NewReader(conn)
	_, code, reason, err := c.readStatus(br)
	if err != nil {
		if oe, ok := err.(*osError); ok {
			return urlError(oe.text)
		}
		return err
	}
	if code != 200 {
		return urlError("Tunnel connection failed: " + strconv.Itoa(code) + " " + pytext.Strip(reason))
	}
	for {
		line, err := c.readLine(br, "header line")
		if err != nil {
			return urlError(err.Error())
		}
		if line == "\r\n" || line == "\n" || line == "" {
			break
		}
	}
	if br.Buffered() > 0 {
		throw("RuntimeError", "proxy sent data after the tunnel response")
	}
	return nil
}

// ------------------------------------------------------------ response

type pyResponse struct {
	status  int
	reason  string
	headers hdrs
	body    []byte
	bodyErr error
	read1   bool
}

// read is HTTPResponse.read(): the body was buffered eagerly; errors surface
// at the point Python would have read it.
func (r *pyResponse) read() ([]byte, error) {
	if r.read1 {
		return nil, nil
	}
	r.read1 = true
	if r.bodyErr != nil {
		if cr, ok := r.bodyErr.(crashErr); ok {
			throw(cr.typ, cr.msg)
		}
		return nil, r.bodyErr
	}
	return r.body, nil
}

type crashErr crash

func (e crashErr) Error() string { return e.msg }

func (c *client) readLine(br *bufio.Reader, what string) (string, error) {
	var line []byte
	for {
		b, err := br.ReadByte()
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", &osError{c.sockText(err)}
		}
		line = append(line, b)
		if b == '\n' {
			break
		}
		if len(line) > maxLine {
			throw("http.client.LineTooLong", "got more than 65536 bytes when reading "+what)
		}
	}
	// iso-8859-1 decode
	rs := make([]rune, len(line))
	for i, b := range line {
		rs[i] = rune(b)
	}
	return string(rs), nil
}

func (c *client) readStatus(br *bufio.Reader) (version string, status int, reason string, err error) {
	line, err := c.readLine(br, "status line")
	if err != nil {
		return "", 0, "", err
	}
	if line == "" {
		return "", 0, "", &osError{"Remote end closed connection without response"}
	}
	fields := splitN3(line)
	if len(fields) < 2 {
		throw("http.client.BadStatusLine", pyjson.Repr(line))
	}
	version = fields[0]
	if len(fields) == 3 {
		reason = fields[2]
	}
	if !strings.HasPrefix(version, "HTTP/") {
		throw("http.client.BadStatusLine", pyjson.Repr(line))
	}
	n, ierr := pytext.Int(fields[1])
	if ierr != nil || n < 100 || n > 999 {
		throw("http.client.BadStatusLine", pyjson.Repr(line))
	}
	return version, int(n), reason, nil
}

// splitN3 is str.split(None, 2).
func splitN3(s string) []string {
	var out []string
	rs := []rune(s)
	i := 0
	for len(out) < 2 {
		for i < len(rs) && pytext.IsSpace(rs[i]) {
			i++
		}
		if i >= len(rs) {
			return out
		}
		j := i
		for j < len(rs) && !pytext.IsSpace(rs[j]) {
			j++
		}
		out = append(out, string(rs[i:j]))
		i = j
	}
	for i < len(rs) && pytext.IsSpace(rs[i]) {
		i++
	}
	if i < len(rs) {
		out = append(out, string(rs[i:]))
	}
	return out
}

func (c *client) readHeaders(br *bufio.Reader) (hdrs, error) {
	var lines []string
	for {
		line, err := c.readLine(br, "header line")
		if err != nil {
			return nil, err
		}
		if line == "\r\n" || line == "\n" || line == "" {
			break
		}
		lines = append(lines, line)
		if len(lines) > maxHeaders {
			throw("http.client.HTTPException", "got more than 100 headers")
		}
	}
	var h hdrs
	for _, line := range lines {
		if (line[0] == ' ' || line[0] == '\t') && len(h) > 0 {
			h[len(h)-1].v += line
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || name == "" {
			continue
		}
		h = append(h, hdr{name, strings.TrimLeft(value, " \t")})
	}
	for i := range h {
		h[i].v = strings.TrimRight(h[i].v, "\r\n")
	}
	return h, nil
}

func (c *client) readResponse(br *bufio.Reader, method string) (*pyResponse, error) {
	var version, reason string
	var status int
	var err error
	for {
		version, status, reason, err = c.readStatus(br)
		if err != nil {
			return nil, err
		}
		if status != 100 {
			break
		}
		if _, err := c.readHeaders(br); err != nil {
			return nil, err
		}
	}
	h, err := c.readHeaders(br)
	if err != nil {
		return nil, err
	}
	if !(version == "HTTP/1.0" || version == "HTTP/0.9" || strings.HasPrefix(version, "HTTP/1.")) {
		throw("http.client.UnknownProtocol", pyjson.Repr(version))
	}
	resp := &pyResponse{status: status, reason: pytext.Strip(reason), headers: h}
	chunked := false
	if te, ok := h.get("transfer-encoding"); ok && strings.ToLower(te) == "chunked" {
		chunked = true
	}
	length := int64(-1)
	if cl, ok := h.get("content-length"); ok && cl != "" && !chunked {
		if n, err := pytext.Int(cl); err == nil && n >= 0 {
			length = n
		}
	}
	if status == 204 || status == 304 || (status >= 100 && status < 200) || method == "HEAD" {
		length = 0
	}
	switch {
	case chunked:
		resp.body, resp.bodyErr = c.readChunked(br)
	case length >= 0:
		buf := make([]byte, length)
		n, err := io.ReadFull(br, buf)
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			resp.bodyErr = crashErr{"http.client.IncompleteRead", "IncompleteRead(" + strconv.Itoa(n) + " bytes read, " + strconv.FormatInt(length-int64(n), 10) + " more expected)"}
		} else if err != nil {
			resp.bodyErr = &osError{c.sockText(err)}
		}
		resp.body = buf[:n]
	default:
		b, err := io.ReadAll(br)
		if err != nil {
			resp.bodyErr = &osError{c.sockText(err)}
		}
		resp.body = b
	}
	return resp, nil
}

func (c *client) readChunked(br *bufio.Reader) ([]byte, error) {
	var out []byte
	incomplete := func() error {
		return crashErr{"http.client.IncompleteRead", "IncompleteRead(" + strconv.Itoa(len(out)) + " bytes read)"}
	}
	for {
		line, err := c.readLine(br, "chunk size")
		if err != nil {
			return out, err
		}
		if i := strings.Index(line, ";"); i >= 0 {
			line = line[:i]
		}
		size, ok := parseHex(line)
		if !ok {
			return out, incomplete()
		}
		if size == 0 {
			break
		}
		buf := make([]byte, size)
		n, err := io.ReadFull(br, buf)
		out = append(out, buf[:n]...)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return out, incomplete()
			}
			return out, &osError{c.sockText(err)}
		}
		crlf := make([]byte, 2)
		if _, err := io.ReadFull(br, crlf); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return out, incomplete()
			}
			return out, &osError{c.sockText(err)}
		}
	}
	for { // _read_and_discard_trailer
		line, err := c.readLine(br, "trailer line")
		if err != nil {
			return out, err
		}
		if line == "\r\n" || line == "\n" || line == "" {
			break
		}
	}
	return out, nil
}

// parseHex is int(line, 16).
func parseHex(s string) (int64, bool) {
	s = pytext.Strip(s)
	neg := false
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
		neg, s = s[0] == '-', s[1:]
	}
	if len(s) > 2 && (s[:2] == "0x" || s[:2] == "0X") {
		s = s[2:]
		if strings.HasPrefix(s, "_") {
			s = s[1:]
		}
	}
	if s == "" || strings.HasPrefix(s, "_") || strings.HasSuffix(s, "_") || strings.Contains(s, "__") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(s, "_", ""), 16, 64)
	if err != nil || neg {
		return 0, false
	}
	return n, true
}
