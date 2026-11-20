package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/mobile"
	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/nodehist"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/settings"
	"arkstudio/go/internal/store"
	"arkstudio/go/internal/workspace"
)

const maxBody = 24 * 1024 * 1024
const maxLoginBody = 8 * 1024

// reqCtx is one request: Handler instance state plus App._local.
type reqCtx struct {
	app     *App
	w       http.ResponseWriter
	r       *http.Request
	ctx     context.Context
	target  string // raw request target (self.path)
	rawPath string // urlparse(self.path).path
	path    string // unquote(rawPath)
	query   pyurl.Values

	session       *auth.Session
	user          string
	resolved      bool
	project       *project.Project
	token         *personalToken  // personal arka_ token bound to this request
	forcedProject string          // X-ARK-Project or the task session's project
	mobileSession *mobile.Session // paired device of this request
	bodyConsumed  bool            // the request body was read (_body_consumed)
	tabProject    string          // set by switch: the rest of the request follows it
	tabProjectSet bool
	task          *sessions.Session // bound read-only task-agent session

	// Set by the write phases of the router (router.go) for the handlers
	// of the later phases: the JSON body, the write actor and the project.
	body         *pyjson.Object
	writer       string
	routeProject *project.Project

	releaseAdmission func() // gives the admission slot back early (streams)
}

// detachFromRequestLimits lets a long-lived stream leave the admission pool;
// the stream is then bounded by its own deadline, the client and shutdown.
func (c *reqCtx) detachFromRequestLimits() {
	if c.releaseAdmission != nil {
		c.releaseAdmission()
	}
}

// ServeHTTP dispatches like BaseHTTPRequestHandler.do_<METHOD>.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Server", ServerHeader)
	ctx, cancel := context.WithTimeout(r.Context(), a.Cfg.RequestTimeout)
	defer cancel()
	if err := a.Admission.Acquire(ctx); err != nil {
		return // the client gave up while queued
	}
	released := false
	release := func() {
		if !released {
			released = true
			a.Admission.Release()
		}
	}
	defer release()
	target := r.RequestURI
	parsed := pyurl.Parse(target)
	c := &reqCtx{app: a, w: w, r: r, ctx: ctx, target: target, rawPath: parsed.Path,
		path: pyurl.Unquote(parsed.Path), query: pyurl.ParseQS(parsed.Query), releaseAdmission: release}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		// Agent writes move the agent's presence cursor (routes_presence.go, Go only).
		c.ctx = store.WithActivityObserver(c.ctx, c.observeActivity)
	}
	if replayClockOn {
		clock.SetReplay(nil)
		if raw := r.Header.Get("X-Replay-Clock"); raw != "" {
			if secs, err := strconv.ParseFloat(raw, 64); err == nil {
				t := time.Unix(0, int64(secs*1e9))
				clock.SetReplay(&t)
			}
		}
		if r.Header.Get("X-Replay-GpuTick") != "" {
			a.replayGPUTick()
		}
	}
	defer func() {
		if v := recover(); v != nil {
			if os.Getenv("ARK_DEBUG") != "" {
				fmt.Fprintln(os.Stderr, "panic:", v)
			}
			c.fail(500, fmt.Sprintf("Internal error: %v", v), "")
		}
	}()
	var err error
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		err = c.doGet()
	case http.MethodPost:
		err = c.doPost()
	case http.MethodPatch, http.MethodDelete:
		err = c.doPatchDelete()
	case http.MethodOptions:
		c.doOptions()
	default:
		c.unsupported()
	}
	if err != nil {
		c.handleError(err)
	}
}

// replayClockOn enables the X-Replay-Clock request header (differential
// replay only; see clock.SetReplay).
var replayClockOn = func() bool {
	on := os.Getenv("ARK_REPLAY_CLOCK") == "1"
	if on {
		nodehist.CommitTime = clock.Replay
	}
	return on
}()

// handleError is the except-chain of do_GET / do_POST.
func (c *reqCtx) handleError(err error) {
	var api *ApiError
	var se *store.Error
	var pe *project.Error
	var oe *org.Error
	var ve *pyerr.ValueError
	var ie *pyerr.InternalError
	var sett *settings.Error
	var we *workspace.Error
	switch {
	case errors.As(err, &api):
		if api.Status == 302 {
			c.redirect(api.Message, 302, nil)
		} else {
			c.fail(api.Status, api.Message, api.Code)
		}
	case errors.As(err, &se):
		c.fail(400, se.Message, "")
	case errors.As(err, &pe):
		c.fail(400, pe.Message, "")
	case errors.As(err, &oe):
		c.fail(oe.Status, oe.Message, oe.Code)
	case errors.As(err, &we):
		c.fail(we.Status, we.Message, "")
	case isSessionErr(err):
		se, _ := asSessionError(err)
		c.fail(se.Status, se.Message, se.Code)
	case isWorktreeError(err):
		wt, _ := asWorktreeError(err)
		c.fail(wt.Status, wt.Message, wt.Code)
	case errors.As(err, &ie):
		c.fail(500, "Internal error: "+ie.Message, "")
	case errors.As(err, &ve) && c.r.Method == http.MethodPost:
		c.fail(400, ve.Message, "")
	case errors.As(err, &sett) && c.r.Method == http.MethodPost:
		c.fail(400, sett.Message, "")
	case store.IsCanceled(err):
		c.fail(503, "Request timed out or was cancelled", "TIMEOUT")
	default:
		msg := err.Error()
		if errors.As(err, &ve) {
			msg = ve.Message
		}
		if c.r.Method == http.MethodPost && !errors.As(err, &ve) && strings.Contains(msg, "SQL") {
			c.fail(500, "Database error: "+msg, "")
			return
		}
		c.fail(500, "Internal error: "+msg, "")
	}
}

// unsupported is BaseHTTPRequestHandler.send_error(501) for unknown verbs.
func (c *reqCtx) unsupported() {
	body := "<!DOCTYPE HTML PUBLIC \"-//W3C//DTD HTML 4.01//EN\"\n        \"http://www.w3.org/TR/html4/strict.dtd\">\n" +
		"<html>\n    <head>\n        <meta http-equiv=\"Content-Type\" content=\"text/html;charset=utf-8\">\n" +
		"        <title>Error response</title>\n    </head>\n    <body>\n        <h1>Error response</h1>\n" +
		"        <p>Error code: 501</p>\n        <p>Message: Unsupported method ('" + htmlEscape(c.r.Method) + "').</p>\n" +
		"        <p>Error code explanation: HTTPStatus.NOT_IMPLEMENTED - Server does not support this operation.</p>\n" +
		"    </body>\n</html>\n"
	h := c.w.Header()
	h.Set("Connection", "close")
	h.Set("Content-Type", "text/html;charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	c.w.WriteHeader(501)
	io.WriteString(c.w, body) //nolint:errcheck
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;").Replace(s)
}

// doOptions is do_OPTIONS (only the local-open bridge, not ported).
func (c *reqCtx) doOptions() {
	if c.rawPath == "/api/local/open" {
		c.localOpenOptions()
		return
	}
	c.sendJSON(404, pyjson.NewObject("ok", false, "error", pyjson.NewObject("status", int64(404), "message", "Unknown endpoint")), nil)
}

// notPorted answers a contract route the Go server does not implement yet.
func (c *reqCtx) notPorted() {
	c.fail(501, "Not ported to the Go server yet: "+c.r.Method+" "+c.path+
		" (docs/development/go-port/README.md); run the Python server for this feature.", "NOT_PORTED")
}

// checkHost is Handler._check_host.
func (c *reqCtx) checkHost() error {
	if !c.app.hostAllowed(c.r.Host) {
		return apiErr(403, "Unexpected Host header", "HOST_REJECTED")
	}
	return nil
}

// header is self.headers.get(name): http.server decodes header bytes as
// ISO-8859-1, so UTF-8 text arrives as one character per byte.
func (c *reqCtx) header(name string) string {
	if strings.EqualFold(name, "Host") {
		return latin1(c.r.Host) // net/http moves Host out of r.Header
	}
	return latin1(c.r.Header.Get(name))
}

func latin1(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	r := make([]rune, len(s))
	for i := 0; i < len(s); i++ {
		r[i] = rune(s[i])
	}
	return string(r)
}

// clientIP is Handler._client_ip.
func (c *reqCtx) clientIP() string {
	if c.app.Cfg.TrustProxy {
		fwd := pytext.Strip(strings.Split(c.header("X-Forwarded-For"), ",")[0])
		if fwd != "" {
			if ip := net.ParseIP(strings.Split(fwd, "%")[0]); ip != nil {
				return ip.String()
			}
		}
	}
	return splitHostPort(c.r.RemoteAddr)
}

// trustedLocal is Handler._trusted_local.
func (c *reqCtx) trustedLocal() bool {
	remote := c.clientIP()
	loop := false
	if ip := net.ParseIP(strings.Split(remote, "%")[0]); ip != nil {
		loop = ip.IsLoopback()
	} else {
		loop = IsLoopbackHost(remote)
	}
	return loop && IsLoopbackHost(urlHostname(c.r.Host))
}

// requestScheme is Handler._request_scheme.
func (c *reqCtx) requestScheme() string {
	if c.app.Cfg.TrustProxy {
		if strings.ToLower(pytext.Strip(strings.Split(c.header("X-Forwarded-Proto"), ",")[0])) == "https" {
			return "https"
		}
		if strings.ToLower(pytext.Strip(c.header("X-Forwarded-Ssl"))) == "on" {
			return "https"
		}
	}
	if strings.HasPrefix(c.app.publicURL, "https://") {
		return "https"
	}
	return "http"
}

func (c *reqCtx) secure() bool { return c.requestScheme() == "https" }

// externalPrefix is Handler._external_prefix.
func (c *reqCtx) externalPrefix() string {
	if c.app.publicURL != "" {
		p := cookiePathFromURL(c.app.publicURL)
		if strings.HasPrefix(p, "/") {
			return p
		}
		return "/"
	}
	if c.app.Cfg.TrustProxy {
		if raw := pytext.Strip(c.header("X-Forwarded-Prefix")); raw != "" {
			return auth.NormalizeCookiePath(raw)
		}
	}
	return "/"
}

// externalPath is Handler._external_path.
func (c *reqCtx) externalPath() string {
	prefix := c.externalPrefix()
	path := c.target
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if prefix == "/" {
		return path
	}
	return strings.TrimRight(prefix, "/") + path
}

// sameOrigin is Handler._same_origin.
func (c *reqCtx) sameOrigin() bool {
	host := strings.ToLower(pytext.Strip(c.r.Host))
	origin := c.r.Header.Values("Origin")
	site := strings.ToLower(pytext.Strip(c.header("Sec-Fetch-Site")))
	referer := c.header("Referer")
	hasOrigin := len(origin) > 0 && origin[0] != ""
	if hasOrigin {
		o := origin[0]
		if strings.ToLower(pytext.Strip(o)) == "null" {
			if site != "same-origin" {
				return false
			}
		} else {
			p := pyurl.Split(o)
			if p.Scheme != "http" && p.Scheme != "https" {
				return false
			}
			if strings.Contains(p.Netloc, "@") {
				return false
			}
			originHost := urlHostname(p.Netloc)
			allowed := map[string]bool{urlHostname(host): true}
			if c.app.publicURL != "" {
				allowed[urlHostname(pyurl.Split(c.app.publicURL).Netloc)] = true
			}
			if c.app.Cfg.TrustProxy {
				if fwd := pytext.Strip(strings.Split(c.header("X-Forwarded-Host"), ",")[0]); fwd != "" {
					allowed[urlHostname(fwd)] = true
				}
			}
			if originHost == "" || !allowed[originHost] {
				return false
			}
		}
	}
	if site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if referer != "" && !hasOrigin {
		if urlHostname(pyurl.Split(referer).Netloc) != urlHostname(host) {
			return false
		}
	}
	return true
}

// actor is App.actor() for this request.
func (c *reqCtx) actor() string {
	if c.user != "" {
		return c.user
	}
	if c.app.Auth != nil {
		if c.session != nil {
			return c.session.Username
		}
		return ""
	}
	return org.LocalUsername
}

// agentToken is Handler._agent_token.
func (c *reqCtx) agentToken() string {
	h := c.header("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return pytext.Strip(h[7:])
	}
	return c.header("X-ARK-Token")
}

// writeActor is Handler._actor for writes.
func (c *reqCtx) writeActor() string {
	name := pytext.Strip(c.header("X-Agent-Name"))
	if c.token != nil {
		out := "agent:" + c.actor()
		if name != "" {
			out += "/" + pytext.Prefix(name, 40)
		}
		return out
	}
	if name != "" {
		return "agent:" + pytext.Prefix(name, 60)
	}
	if c.agentToken() != "" {
		return "agent:api"
	}
	// A user view plugin acting through the browser host with the user's
	// session (D-0003; web/js/plugin-host.js): still the user ("human:"),
	// named after the plugin in activity and node history.  Go only.
	if id := c.header("X-ARK-Plugin"); pluginIDRe.MatchString(id) {
		return "human:plugin:" + id
	}
	return "human:ui"
}

// pluginIDRe is a view plugin id (web/js/view-plugins.js PLUGIN_ID_RE).
var pluginIDRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,28}[a-z0-9]$`)

// readJSON is Handler._read_json.
func (c *reqCtx) readJSON() (*pyjson.Object, error) {
	length, err := contentLength(c.header("Content-Length"))
	if err != nil {
		return nil, &pyerr.ValueError{Message: err.Error()}
	}
	if length > maxBody {
		return nil, apiErr(413, "Request body is too large", "")
	}
	raw := []byte("{}")
	c.bodyConsumed = true
	if length > 0 {
		raw = make([]byte, length)
		if _, err := io.ReadFull(c.r.Body, raw); err != nil {
			return nil, apiErr(400, "Request body must be JSON", "")
		}
	}
	text := pyurl.DecodeStrict(raw)
	if text == nil {
		return nil, apiErr(400, "Request body must be JSON", "")
	}
	if *text == "" {
		*text = "{}"
	}
	v, err := pyjson.Loads(*text)
	if err != nil {
		return nil, apiErr(400, "Request body must be JSON", "")
	}
	o, ok := v.(*pyjson.Object)
	if !ok {
		return nil, apiErr(400, "Request body must be a JSON object", "")
	}
	return o, nil
}

// contentLength is int(self.headers.get("Content-Length") or 0).
func contentLength(h string) (int64, error) {
	if h == "" {
		return 0, nil
	}
	n, err := pytext.Int(h)
	if err != nil {
		return 0, err
	}
	return n, nil
}
