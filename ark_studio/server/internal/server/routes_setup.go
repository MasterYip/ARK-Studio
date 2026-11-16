package server

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/setup"
)

// Handler._setup_viewer, _setup_payload, _setup_action and
// _setup_gpu_servers (arkstudio/skills/setup.py, setup_layers.py).

var setupPorted = []PortedRoute{
	{"regex:^/api/agent/v1/setup/([a-z][a-z-]*)$", "POST"},
}

// setupViewer is Handler._setup_viewer(project, body, query).  useQuery is
// false for POST, where Python passes no query.
func (c *reqCtx) setupViewer(p *project.Project, body *pyjson.Object, useQuery bool) (*setup.Viewer, error) {
	host, source := "", "server"
	type cand struct {
		v      any
		origin string
	}
	cands := []cand{{c.header("X-ARK-Host"), "header"}}
	q := ""
	if useQuery {
		q = c.query.First("host", "")
	}
	cands = append(cands, cand{q, "query"})
	if body != nil {
		cands = append(cands, cand{body.Value("host_id"), "body"})
	} else {
		cands = append(cands, cand{nil, "body"})
	}
	for _, x := range cands {
		if host = setup.CleanHostID(x.v); host != "" {
			source = x.origin
			break
		}
	}
	bearer := c.agentToken()
	if c.token == nil && bearer != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(c.app.AgentToken())) == 1 {
		v := setup.LegacyViewer(host)
		v.HostSource = source
		return v, nil
	}
	user := c.actor()
	perm := "admin"
	if c.app.Auth != nil {
		perm = "read"
		if p.ID() == demoID {
			perm = "write"
		} else if pr, err := c.app.Org.ProjectPermission(c.ctx, user, p.ID()); err != nil {
			return nil, err
		} else if pr != "" {
			perm = pr
		}
	}
	rank, ok := org.PermissionRank[perm]
	if !ok {
		rank = 1
	}
	kind := "session"
	if c.token != nil {
		scope := orStrV(c.token.row.Get("scope"))
		if scope == "" {
			scope = "read"
		}
		capRank, ok := org.PermissionRank[scope]
		if !ok {
			capRank = 1
		}
		rank = min(rank, capRank)
		kind = "personal"
	} else if c.app.Auth == nil {
		kind = "local"
	}
	if c.mobileSession != nil && c.mobileSession.Permission == "read_only" {
		rank = min(rank, org.PermissionRank["read"])
	}
	return setup.NewViewer(setup.Viewer{User: user, Admin: rank >= org.PermissionRank["admin"],
		CanWrite: rank >= org.PermissionRank["write"], TokenKind: kind, HostID: host, HostSource: source}), nil
}

// setupEnv is the project's setup folder with _setup_default_owner and
// _setup_known_user.
func (c *reqCtx) setupEnv(p *project.Project) *setup.Env {
	owner := c.ownerBootstrapName()
	if owner == "" {
		owner = org.LocalUsername
	}
	return &setup.Env{Project: p.Path, DefaultOwner: owner, KnownUser: func(name string) string {
		if c.app.Auth == nil {
			if pytext.Lower(name) == org.LocalUsername {
				return org.LocalUsername
			}
			return ""
		}
		row, ok, err := c.app.Org.UserByName(c.ctx, name)
		if err != nil || !ok {
			return ""
		}
		return pyjson.Str(row.Get("username"))
	}}
}

// setupPayload is Handler._setup_payload for the GET viewer.
func (c *reqCtx) setupPayload(p *project.Project) (*pyjson.Object, error) {
	v, err := c.setupViewer(p, nil, true)
	if err != nil {
		return nil, err
	}
	data, err := c.setupPayloadFor(p, v, false)
	var se *setup.Error
	if errors.As(err, &se) {
		return nil, apiErr(500, "Internal error: "+se.Message, "")
	}
	return data, err
}

// setupPayloadFor is skills.setup_layers.payload(project.path, viewer).  A
// project without a setup folder answers the template captured from Python.
func (c *reqCtx) setupPayloadFor(p *project.Project, v *setup.Viewer, locked bool) (*pyjson.Object, error) {
	if _, err := os.Stat(filepath.Join(p.Path, setup.ProfileDir)); err == nil {
		if locked {
			return c.setupEnv(p).PayloadLocked(v)
		}
		return c.setupEnv(p).Payload(v)
	}
	class, user := "read", c.actor()
	switch {
	case v.Legacy:
		class, user = "legacy", ""
	case v.Admin:
		class = "admin"
	case v.CanWrite:
		class = "write"
	}
	server := 0
	if v.HostID == setup.MachineFingerprint() {
		server = 1
	}
	text := setupTemplates[class+":"+strconv.Itoa(server)]
	text = strings.ReplaceAll(text, setupLabel, jsonBody("host-"+pytext.Prefix(v.HostID, 6)))
	text = strings.ReplaceAll(text, setupHost, jsonBody(v.HostID))
	text = strings.ReplaceAll(text, setupUser, jsonBody(user))
	text = strings.ReplaceAll(text, setupKind, jsonBody(v.TokenKind))
	text = strings.ReplaceAll(text, setupSource, jsonBody(v.HostSource))
	o, ok := pyjson.LoadsObject(text)
	if !ok {
		return nil, fmt.Errorf("setup template is not JSON")
	}
	return o, nil
}

func jsonBody(s string) string {
	b := pyjson.AppendString(nil, s)
	return string(b[1 : len(b)-1])
}

// setupGPUServers is Handler._setup_gpu_servers (None on any error), names
// only (T-0018): discovery offers every configured server so a member can
// choose which to register, and verify checks names; neither needs the
// address, tags or note of a server the project may not have registered.
func (c *reqCtx) setupGPUServers() *pyjson.Object {
	s, err := c.app.gpu().Servers()
	if err != nil {
		return nil
	}
	list, _ := s.Value("servers").([]any)
	names := make([]any, 0, len(list))
	for _, item := range list {
		if o, ok := item.(*pyjson.Object); ok {
			names = append(names, pyjson.NewObject("name", o.Value("name")))
		}
	}
	out := pyjson.NewObject("servers", names)
	if hint := s.Value("hint"); hint != nil {
		out.Set("hint", hint)
	}
	return out
}

var setupActions = map[string]bool{"discover": true, "draft": true, "resource": true, "remove": true,
	"location": true, "host": true, "propose": true, "confirm": true, "verify": true}

// setupAction is Handler._setup_action.
func (c *reqCtx) setupAction(action string, body *pyjson.Object, actor string) error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	v, err := c.setupViewer(p, body, false)
	if err != nil {
		return err
	}
	env := c.setupEnv(p)
	setup.Lock()
	defer setup.Unlock()
	defaultLayer := "user"
	if v.Legacy {
		defaultLayer = "project"
	}
	if action == "confirm" && !pyjson.Truthy(body.Value("layer")) {
		mine := ""
		if v.User != "" {
			if mine, err = env.LoadUserDigest(v.User); err != nil {
				return setupAPIErr(err)
			}
		}
		defaultLayer = "project"
		if mine != "" && mine == orStrV(body.Value("digest")) {
			defaultLayer = "user"
		}
	}
	layer := pytext.Lower(pytext.Strip(orStrV(orV(body.Value("layer"), defaultLayer))))
	if layer != "project" && layer != "user" {
		return apiErr(400, "layer must be project or user", "SETUP_INVALID")
	}
	owner := pytext.Strip(orStrV(body.Value("owner")))
	if layer == "user" && owner != "" && !(v.User != "" && pytext.Lower(v.User) == pytext.Lower(owner)) {
		return apiErr(403, "you can only change your own setup layer", "SETUP_NOT_OWNER")
	}
	done := func(result *pyjson.Object) error {
		data, err := c.setupPayloadFor(p, v, true)
		if err != nil {
			return err
		}
		for _, k := range []string{"ok", "errors", "warnings", "location", "host"} {
			if val, ok := result.Get(k); ok {
				data.Set(k, val)
			}
		}
		c.ok(data)
		return nil
	}
	err = func() error {
		switch action {
		case "discover":
			var paths []any
			if l, ok := body.Value("paths").([]any); ok {
				paths = l
			}
			found := setup.Discover(paths, c.setupGPUServers(), c.installedProviders(), p.Path)
			items := setup.LegacyResources(found.Value("draft").(*pyjson.Object))
			for _, x := range items {
				vis := "private"
				if layer == "project" {
					vis = "shared"
				}
				x.(*pyjson.Object).Set("visibility", vis)
			}
			found.Set("layer", layer).Set("resources", items).Set("host", pyjson.NewObject("id", v.HostID))
			c.ok(found)
			return nil
		case "draft", "resource", "remove":
			if _, err := c.require("write"); err != nil {
				return err
			}
			var raw *pyjson.Object
			switch action {
			case "draft":
				raw = pyjson.NewObject()
				if prof, ok := body.Value("profile").(*pyjson.Object); ok {
					raw = prof.Copy()
				}
				for _, k := range []string{"resources", "locations", "hosts"} {
					if l, ok := body.Value(k).([]any); ok {
						raw.Set(k, l)
					}
				}
			case "resource":
				item, _ := body.Value("resource").(*pyjson.Object)
				if item.Len() == 0 {
					return apiErr(400, "resource is required", "SETUP_INVALID")
				}
				raw = pyjson.NewObject("resources", []any{item})
			default:
				rid := pytext.Strip(orStrV(body.Value("id")))
				if rid == "" {
					return apiErr(400, "id is required", "SETUP_INVALID")
				}
				raw = pyjson.NewObject("resources", []any{}, "delete", []any{rid})
			}
			result, err := env.SaveLayer(raw, v, layer, actor, pyjson.Truthy(body.Value("propose")), orStrV(body.Value("host_label")))
			if err != nil {
				return err
			}
			if err := p.Store.LogActivity(c.ctx, actor, "setup."+layer+"."+pyjson.Str(result.Value("state")), nil,
				pyjson.NewObject("digest", result.Value("digest"))); err != nil {
				return err
			}
			return done(result)
		case "location":
			if _, err := c.require("write"); err != nil {
				return err
			}
			result, err := env.AddLocation(v, orStrV(body.Value("resource")), orStrV(orV(body.Value("path_or_ref"), body.Value("path"))),
				actor, orStrV(body.Value("host_id")), orStrV(body.Value("host_label")), orStrV(body.Value("scope")))
			if err != nil {
				return err
			}
			return done(result)
		case "host":
			if _, err := c.require("write"); err != nil {
				return err
			}
			result, err := env.SetHost(v, orStrV(body.Value("label")), actor, orStrV(body.Value("id")))
			if err != nil {
				return err
			}
			return done(result)
		case "propose":
			if _, err := c.require("write"); err != nil {
				return err
			}
			result, err := env.Propose(v, layer, actor)
			if err != nil {
				return err
			}
			if err := p.Store.LogActivity(c.ctx, actor, "setup."+layer+".proposed", nil,
				pyjson.NewObject("digest", result.Value("digest"))); err != nil {
				return err
			}
			return done(result)
		case "confirm":
			if c.agentToken() != "" || !setup.IsHumanActor(actor) {
				return &setup.Error{Message: "only a human can confirm the environment profile; agents may " +
					"draft and propose", Status: 403, Code: "SETUP_HUMAN_REQUIRED"}
			}
			if err := c.rejectTaskReadToken("confirm the environment setup"); err != nil {
				return err
			}
			level := "write"
			if layer == "project" {
				level = "admin"
			}
			if _, err := c.require(level); err != nil {
				return err
			}
			human := "human:" + v.User
			if v.User == "" {
				human = "human:ui"
			}
			result, err := env.ConfirmLayer(v, layer, human, orStrV(body.Value("digest")), orStrV(body.Value("note")))
			if err != nil {
				return err
			}
			if err := p.Store.LogActivity(c.ctx, human, "setup."+layer+".confirmed", nil,
				pyjson.NewObject("digest", result.Value("digest"))); err != nil {
				return err
			}
			return done(result)
		case "verify":
			checked, err := env.VerifyView(v, actor, c.setupGPUServers())
			if err != nil {
				return err
			}
			if err := p.Store.LogActivity(c.ctx, actor, "setup.verify", nil, pyjson.NewObject("ok", checked.Value("ok"))); err != nil {
				return err
			}
			data, err := c.setupPayloadFor(p, v, true)
			if err != nil {
				return err
			}
			c.ok(data.Set("verify", checked))
			return nil
		}
		return apiErr(404, "Unknown setup action: "+action, "")
	}()
	return setupAPIErr(err)
}

// setupAPIErr is `except skills.SetupError as exc: raise ApiError(exc.status, str(exc), exc.code)`.
func setupAPIErr(err error) error {
	var se *setup.Error
	if errors.As(err, &se) {
		return apiErr(se.Status, se.Message, se.Code)
	}
	return err
}

func orV(a, b any) any {
	if pyjson.Truthy(a) {
		return a
	}
	return b
}

// orStrV is str(v or "").
func orStrV(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

// installedProviders is the installed provider ids of
// App.providers.summary(), in the summary's order (skills/setup.discover).
func (c *reqCtx) installedProviders() []string {
	ids := []string{}
	if c.app.Providers == nil {
		return ids
	}
	summary := c.app.Providers.Summary()
	providers, _ := summary.Value("providers").(*pyjson.Object)
	if providers == nil {
		return ids
	}
	for _, id := range providers.Keys() {
		if item, ok := providers.Value(id).(*pyjson.Object); ok && pyjson.Truthy(item.Value("installed")) {
			ids = append(ids, id)
		}
	}
	return ids
}
