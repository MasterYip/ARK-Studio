package server

// POST /api/open (App.open_external) and the loopback-only local-folder
// bridge POST/OPTIONS /api/local/open (Handler._handle_local_open).

import (
	"errors"
	"net"
	"os"
	"regexp"
	"strings"

	"arkstudio/go/internal/hostopen"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
)

var hostopenPorted = []PortedRoute{
	{"exact:/api/open", "POST"},
	{"exact:/api/local/open", "POST"},
}

func init() { Ported = append(Ported, hostopenPorted...) }

var httpURL = regexp.MustCompile(`(?i)^https?://`)

func platformErr(err error) error {
	var pe *hostopen.Error
	if errors.As(err, &pe) {
		return apiErr(pe.Status, pe.Message, "")
	}
	return err
}

// openExternal is POST /api/open.
func (c *reqCtx) openExternal(body *pyjson.Object) error {
	for _, candidate := range []string{c.readToken(), c.agentToken()} {
		if candidate != "" && c.app.Sessions != nil && c.app.Sessions.ForReadToken(candidate) != nil {
			return apiErr(403, "Task-agent read tokens cannot open resources on the host", "")
		}
	}
	p, err := c.require("admin")
	if err != nil {
		return err
	}
	field := func(name string) string { return pytext.Strip(pyjson.Str(orDefault(body.Value(name), ""))) }
	unitRef, sessionID, pathValue, urlValue := field("unit"), field("session"), field("path"), field("url")
	root := p.Path
	var result *pyjson.Object
	switch {
	case unitRef != "":
		unit, err := p.Store.GetUnit(c.ctx, unitRef)
		if err != nil {
			return err
		}
		var denied error
		target := ""
		media, _ := unit.Value("media").([]any)
		for _, item := range media {
			obj, _ := item.(*pyjson.Object)
			if obj == nil {
				continue
			}
			raw := pytext.Strip(pyjson.Str(orDefault(obj.Value("url"), "")))
			if raw == "" || httpURL.MatchString(raw) {
				continue
			}
			t, err := hostopen.ResolveMediaPath(root, raw)
			if err != nil {
				if denied == nil {
					denied = err
				}
				continue
			}
			target = t
			break
		}
		if target == "" {
			if denied != nil {
				return platformErr(denied)
			}
			ref := unitRef
			if uid := pyjson.Str(orDefault(unit.Value("uid"), "")); uid != "" {
				ref = uid
			}
			return apiErr(400, ref+" has no local media file to open; add a local asset under the project first", "")
		}
		if result, err = hostopen.OpenExternal(target); err != nil {
			return platformErr(err)
		}
		result.Set("mode", "unit").Set("unit", unit.Value("uid"))
	case sessionID != "":
		if pathValue == "" {
			return apiErr(400, "session open requests need a workspace-relative path", "")
		}
		if c.app.Sessions == nil {
			return apiErr(404, "session not found or not accessible", "SESSION_NOT_ACCESSIBLE")
		}
		s, err := c.app.Sessions.Get(sessionID)
		if err != nil {
			if _, ok := asSessionError(err); ok {
				return apiErr(404, "session not found or not accessible", "SESSION_NOT_ACCESSIBLE")
			}
			return err
		}
		// Project admin on project A must not open project B's files.
		if c.app.Sessions.ProjectID(s) != p.ID() {
			return apiErr(404, "session not found or not accessible", "SESSION_NOT_ACCESSIBLE")
		}
		target, err := c.app.Sessions.ArtifactFile(sessionID, pathValue)
		if err != nil {
			return err // SessionError -> its status (do_POST)
		}
		if result, err = hostopen.OpenExternal(target); err != nil {
			return platformErr(err)
		}
		result.Set("mode", "session").Set("session", sessionID)
	case pathValue != "":
		var target string
		if strings.HasPrefix(strings.ToLower(pathValue), "file://") {
			raw, err := hostopen.PathFromFileURL(pathValue)
			if err != nil {
				return platformErr(err)
			}
			if target, err = hostopen.EnsureWithin(root, raw, true); err != nil {
				return platformErr(err)
			}
		} else if target, err = hostopen.SafePath(root, pathValue, true); err != nil {
			return platformErr(err)
		}
		if st, err := os.Stat(target); err != nil || !st.Mode().IsRegular() {
			return apiErr(400, "only files can be opened", "")
		}
		if result, err = hostopen.OpenExternal(target); err != nil {
			return platformErr(err)
		}
		result.Set("mode", "path")
	case urlValue != "":
		if httpURL.MatchString(urlValue) {
			result, err = hostopen.DefaultBrowserOpen(urlValue)
		} else {
			result, err = hostopen.OpenURL(urlValue)
		}
		if err != nil {
			return platformErr(err)
		}
		result.Set("mode", "url")
	default:
		return apiErr(400, "open requires one of: unit, session+path, path, url", "")
	}
	c.ok(result)
	return nil
}

// localOpenCORS is Handler._local_open_cors (nil = origin not allowed).
func (c *reqCtx) localOpenCORS() headerList {
	origin := strings.TrimRight(pytext.Strip(c.header("Origin")), "/")
	if origin == "" {
		return nil
	}
	for _, item := range strings.Split(os.Getenv("ARK_LOCAL_OPEN_ORIGINS"), ",") {
		if item = strings.TrimRight(pytext.Strip(item), "/"); item != "" && item == origin {
			return headerList{
				{"Access-Control-Allow-Origin", origin},
				{"Access-Control-Allow-Methods", "POST, OPTIONS"},
				{"Access-Control-Allow-Headers", "Content-Type"},
				{"Access-Control-Allow-Private-Network", "true"},
				{"Vary", "Origin"},
			}
		}
	}
	return nil
}

func localOpenFail(status int, message string) *pyjson.Object {
	return pyjson.NewObject("ok", false, "error", pyjson.NewObject("status", int64(status), "message", message))
}

// localOpenOptions is do_OPTIONS for /api/local/open.
func (c *reqCtx) localOpenOptions() {
	cors := c.localOpenCORS()
	if cors == nil {
		c.sendJSON(403, localOpenFail(403, "Local open origin is not allowed"), headerList{{"Vary", "Origin"}})
		return
	}
	c.send(204, nil, jsonType, cors, true)
}

// localOpen is Handler._handle_local_open.
func (c *reqCtx) localOpen() error {
	host, _, err := net.SplitHostPort(c.r.RemoteAddr)
	if err != nil {
		host = c.r.RemoteAddr
	}
	// ipaddress.ip_address(...).is_loopback (an IPv4-mapped IPv6 peer is not).
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() || (ip.To4() != nil && strings.Contains(host, ":")) {
		c.sendJSON(403, localOpenFail(403, "Local open is loopback-only"), nil)
		return nil
	}
	cors := c.localOpenCORS()
	if cors == nil {
		c.sendJSON(403, localOpenFail(403, "Local open origin is not allowed"), headerList{{"Vary", "Origin"}})
		return nil
	}
	body, err := c.readJSON()
	if err != nil {
		return err
	}
	raw := pytext.Strip(pyjson.Str(orDefault(body.Value("path"), "")))
	if raw == "" {
		c.sendJSON(400, localOpenFail(400, "path is required"), cors)
		return nil
	}
	target := hostopen.Resolve(raw)
	var roots []string
	for _, item := range strings.Split(os.Getenv("ARK_LOCAL_OPEN_ROOTS"), string(os.PathListSeparator)) {
		if item = strings.Trim(pytext.Strip(item), `"`); item != "" {
			roots = append(roots, hostopen.Resolve(item))
		}
	}
	if len(roots) == 0 {
		c.sendJSON(403, localOpenFail(403, "ARK_LOCAL_OPEN_ROOTS is not configured"), cors)
		return nil
	}
	inside := false
	for _, root := range roots {
		if within(target, root) {
			inside = true
			break
		}
	}
	if !inside {
		c.sendJSON(403, localOpenFail(403, "Path is outside ARK_LOCAL_OPEN_ROOTS"), cors)
		return nil
	}
	if _, err := os.Stat(target); err != nil {
		c.sendJSON(404, localOpenFail(404, "Path does not exist on this computer"), cors)
		return nil
	}
	result, err := hostopen.OpenExternal(target)
	if err != nil {
		var pe *hostopen.Error
		if errors.As(err, &pe) {
			c.sendJSON(pe.Status, localOpenFail(pe.Status, pe.Message), cors)
			return nil
		}
		return err
	}
	out := pyjson.NewObject("ok", true, "path", target, "mode", "local")
	out.Update(result)
	c.sendJSON(200, out, cors)
	return nil
}
