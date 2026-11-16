package server

// Services modules (branch feat/go-backend-services): assistant, chat, GPU,
// InfoHub import, local open.  Each module registers its routes in
// servicesPorted and answers from servicesGet / servicesPost, which
// routes.go calls at the place Python's dispatch reaches these paths.

import (
	"arkstudio/go/internal/assistant"
	"arkstudio/go/internal/infohub"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/store"
	"errors"
	"strings"
)

var servicesPorted = []PortedRoute{
	{"exact:/api/assistant/dispatch", "GET,HEAD"},
	{"exact:/api/assistant/apply", "POST"},
	{"exact:/api/assistant/extract", "POST"},
	// InfoHub import (importer.py)
	{"exact:/api/infohub/candidates", "GET,HEAD"},
	{"exact:/api/infohub/browse", "GET,HEAD"},
	{"exact:/api/infohub/unit", "GET,HEAD"},
	{"exact:/api/infohub/graph", "GET,HEAD"},
	{"exact:/api/infohub/import", "POST"},
	{"exact:/api/import/infohub", "POST"},
	{"exact:/api/import/infohub/sources", "GET,HEAD"},
	{"exact:/api/import/infohub/preview", "GET,HEAD"},
	// Avatar writes: _handle_profile answers 405 for every write to
	// /api/users/<name>/avatar (profileWrite).
	{"regex:^/api/users/([^/]+)/avatar$", "DELETE,PATCH,POST"},
}

func init() { Ported = append(Ported, servicesPorted...) }

// servicesGet answers the services GET routes (Handler._api_get).
func (c *reqCtx) servicesGet(path string) (bool, error) {
	if handled, err := c.gpuGet(path); handled {
		return true, err
	}
	switch path {
	case "/api/chat", "/api/chat/status":
		return true, c.chatGet(path)
	case "/api/assistant/dispatch":
		task := c.query.First("task", "")
		if task == "" {
			return true, apiErr(400, "task is required", "")
		}
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		out, err := assistant.Dispatch(c.ctx, p.Store, task)
		if err == nil {
			c.ok(out)
		}
		return true, err
	case "/api/infohub/candidates", "/api/import/infohub/sources":
		name := "candidates"
		if path == "/api/import/infohub/sources" {
			name = "sources"
		}
		c.ok(pyjson.NewObject(name, infohub.Candidates()))
		return true, nil
	case "/api/import/infohub/preview":
		src := c.query.First("path", "")
		if src == "" {
			return true, apiErr(400, "path is required", "")
		}
		out, err := infohub.Preview(c.ctx, src)
		if err == nil {
			c.ok(out)
		}
		return true, infohubErr(err, false)
	case "/api/infohub/browse", "/api/infohub/graph", "/api/infohub/unit":
		return true, infohubErr(c.infohubGet(path), false)
	}
	return false, nil
}

// infohubErr maps a sqlite3.Error of the InfoHub file like do_GET / do_POST.
func infohubErr(err error, post bool) error {
	var se *infohub.SQLError
	if !errors.As(err, &se) {
		return err
	}
	if post {
		return apiErr(500, "Database error: "+se.Message, "")
	}
	return &store.InternalError{Message: se.Message}
}

// firstInfohubPath is Handler._first_infohub_path.
func firstInfohubPath() (string, error) {
	candidates := infohub.Candidates()
	if len(candidates) == 0 {
		return "", apiErr(400, "No InfoHub database found; set INFOHUB_DB or pass path", "")
	}
	return pyjson.Str(candidates[0].(*pyjson.Object).Value("path")), nil
}

func pyBoolQuery(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func (c *reqCtx) infohubGet(path string) error {
	q := c.query
	sourcePath := q.First("path", "")
	if sourcePath == "" {
		var err error
		if sourcePath, err = firstInfohubPath(); err != nil {
			return err
		}
	}
	key := q.First("key", "")
	if _, ok := q["key"]; !ok {
		key = q.First("id", "")
	}
	if path == "/api/infohub/unit" && key == "" {
		return apiErr(400, "key is required", "")
	}
	src, err := infohub.Open(sourcePath)
	if err != nil {
		return err
	}
	defer src.Close()
	p, err := c.require("read")
	if err != nil {
		return err
	}
	if path == "/api/infohub/unit" {
		unit, err := src.Unit(c.ctx, key, p.Store)
		if err == nil {
			c.ok(pyjson.NewObject("unit", unit))
		}
		return err
	}
	limitDef := int64(60)
	if path == "/api/infohub/graph" {
		limitDef = 160
	}
	f := infohub.Filter{
		Q: q.First("q", ""), Kind: q.First("kind", ""), Tags: q.First("tags", ""), Status: q.First("status", ""),
		Days: infohub.FloatArg(q.First("days", "0"), 0, 0, 36500), Mine: pyBoolQuery(q.First("mine", "0")),
		Roots: pyBoolQuery(q.First("roots", "0")), Order: q.First("order", "activity"),
		Limit: c.qInt("limit", limitDef, 1, 1000),
	}
	var out *pyjson.Object
	if path == "/api/infohub/browse" {
		f.Offset = c.qInt("offset", 0, 0, 100_000)
		out, err = src.Browse(c.ctx, p.Store, f)
	} else {
		out, err = src.Graph(c.ctx, p.Store, f, pyBoolQuery(q.First("connected", "1")), pyBoolQuery(q.First("context", "0")))
	}
	if err == nil {
		c.ok(out)
	}
	return err
}

// pathArg is Path(value) for a JSON value (str only).
func pathArg(v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	return "", &store.InternalError{Message: "expected str, bytes or os.PathLike object, not " + store.PyTypeName(v)}
}

func (c *reqCtx) infohubPost(path string, body *pyjson.Object, actor string) error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	if path == "/api/import/infohub" {
		sp, err := pathArg(orDefault(body.Value("path"), ""))
		if err != nil {
			return err
		}
		src, err := infohub.Open(sp)
		if err != nil {
			return err
		}
		defer src.Close()
		mode := orDefault(body.Value("mode"), "topics")
		switch mode.(type) {
		case []any, *pyjson.Object:
			return &store.InternalError{Message: "unhashable type: '" + store.PyTypeName(mode) + "'"}
		}
		m, _ := mode.(string) // other values are not "topics" or "all"
		out, err := src.ImportInto(c.ctx, p.Store, m, pyjson.Truthy(body.Value("update")), actor, c.who(p))
		if err == nil {
			c.ok(out)
		}
		return err
	}
	raw := orDefault(body.Value("path"), nil)
	var sp string
	if raw == nil {
		if sp, err = firstInfohubPath(); err != nil {
			return err
		}
	} else if sp, err = pathArg(raw); err != nil {
		return err
	}
	src, err := infohub.Open(sp)
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := src.ImportKey(c.ctx, p.Store, pyjson.Str(orDefault(body.Value("key"), "")), actor,
		pyjson.Truthy(body.Value("as_idea")), pyjson.Truthy(body.Value("copy")), c.who(p))
	if err == nil {
		c.ok(out)
	}
	return err
}

// servicesPost answers the services POST routes (Handler.do_POST, after the
// JSON body was read).
func (c *reqCtx) servicesPost(path string, body *pyjson.Object, actor string) (bool, error) {
	if handled, err := c.gpuPost(path, body); handled {
		return true, err
	}
	switch path {
	case "/api/assistant/apply", "/api/assistant/extract":
		p, err := c.require("read")
		if err != nil {
			return true, err
		}
		var out *pyjson.Object
		if path == "/api/assistant/apply" {
			out, err = assistant.Apply(c.ctx, p.Store, orDefault(body.Value("insight"), pyjson.NewObject()), actor)
		} else {
			out, err = assistant.Extract(c.ctx, p.Store, orDefault(body.Value("text"), ""), body.Value("source"),
				orDefault(body.Value("limit"), int64(24)), actor, c.who(p))
		}
		if err == nil {
			c.ok(out)
		}
		return true, err
	case "/api/open":
		return true, c.openExternal(body)
	case "/api/chat":
		return true, c.chatSend(body)
	case "/api/import/infohub", "/api/infohub/import":
		return true, infohubErr(c.infohubPost(path, body, actor), true)
	}
	return false, nil
}
