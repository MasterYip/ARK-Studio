package server

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	if v.HostSource == "header" { // an agent on a device: remember when it was last seen
		if _, err := os.Stat(filepath.Join(p.Path, setup.ProfileDir)); err == nil {
			c.setupEnv(p).TouchHost(v, "")
		}
	}
	data, err := c.setupPayloadFor(p, v, false)
	c.app.sizes().scan(p.Path) // Go only: due sizes of this host's locations, in the background
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
	"location": true, "host": true, "propose": true, "confirm": true, "verify": true,
	"location-stats": true, "measure": true}

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
	rawLayer := pytext.Lower(pytext.Strip(orStrV(body.Value("layer"))))
	// propose without a layer (or "all") sends every changed layer at once;
	// confirm with a "layers" list confirms several layers at once.
	allLayers := action == "propose" && (rawLayer == "" || rawLayer == "all")
	confirmList, multiConfirm := body.Value("layers").([]any)
	multiConfirm = multiConfirm && action == "confirm"
	if allLayers || multiConfirm {
		rawLayer = "project"
	}
	layer := pytext.Lower(pytext.Strip(orStrV(orV(rawLayer, defaultLayer))))
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
		for _, k := range []string{"ok", "errors", "warnings", "location", "host", "proposed", "pending", "confirmed"} {
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
			if pyjson.Truthy(body.Value("propose")) {
				if err := c.setupProposeAll(p, env, v, actor, result, false); err != nil {
					return err
				}
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
			if pyjson.Truthy(body.Value("propose")) {
				if err := c.setupProposeAll(p, env, v, actor, result, false); err != nil {
					return err
				}
			}
			return done(result)
		case "location-stats": // Go only (2026-11-20): sizes measured on the caller's device
			if _, err := c.require("write"); err != nil {
				return err
			}
			source := "agent"
			if c.agentToken() == "" && !strings.HasPrefix(actor, "agent:") {
				source = "desktop"
			}
			result, err := env.ReportLocationStats(v, body, actor, source)
			if err != nil {
				return err
			}
			c.ok(result)
			return nil
		case "measure": // Go only (2026-11-20): measure a location on this host again (the refresh button)
			return c.setupMeasure(p, env, body)
		case "alias": // Go only (T-0053): host id `from` is this device (X-ARK-Host)
			if _, err := c.require("write"); err != nil {
				return err
			}
			result, err := env.AddAlias(v, orStrV(body.Value("from")), actor)
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
			if allLayers {
				result := pyjson.NewObject("ok", true)
				if err := c.setupProposeAll(p, env, v, actor, result, true); err != nil {
					return err
				}
				return done(result)
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
			items := []setup.LayerDigest{{Layer: layer, Digest: orStrV(body.Value("digest"))}}
			if multiConfirm {
				items = items[:0]
				for _, x := range confirmList {
					item, _ := x.(*pyjson.Object)
					name := pytext.Lower(pytext.Strip(orStrV(item.Value("layer"))))
					if name != "project" && name != "user" {
						return apiErr(400, "layer must be project or user", "SETUP_INVALID")
					}
					owner := pytext.Strip(orStrV(item.Value("owner")))
					if name == "user" && owner != "" && !(v.User != "" && pytext.Lower(v.User) == pytext.Lower(owner)) {
						return apiErr(403, "you can only change your own setup layer", "SETUP_NOT_OWNER")
					}
					items = append(items, setup.LayerDigest{Layer: name, Digest: orStrV(item.Value("digest"))})
				}
			}
			level := "write"
			for _, item := range items {
				if item.Layer == "project" {
					level = "admin"
				}
			}
			if _, err := c.require(level); err != nil {
				return err
			}
			human := "human:" + v.User
			if v.User == "" {
				human = "human:ui"
			}
			results, err := env.ConfirmLayers(v, items, human, orStrV(body.Value("note")))
			if err != nil {
				return err
			}
			for _, x := range results {
				r := x.(*pyjson.Object)
				if err := p.Store.LogActivity(c.ctx, human, "setup."+pyjson.Str(r.Value("layer"))+".confirmed", nil,
					pyjson.NewObject("digest", r.Value("digest"))); err != nil {
					return err
				}
			}
			result := results[0].(*pyjson.Object)
			if multiConfirm {
				result = pyjson.NewObject("ok", true, "confirmed", results)
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

// setupProposeAll sends every changed layer for confirmation (setup.ProposeAll),
// logs each newly proposed layer and adds "proposed" (the layers this request
// sent, {layer, owner, digest}) and "pending" (every layer awaiting
// confirmation, same shape) to result.
func (c *reqCtx) setupProposeAll(p *project.Project, env *setup.Env, v *setup.Viewer, actor string, result *pyjson.Object, strict bool) error {
	layers, warnings, err := env.ProposeAll(v, actor, strict)
	if err != nil {
		return err
	}
	// The layer this request wrote may already be proposed (SaveLayer).
	written := ""
	if result.Value("state") == "proposed" {
		written = orStrV(result.Value("layer"))
	}
	sent := []any{}
	for _, x := range layers {
		item := x.(*pyjson.Object)
		name := pyjson.Str(item.Value("layer"))
		if pyjson.Truthy(item.Value("proposed")) {
			if err := p.Store.LogActivity(c.ctx, actor, "setup."+name+".proposed", nil,
				pyjson.NewObject("digest", item.Value("digest"))); err != nil {
				return err
			}
		} else if name != written || item.Value("state") != "proposed" {
			continue
		}
		sent = append(sent, pyjson.NewObject("layer", name, "owner", item.Value("owner"), "digest", item.Value("digest")))
	}
	if len(warnings) > 0 {
		all, _ := result.Value("warnings").([]any)
		for _, w := range warnings {
			all = append(all, w)
		}
		result.Set("warnings", all)
	}
	result.Set("proposed", sent).Set("pending", setup.Pending(layers))
	return nil
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

// setupMeasure queues a size measurement of one location (resource key and
// path) when it is on the ARK server's host; a location on another device
// is measured there (desktop app, agent).
func (c *reqCtx) setupMeasure(p *project.Project, env *setup.Env, body *pyjson.Object) error {
	resource := pytext.Strip(orStrV(body.Value("resource")))
	path := pytext.Strip(orStrV(orV(body.Value("path_or_ref"), body.Value("path"))))
	if resource == "" {
		return apiErr(400, "resource is required", "SETUP_INVALID")
	}
	locs := env.FindLocations(resource, path)
	if len(locs) == 0 {
		return apiErr(404, "no such resource location", "SETUP_NO_LOCATION")
	}
	server := setup.MachineFingerprint()
	for _, l := range locs {
		if !env.SameHost(l.HostID, server) || !setup.IsLocalPath(l.Path) {
			continue
		}
		svc := c.app.sizes()
		if svc.off {
			return apiErr(409, "measuring sizes is turned off on this server", "SETUP_SIZES_OFF")
		}
		queued, wait := svc.refresh(sizeJob{p.Path, l.Resource, l.HostID, l.Path})
		out := pyjson.NewObject("ok", true, "queued", queued, "where", "server", "path_or_ref", l.Path)
		if wait > 0 {
			out.Set("retry_after", int64(wait/time.Second)+1)
		}
		c.ok(out)
		return nil
	}
	return apiErr(409, "this path is on another device; its size is measured on that device (ARK desktop app or an agent there)",
		"SETUP_SIZE_ON_DEVICE")
}
