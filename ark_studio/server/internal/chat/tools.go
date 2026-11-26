package chat

import (
	"errors"
	"sort"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/assistcfg"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/infohub"
	"arkstudio/go/internal/store"
)

func sortedStrings(in []string) []any {
	s := append([]string(nil), in...)
	sort.Strings(s)
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func knownTypes() []string {
	out := make([]string, len(store.KnownTypes))
	for i, t := range store.KnownTypes {
		out[i] = t[0]
	}
	return out
}

func fnSpec(name, description string, parameters *pyjson.Object) *pyjson.Object {
	return pyjson.NewObject("type", "function", "function", pyjson.NewObject(
		"name", name, "description", description, "parameters", parameters))
}

func params(properties *pyjson.Object, required ...any) *pyjson.Object {
	p := pyjson.NewObject("type", "object", "properties", properties)
	if len(required) > 0 {
		p.Set("required", required)
	}
	p.Set("additionalProperties", false)
	return p
}

func str() *pyjson.Object { return pyjson.NewObject("type", "string") }

func enum(values []any) *pyjson.Object { return pyjson.NewObject("type", "string", "enum", values) }

func integer(lo int64, hi int64) *pyjson.Object {
	o := pyjson.NewObject("type", "integer", "minimum", lo)
	if hi > 0 {
		o.Set("maximum", hi)
	}
	return o
}

func importance() *pyjson.Object { return enum([]any{"p0", "p1", "p2", "p3"}) }

// toolSpecs is _tool_specs().  scoped adds the person's other projects:
// list_projects, get_neighbors and an optional "project" argument on the
// unit tools (the Assistant chat; plugin conversations stay unscoped).
func toolSpecs(scoped ...bool) []any {
	types, statuses, relations := sortedStrings(knownTypes()), sortedStrings(store.KnownStatuses), sortedStrings(store.Relations)
	other := len(scoped) > 0 && scoped[0]
	props := func(kv ...any) *pyjson.Object {
		o := pyjson.NewObject(kv...)
		if other {
			o.Set("project", projectParam())
		}
		return o
	}
	read, write := "", ""
	if other {
		read = " Reads the current project unless project names another one."
		write = " Changes the current project unless project names another one you can write."
	}
	specs := []any{
		fnSpec("get_context", "Read current ARK project stats, next actions and recent units."+read,
			params(props())),
		fnSpec("search_units", "Search ARK units by title/body/uid, optionally by type."+read,
			params(props("query", str(), "type", str(), "limit", integer(1, 20)), "query")),
		fnSpec("list_units", "List/filter any ARK units by type, status, importance or free-text query."+read,
			params(props("type", enum(types), "status", enum(statuses), "importance", importance(),
				"query", str(), "limit", integer(1, 50)))),
		fnSpec("get_unit", "Read one ARK unit and its links by uid or numeric id."+read,
			params(props("ref", str()), "ref")),
		fnSpec("create_unit", "Create an ARK unit. Use only when the user asks to create/capture something."+write,
			params(props("type", enum(types), "title", str(), "body", str(), "status", enum(statuses),
				"importance", importance(), "tags", pyjson.NewObject("type", "array", "items", str())), "type", "title")),
		fnSpec("update_unit", "Update title/status/importance/tags of an existing ARK unit."+write,
			params(props("ref", str(), "title", str(), "status", enum(statuses), "importance", importance(),
				"tags", pyjson.NewObject("type", "array", "items", str())), "ref")),
		fnSpec("link_units", "Create a directed link between two ARK units."+write,
			params(props("source", str(), "target", str(), "relation", enum(relations), "note", str()),
				"source", "target", "relation")),
	}
	if other {
		specs = append(specs,
			fnSpec("list_projects", "List the ARK projects the user can read: id, name, the user's access (read, write or admin) and which one is current.",
				params(pyjson.NewObject())),
			fnSpec("get_neighbors", "Read the graph neighbourhood of one ARK unit: the units within depth links (1 or 2) and the links between them."+read,
				params(props("ref", str(), "depth", integer(1, 2)), "ref")))
	}
	return append(specs,
		fnSpec("get_infohub_context", "Read the current InfoHub graph scope: database path, active UI filter rules, node count and matching InfoHub topics. Use this before answering questions about what is currently visible in InfoHub.",
			params(pyjson.NewObject())),
		fnSpec("search_infohub", "Search the read-only InfoHub database by text, kind, tags, status or recent days. Returns topic keys and summaries.",
			params(pyjson.NewObject("query", str(), "kind", str(), "tags", str(), "status", str(),
				"days", integer(0, 0), "limit", integer(1, 100)))),
		fnSpec("get_infohub_unit", "Read one InfoHub topic, its hierarchy, tags and a compact plain-text conversation sample.",
			params(pyjson.NewObject("key", str()), "key")),
	)
}

func projectParam() *pyjson.Object {
	return pyjson.NewObject("type", "string",
		"description", "Project id (or exact name) from list_projects. Leave out for the current project.")
}

// infohubPath is _infohub_path(context).
func infohubPath(context *pyjson.Object) (string, error) {
	if context != nil {
		if p := pytext.Strip(orEmpty(context.Value("path"))); p != "" {
			return p, nil
		}
	}
	candidates := infohub.Candidates()
	if len(candidates) == 0 {
		return "", &store.Error{Message: "No InfoHub database found; set INFOHUB_DB or the InfoHub path"}
	}
	return pyjson.Str(candidates[0].(*pyjson.Object).Value("path")), nil
}

// infohubFilters is _infohub_filters(context).
func infohubFilters(context *pyjson.Object) *pyjson.Object {
	if context != nil {
		if f, ok := context.Value("filters").(*pyjson.Object); ok {
			return f
		}
	}
	return pyjson.NewObject()
}

func joinStrs(v any) (string, error) {
	if !pyjson.Truthy(v) {
		return "", nil
	}
	list, err := iterate(v)
	if err != nil {
		return "", err
	}
	out := ""
	for i, x := range list {
		if i > 0 {
			out += ","
		}
		out += pyjson.Str(x)
	}
	return out, nil
}

// intArg is int(v or default) inside _execute_tool: a ValueError becomes
// the tool's error, anything else propagates.
func intArg(v any, def int64) (int64, error) {
	if !pyjson.Truthy(v) {
		return def, nil
	}
	n, err := assistcfg.PyInt(v)
	if err != nil {
		var ce *assistcfg.Error
		if errors.As(err, &ce) {
			return 0, &PyError{ce.Type, ce.Message}
		}
	}
	return n, err
}

func clampInt(n, lo, hi int64) int64 { return min(hi, max(lo, n)) }

func (e *Env) openInfohub() (*infohub.Source, error) {
	path, err := infohubPath(e.Infohub)
	if err != nil {
		return nil, err
	}
	return infohub.Open(path)
}

func (e *Env) infohubGraph() (*pyjson.Object, error) {
	filters := infohubFilters(e.Infohub)
	src, err := e.openInfohub()
	if err != nil {
		return nil, err
	}
	defer src.Close()
	kinds, err := joinStrs(filters.Value("kinds"))
	if err != nil {
		return nil, err
	}
	tags, err := joinStrs(filters.Value("tags"))
	if err != nil {
		return nil, err
	}
	days, err := intArg(filters.Value("days"), 0)
	if err != nil {
		return nil, err
	}
	order := orEmpty(filters.Value("order"))
	if order == "" {
		order = "activity"
	}
	connected := true
	if v, ok := filters.Get("connected"); ok {
		connected = pyjson.Truthy(v)
	}
	f := infohub.Filter{Q: orEmpty(filters.Value("q")), Kind: kinds, Tags: tags, Status: orEmpty(filters.Value("status")),
		Days: float64(days), Mine: pyjson.Truthy(filters.Value("mine")), Roots: pyjson.Truthy(filters.Value("roots")),
		Order: order, Limit: 2000}
	return src.Graph(e.Ctx, e.Store, f, connected, pyjson.Truthy(filters.Value("context")))
}

func pick(o *pyjson.Object, keys ...string) *pyjson.Object {
	out := pyjson.NewObject()
	for _, k := range keys {
		out.Set(k, o.Value(k))
	}
	return out
}

// toList is a decoded or Go-built list value as []any.
func toList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []*pyjson.Object:
		out := make([]any, len(x))
		for i, o := range x {
			out[i] = o
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, o := range x {
			out[i] = o
		}
		return out
	}
	return nil
}

func head(v any, n int) []any {
	list := toList(v)
	if len(list) > n {
		return list[:n]
	}
	if list == nil {
		return []any{}
	}
	return list
}

func tail(v any, n int) []any {
	list := toList(v)
	if len(list) > n {
		return list[len(list)-n:]
	}
	if list == nil {
		return []any{}
	}
	return list
}

func orV(v, def any) any {
	if pyjson.Truthy(v) {
		return v
	}
	return def
}

func briefUnits(list []any, keys ...string) []any {
	out := []any{}
	for _, it := range list {
		out = append(out, pick(obj(it), keys...))
	}
	return out
}

func toolCaught(err error) bool {
	var pe *PyError
	if errors.As(err, &pe) {
		return pe.Type == "ValueError" || pe.Type == "DeepSeekError"
	}
	var se *store.Error
	var ve *pyerr.ValueError
	return errors.As(err, &se) || errors.As(err, &ve)
}

func errMessage(err error) string {
	var se *store.Error
	var ve *pyerr.ValueError
	var pe *PyError
	switch {
	case errors.As(err, &se):
		return se.Message
	case errors.As(err, &ve):
		return ve.Message
	case errors.As(err, &pe):
		return pe.Message
	}
	return err.Error()
}

// executeTool is _execute_tool: (result, did_write).
func (e *Env) executeTool(nameV any, argsV any) (*pyjson.Object, bool, error) {
	args, ok := argsV.(*pyjson.Object)
	if !ok {
		args = pyjson.NewObject()
	}
	name, _ := nameV.(string)
	if _, isStr := nameV.(string); !isStr {
		name = "\x00" // no tool matches a non-str name
	}
	write := name == "create_unit" || name == "update_unit" || name == "link_units"
	result, err := e.tool(name, args)
	if err != nil {
		if toolCaught(err) {
			return pyjson.NewObject("error", errMessage(err)), false, nil
		}
		return nil, false, err
	}
	if result == nil {
		return pyjson.NewObject("error", "Unknown tool: "+pyjson.Str(nameV)), false, nil
	}
	return result, write, nil
}

func (e *Env) tool(name string, args *pyjson.Object) (*pyjson.Object, error) {
	level := ""
	switch name {
	case "get_context", "search_units", "list_units", "get_unit", "get_neighbors":
		level = "read"
	case "create_unit", "update_unit", "link_units":
		level = "write"
	}
	var t *Target
	if level != "" {
		var err error
		if t, err = e.target(args, level); err != nil {
			return nil, err
		}
	}
	switch name {
	case "list_projects":
		return e.listProjects()
	case "get_neighbors":
		depth, err := intArg(args.Value("depth"), 1)
		if err != nil {
			return nil, err
		}
		return e.neighbors(t, orEmpty(args.Value("ref")), clampInt(depth, 1, 2))
	case "get_context":
		stats, err := t.Store.Stats(e.Ctx)
		if err != nil {
			return nil, err
		}
		actions, err := t.Store.NextActions(e.Ctx)
		if err != nil {
			return nil, err
		}
		recentData, err := t.Store.ListUnits(e.Ctx, listFilters("limit", int64(10), "sort", "updated"), false)
		if err != nil {
			return nil, err
		}
		if actions == nil {
			actions = []any{}
		}
		recent := briefUnits(items(recentData), "uid", "type", "title", "status")
		return e.withProject(t, pyjson.NewObject("stats", stats, "next_actions", actions, "recent", recent), recent), nil
	case "search_units":
		limit, err := intArg(args.Value("limit"), 8)
		if err != nil {
			return nil, err
		}
		f := listFilters("q", orEmpty(args.Value("query")), "limit", clampInt(limit, 1, 20))
		if t := args.Value("type"); pyjson.Truthy(t) {
			f.Types = []any{pyjson.Str(t)}
		}
		data, err := t.Store.ListUnits(e.Ctx, f, false)
		if err != nil {
			return nil, err
		}
		list := briefUnits(items(data), "uid", "type", "title", "status")
		return e.withProject(t, pyjson.NewObject("total", data.Value("total"), "items", list), list), nil
	case "list_units":
		limit, err := intArg(args.Value("limit"), 20)
		if err != nil {
			return nil, err
		}
		f := listFilters("limit", clampInt(limit, 1, 50), "sort", "updated")
		if t := args.Value("type"); pyjson.Truthy(t) {
			f.Types = []any{pyjson.Str(t)}
		}
		if s := args.Value("status"); pyjson.Truthy(s) {
			f.Statuses = []any{pyjson.Str(s)}
		}
		if i := args.Value("importance"); pyjson.Truthy(i) {
			f.Importance = []any{pyjson.Str(i)}
		}
		if q := args.Value("query"); pyjson.Truthy(q) {
			f.Q = pyjson.Str(q)
		}
		data, err := t.Store.ListUnits(e.Ctx, f, false)
		if err != nil {
			return nil, err
		}
		list := briefUnits(items(data), "uid", "type", "title", "status", "importance")
		return e.withProject(t, pyjson.NewObject("total", data.Value("total"), "items", list), list), nil
	case "get_unit":
		unit, err := t.Store.GetUnit(e.Ctx, orEmpty(args.Value("ref")))
		if err != nil {
			return nil, err
		}
		brief := pick(unit, "uid", "id", "type", "title", "body", "status", "importance", "tags")
		return e.withProject(t, pyjson.NewObject("unit", brief,
			"links_out", head(orV(unit.Value("links_out"), []any{}), 20), "links_in", head(orV(unit.Value("links_in"), []any{}), 20)),
			[]any{brief}), nil
	case "create_unit":
		tags := []any{}
		if t := args.Value("tags"); pyjson.Truthy(t) {
			list, err := iterate(t)
			if err != nil {
				return nil, err
			}
			tags = append(tags, list...)
		}
		tags = append(tags, "origin:chat")
		unit, err := t.Store.CreateUnit(e.Ctx, pyjson.NewObject(
			"type", orV(args.Value("type"), "note"), "title", orV(args.Value("title"), "Untitled"),
			"body", orV(args.Value("body"), ""), "status", orV(args.Value("status"), "open"),
			"importance", orV(args.Value("importance"), "p2"), "tags", tags, "origin", "assistant"),
			e.writeActor(), t.Who)
		if err != nil {
			return nil, err
		}
		out := pick(unit, "uid", "id", "title", "type")
		return e.withProject(t, out, []any{out}), nil
	case "update_unit":
		patch := pyjson.NewObject()
		for _, k := range []string{"title", "status", "importance", "tags"} {
			if v, ok := args.Get(k); ok {
				patch.Set(k, v)
			}
		}
		unit, err := t.Store.UpdateUnit(e.Ctx, orEmpty(args.Value("ref")), patch, e.writeActor(), t.Who)
		if err != nil {
			return nil, err
		}
		out := pick(unit, "uid", "title", "status")
		return e.withProject(t, out, []any{out}), nil
	case "link_units":
		relation := orEmpty(args.Value("relation"))
		if relation == "" {
			relation = "relates_to"
		}
		note := orEmpty(args.Value("note"))
		if note == "" {
			note = "Created by DeepSeek assistant"
		}
		l, err := t.Store.CreateLink(e.Ctx, orEmpty(args.Value("source")), orEmpty(args.Value("target")), relation, note, e.writeActor())
		if err != nil {
			return nil, err
		}
		return e.withProject(t, pick(l, "id", "source", "target", "relation")), nil
	case "get_infohub_context":
		data, err := e.infohubGraph()
		if err != nil {
			return nil, err
		}
		nodesV := orV(data.Value("nodes"), []any{})
		nodes := []any{}
		for _, n := range head(nodesV, 80) {
			o := obj(n)
			nodes = append(nodes, pyjson.NewObject("key", orV(o.Value("uid"), o.Value("key")), "kind", o.Value("kind"),
				"title", o.Value("title"), "status", o.Value("status"), "summary", pytext.Prefix(orEmpty(o.Value("summary")), 240),
				"messages", o.Value("n_messages"), "imported", o.Value("imported")))
		}
		topics := []any{}
		for _, n := range head(nodesV, 1000) {
			o := obj(n)
			topics = append(topics, pyjson.NewObject("key", orV(o.Value("uid"), o.Value("key")), "kind", o.Value("kind"),
				"title", pytext.Prefix(orEmpty(o.Value("title")), 100)))
		}
		visible := true
		if e.Infohub != nil {
			if v, ok := e.Infohub.Get("visible"); ok {
				visible = pyjson.Truthy(v)
			}
		}
		edges := toList(data.Value("edges"))
		return pyjson.NewObject("path", data.Value("path"), "visible_in_ui", visible, "filters", infohubFilters(e.Infohub),
			"total", data.Value("total"), "matched_count", data.Value("matched_count"), "context_count", data.Value("context_count"),
			"kinds", data.Value("kinds"), "nodes", nodes, "topics", topics, "shown", int64(len(nodes)),
			"topics_shown", int64(len(topics)), "edges", int64(len(edges))), nil
	case "search_infohub":
		src, err := e.openInfohub()
		if err != nil {
			return nil, err
		}
		defer src.Close()
		days, err := intArg(args.Value("days"), 0)
		if err != nil {
			return nil, err
		}
		limit, err := intArg(args.Value("limit"), 20)
		if err != nil {
			return nil, err
		}
		data, err := src.Browse(e.Ctx, e.Store, infohub.Filter{Q: orEmpty(args.Value("query")), Kind: orEmpty(args.Value("kind")),
			Tags: orEmpty(args.Value("tags")), Status: orEmpty(args.Value("status")), Days: float64(days),
			Order: "activity", Limit: clampInt(limit, 1, 100)})
		if err != nil {
			return nil, err
		}
		list := []any{}
		for _, it := range head(orV(data.Value("items"), []any{}), 1<<30) {
			o := obj(it)
			list = append(list, pyjson.NewObject("key", o.Value("key"), "kind", o.Value("kind"), "title", o.Value("title"),
				"status", o.Value("status"), "summary", pytext.Prefix(orEmpty(o.Value("summary")), 300),
				"tags", head(orV(o.Value("tags"), []any{}), 8)))
		}
		return pyjson.NewObject("total", data.Value("total"), "items", list), nil
	case "get_infohub_unit":
		src, err := e.openInfohub()
		if err != nil {
			return nil, err
		}
		defer src.Close()
		unit, err := src.Unit(e.Ctx, orEmpty(args.Value("key")), e.Store)
		if err != nil {
			return nil, err
		}
		messages := []any{}
		for _, m := range tail(orV(unit.Value("messages"), []any{}), 20) {
			o := obj(m)
			messages = append(messages, pyjson.NewObject("sender", orV(o.Value("sender_name"), o.Value("sender")),
				"text", pytext.Prefix(orEmpty(o.Value("text")), 400)))
		}
		return pyjson.NewObject("key", unit.Value("key"), "kind", unit.Value("kind"), "title", unit.Value("title"),
			"summary", unit.Value("summary"), "status", unit.Value("status"), "tags", unit.Value("tags"),
			"parents", unit.Value("parents"), "children", unit.Value("children"), "messages", messages,
			"n_messages", unit.Value("n_messages"), "imported", unit.Value("imported")), nil
	}
	return nil, nil
}
