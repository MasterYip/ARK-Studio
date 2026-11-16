// Package hostopen is the HTTP side of opening things on the server's host:
// POST /api/open (App.open_external) and the loopback-only local-folder
// bridge POST/OPTIONS /api/local/open (Handler._handle_local_open), over
// internal/hostopen.  internal/server registers it.
package hostopen

import (
	"errors"
	"net"
	"os"
	"regexp"
	"strings"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/hostopen"
	"arkstudio/go/internal/platform/pathx"
	"arkstudio/go/internal/sessions"
)

// Handlers answers POST /api/open for one server.
type Handlers struct {
	Sessions *sessions.Manager // nil when agent sessions are off
}

func asSessionError(err error) (*sessions.Error, bool) {
	var se *sessions.Error
	ok := errors.As(err, &se)
	return se, ok
}

// orDefault is `value or default`.
func orDefault(v any, def any) any {
	if pyjson.Truthy(v) {
		return v
	}
	return def
}

var httpURL = regexp.MustCompile(`(?i)^https?://`)

func platformErr(err error) error {
	var pe *hostopen.Error
	if errors.As(err, &pe) {
		return api.Err(pe.Status, pe.Message, "")
	}
	return err
}

// Open is POST /api/open (App.open_external).
func (h *Handlers) Open(c api.Ctx, body *pyjson.Object) error {
	if c.TaskAgentReadTokenUsed() {
		return api.Err(403, "Task-agent read tokens cannot open resources on the host", "")
	}
	p, err := c.Require("admin")
	if err != nil {
		return err
	}
	field := func(name string) string { return pytext.Strip(pyjson.Str(orDefault(body.Value(name), ""))) }
	unitRef, sessionID, pathValue, urlValue := field("unit"), field("session"), field("path"), field("url")
	root := p.Path
	var result *pyjson.Object
	switch {
	case unitRef != "":
		unit, err := p.Store.GetUnit(c.Context(), unitRef)
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
			return api.Err(400, ref+" has no local media file to open; add a local asset under the project first", "")
		}
		if result, err = hostopen.OpenExternal(target); err != nil {
			return platformErr(err)
		}
		result.Set("mode", "unit").Set("unit", unit.Value("uid"))
	case sessionID != "":
		if pathValue == "" {
			return api.Err(400, "session open requests need a workspace-relative path", "")
		}
		if h.Sessions == nil {
			return api.Err(404, "session not found or not accessible", "SESSION_NOT_ACCESSIBLE")
		}
		s, err := h.Sessions.Get(sessionID)
		if err != nil {
			if _, ok := asSessionError(err); ok {
				return api.Err(404, "session not found or not accessible", "SESSION_NOT_ACCESSIBLE")
			}
			return err
		}
		// Project admin on project A must not open project B's files.
		if h.Sessions.ProjectID(s) != p.ID() {
			return api.Err(404, "session not found or not accessible", "SESSION_NOT_ACCESSIBLE")
		}
		target, err := h.Sessions.ArtifactFile(sessionID, pathValue)
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
			return api.Err(400, "only files can be opened", "")
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
		return api.Err(400, "open requires one of: unit, session+path, path, url", "")
	}
	c.OK(result)
	return nil
}

// localOpenCORS is Handler._local_open_cors (nil = origin not allowed).
func localOpenCORS(c api.Ctx) [][2]string {
	origin := strings.TrimRight(pytext.Strip(c.Header("Origin")), "/")
	if origin == "" {
		return nil
	}
	for _, item := range strings.Split(os.Getenv("ARK_LOCAL_OPEN_ORIGINS"), ",") {
		if item = strings.TrimRight(pytext.Strip(item), "/"); item != "" && item == origin {
			return [][2]string{
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

// LocalOpenOptions is do_OPTIONS for /api/local/open.
func LocalOpenOptions(c api.Ctx) {
	cors := localOpenCORS(c)
	if cors == nil {
		c.SendJSON(403, localOpenFail(403, "Local open origin is not allowed"), [][2]string{{"Vary", "Origin"}})
		return
	}
	c.Send(204, nil, api.JSONType, cors)
}

// LocalOpen is Handler._handle_local_open.
func LocalOpen(c api.Ctx) error {
	host, _, err := net.SplitHostPort(c.RemoteAddr())
	if err != nil {
		host = c.RemoteAddr()
	}
	// ipaddress.ip_address(...).is_loopback (an IPv4-mapped IPv6 peer is not).
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() || (ip.To4() != nil && strings.Contains(host, ":")) {
		c.SendJSON(403, localOpenFail(403, "Local open is loopback-only"), nil)
		return nil
	}
	cors := localOpenCORS(c)
	if cors == nil {
		c.SendJSON(403, localOpenFail(403, "Local open origin is not allowed"), [][2]string{{"Vary", "Origin"}})
		return nil
	}
	body, err := c.ReadJSON()
	if err != nil {
		return err
	}
	raw := pytext.Strip(pyjson.Str(orDefault(body.Value("path"), "")))
	if raw == "" {
		c.SendJSON(400, localOpenFail(400, "path is required"), cors)
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
		c.SendJSON(403, localOpenFail(403, "ARK_LOCAL_OPEN_ROOTS is not configured"), cors)
		return nil
	}
	inside := false
	for _, root := range roots {
		if pathx.Within(target, root) {
			inside = true
			break
		}
	}
	if !inside {
		c.SendJSON(403, localOpenFail(403, "Path is outside ARK_LOCAL_OPEN_ROOTS"), cors)
		return nil
	}
	if _, err := os.Stat(target); err != nil {
		c.SendJSON(404, localOpenFail(404, "Path does not exist on this computer"), cors)
		return nil
	}
	result, err := hostopen.OpenExternal(target)
	if err != nil {
		var pe *hostopen.Error
		if errors.As(err, &pe) {
			c.SendJSON(pe.Status, localOpenFail(pe.Status, pe.Message), cors)
			return nil
		}
		return err
	}
	out := pyjson.NewObject("ok", true, "path", target, "mode", "local")
	out.Update(result)
	c.SendJSON(200, out, cors)
	return nil
}
