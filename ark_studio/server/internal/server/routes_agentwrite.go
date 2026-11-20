package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/rnd"
	"arkstudio/go/internal/settings"
	"arkstudio/go/internal/store"
)

// agentWritePorted are the contract routes this file answers (router.go registers them).
var agentWritePorted = []PortedRoute{
	{"exact:/api/agent/v1/assets/upload", "POST"},
	{"exact:/api/agent/v1/links", "POST"},
	{"exact:/api/agent/v1/media", "POST"},
	{"exact:/api/agent/v1/media/upload", "POST"},
	{"exact:/api/agent/v1/project/palette", "POST"},
	{"exact:/api/agent/v1/units", "POST"},
	{"fullmatch:/api/agent/v1/units/([^/]+)/history(?:/([^/]+))?", "POST"},
	{"regex:^/api/agent/v1/units/([^/]+)$", "POST"},
	{"regex:^/api/agent/v1/units/([^/]+)/comments$", "POST"},
	{`regex:^/api/agent/v1/units/([^/]+)/comments/(\d+)/resolve$`, "POST"},
	{"regex:^/api/agent/v1/units/([^/]+)/members$", "POST"},
}

// Handler._agent_write (the store-backed branches) and the embedded media /
// asset uploads (_embed_media, _upload_asset).

var (
	reAgentUnitW     = pyRe(`^/api/agent/v1/units/([^/]+)$`, false)
	reAgentCommentsW = pyRe(`^/api/agent/v1/units/([^/]+)/comments$`, false)
	reAgentResolveW  = pyRe(`^/api/agent/v1/units/([^/]+)/comments/(\d+)/resolve$`, false)
	reAgentMembersW  = pyRe(`^/api/agent/v1/units/([^/]+)/members$`, false)
	reAgentSetup     = pyRe(`^/api/agent/v1/setup/([a-z][a-z-]*)$`, false)
	safeAssetRe      = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

// agentActor is server._agent_actor.
func agentActor(actor string) string {
	if strings.HasPrefix(actor, "agent:") {
		return actor
	}
	return "agent:api"
}

// agentWrite is Handler._agent_write.
func (c *reqCtx) agentWrite(path string, body *pyjson.Object, actor string) error {
	if handled, err := c.agentSessionUpload(path, body, actor); handled {
		return err
	}
	if handled, err := c.agentMessagesPost(path, body, actor); handled { // Go only (routes_agentwatch.go)
		return err
	}
	p, err := c.require("read")
	if err != nil {
		return err
	}
	s := p.Store
	if handled, err := c.agentReviewsPost(p, path, body, actor); handled { // Go only (routes_reviews.go)
		return err
	}
	if m := reAgentHistoryRaw.FindStringSubmatch(path); m != nil {
		return c.nodeHistoryWrite(m[1], m[2], body, actor)
	}
	if handled, err := c.gpuHandlers().AgentPost(c, path, body); handled {
		return err
	}
	if m := reAgentSetup.FindStringSubmatch(path); m != nil {
		return c.setupAction(m[1], body, actor)
	}
	reply := func(out *pyjson.Object, err error) error {
		if err != nil {
			return err
		}
		c.ok(out)
		return nil
	}
	switch path {
	case "/api/agent/v1/project/palette":
		var palette any
		encoded, err := p.Manifest.Mutate(func(m *pyjson.Object) error {
			next, err := settings.SettingsPatch(m, pyjson.NewObject("palette", body), nil)
			if err != nil {
				if se, ok := err.(*settings.Error); ok {
					return apiErr(400, se.Message, "")
				}
				return err
			}
			m.Set("settings", next).Set("updated", clock.UTCNow())
			palette = next.Value("palette")
			return nil
		})
		if err != nil {
			return err
		}
		if err := store.WriteManifest(p.Path, encoded); err != nil {
			return err
		}
		c.ok(pyjson.NewObject("palette", palette))
		return nil
	case "/api/agent/v1/units":
		unit, err := s.CreateUnit(c.ctx, body, actor, c.who(p))
		if err != nil {
			return err
		}
		out := pyjson.NewObject("unit", unit)
		c.withReviewWarnings(s, pyjson.Str(unit.Value("uid")), body, out) // Go only (routes_reviews.go)
		c.ok(out)
		return nil
	case "/api/agent/v1/links":
		return reply(s.CreateLink(c.ctx, body.Value("source"), body.Value("target"),
			orDefault(body.Value("relation"), "relates_to"), orDefault(body.Value("note"), ""), actor))
	case "/api/agent/v1/assets/upload":
		return reply(c.uploadAsset(body, actor))
	case "/api/agent/v1/media/upload":
		return reply(c.embedMedia(body, actor))
	case "/api/agent/v1/media":
		return reply(s.AddMedia(c.ctx, body.Value("unit"), body.Value("url"), orDefault(body.Value("kind"), "link"),
			orDefault(body.Value("caption"), ""), actor))
	}
	if m := reAgentCommentsW.FindStringSubmatch(path); m != nil {
		kind := body.Value("kind")
		if !pyjson.Truthy(kind) {
			kind = "agent_reply"
		}
		comment, err := s.AddComment(c.ctx, m[1], body.Value("body"), pyjson.Str(kind), body.Value("reply_to"), "user", agentActor(actor))
		if err != nil {
			return err
		}
		return c.agentCommentsReply(s, m[1], comment)
	}
	if m := reAgentMembersW.FindStringSubmatch(path); m != nil {
		add := body.Value("add")
		if !pyjson.Truthy(add) {
			add = body.Value("members")
		}
		if !pyjson.Truthy(add) {
			add = []any{}
		}
		return c.designReply(s, m[1], body.Copy().Set("add", add), actor)
	}
	if m := reAgentResolveW.FindStringSubmatch(path); m != nil {
		resolved := true
		if v, ok := body.Get("resolved"); ok && v == false {
			resolved = false
		}
		comment, err := s.ResolveComment(c.ctx, m[1], m[2], resolved, "user", agentActor(actor))
		if err != nil {
			return err
		}
		return c.agentCommentsReply(s, m[1], comment)
	}
	if m := reAgentUnitW.FindStringSubmatch(path); m != nil {
		unit, err := s.UpdateUnit(c.ctx, m[1], body, actor, c.who(p))
		if err != nil {
			return err
		}
		out := pyjson.NewObject("unit", unit)
		c.withReviewWarnings(s, m[1], body, out) // Go only (routes_reviews.go)
		c.ok(out)
		return nil
	}
	return apiErr(404, "Unknown agent endpoint: "+path, "")
}

func (c *reqCtx) agentCommentsReply(s *store.Store, ref string, comment *pyjson.Object) error {
	comments, err := s.Comments(c.ctx, ref)
	if err != nil {
		return err
	}
	out := pyjson.NewObject("comment", comment)
	payload := commentsPayload(comments)
	for _, k := range payload.Keys() {
		out.Set(k, payload.Value(k))
	}
	c.ok(out)
	return nil
}

// uuid4Hex is uuid.uuid4().hex.
func uuid4Hex() string { return rnd.UUID4Hex() }

// suffixOf is pathlib.PurePath(name).suffix.
func suffixOf(name string) string {
	base := pathName(name)
	if base == "" || base == "." || base == ".." {
		return ""
	}
	i := strings.LastIndex(base, ".")
	if i <= 0 || i == len(base)-1 {
		return ""
	}
	return base[i:]
}

// pathName is pathlib.Path(s).name on Windows (both separators, drive).
func pathName(s string) string {
	s = strings.ReplaceAll(s, `\`, "/")
	if len(s) >= 2 && s[1] == ':' && ((s[0] >= 'A' && s[0] <= 'Z') || (s[0] >= 'a' && s[0] <= 'z')) {
		s = s[2:]
	}
	var parts []string
	for _, p := range strings.Split(s, "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// embedMedia is Handler._embed_media.
func (c *reqCtx) embedMedia(body *pyjson.Object, actor string) (*pyjson.Object, error) {
	unitRef := body.Value("unit")
	filename := pytext.Strip(strOrFalsy(body.Value("filename"), "asset"))
	raw, ok := strictBase64(strOrFalsy(body.Value("data"), ""))
	if !ok {
		return nil, apiErr(400, "media upload data is not valid base64", "")
	}
	if len(raw) == 0 {
		return nil, apiErr(400, "media upload is empty", "")
	}
	if len(raw) > 12*1024*1024 {
		return nil, apiErr(413, "embedded media must be 12 MB or smaller", "")
	}
	suffix := pytext.Lower(suffixOf(filename))
	switch suffix {
	case ".jpg", ".jpeg", ".png", ".gif", ".svg", ".webp":
	default:
		return nil, apiErr(400, "only jpg, jpeg, png, gif, svg and webp files can be embedded", "")
	}
	p, err := c.require("read")
	if err != nil {
		return nil, err
	}
	folder := filepath.Join(p.Path, "assets", "media")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return nil, err
	}
	name := uuid4Hex() + suffix
	if err := os.WriteFile(filepath.Join(folder, name), raw, 0o644); err != nil {
		return nil, err
	}
	caption := body.Value("caption")
	if !pyjson.Truthy(caption) {
		caption = filename
	}
	return p.Store.AddMedia(c.ctx, unitRef, "assets/media/"+name, strOrFalsy(body.Value("kind"), "image"), caption, actor)
}

// uploadAsset is Handler._upload_asset.
func (c *reqCtx) uploadAsset(body *pyjson.Object, actor string) (*pyjson.Object, error) {
	unitRef := body.Value("unit")
	filename := pytext.Strip(strOrFalsy(body.Value("filename"), "asset"))
	original := pytext.Prefix(pathName(filename), 180)
	if original == "" {
		original = "asset"
	}
	raw, ok := strictBase64(strOrFalsy(body.Value("data"), ""))
	if !ok {
		return nil, apiErr(400, "asset upload data is not valid base64", "")
	}
	if len(raw) == 0 {
		return nil, apiErr(400, "asset upload is empty", "")
	}
	if len(raw) > 64*1024*1024 {
		return nil, apiErr(413, "asset must be 64 MB or smaller", "")
	}
	suffix := pytext.Prefix(pytext.Lower(suffixOf(original)), 24)
	safe := safeAssetRe.ReplaceAllString(original, "_")
	if safe == "" || strings.HasPrefix(safe, ".") {
		safe = "asset"
	}
	p, err := c.require("read")
	if err != nil {
		return nil, err
	}
	folder := filepath.Join(p.Path, "assets", "files")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return nil, err
	}
	name := uuid4Hex() + "-" + safe
	if err := os.WriteFile(filepath.Join(folder, name), raw, 0o644); err != nil {
		return nil, err
	}
	kind := pytext.Strip(strOrFalsy(body.Value("kind"), ""))
	if kind == "" {
		switch suffix {
		case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".avif":
			kind = "image"
		case ".mp4", ".webm", ".mov", ".mkv":
			kind = "video"
		case ".mp3", ".wav", ".ogg", ".m4a":
			kind = "audio"
		default:
			kind = "document"
		}
	}
	caption := body.Value("caption")
	if !pyjson.Truthy(caption) {
		caption = original
	}
	return p.Store.AddMedia(c.ctx, unitRef, "assets/files/"+name, kind, caption, actor)
}
