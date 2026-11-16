package store

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strings"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/sqlitedb"
)

// Port of assistant.context(store) and assistant.insights(store).

func sid(prefix string, parts ...any) string {
	strs := make([]string, len(parts))
	for i, p := range parts {
		strs[i] = pyjson.Str(p)
	}
	sum := sha1.Sum([]byte(strings.Join(strs, "|")))
	return prefix + "-" + hex.EncodeToString(sum[:])[:10]
}

// freeTopics are the values of a unit's "topic:" tags.  Python keeps them in
// a set, whose iteration order is hash-dependent; units here have at most one
// topic tag, and several are visited in sorted order.
func freeTopics(u *pyjson.Object) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range u.Value("tags").([]string) {
		if TagNamespace(t) == "topic" {
			v := TagValue(t)
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	sort.Strings(out)
	return out
}

type link struct {
	other    int64
	relation string
}

// Insights is assistant.insights.
func (s *Store) Insights(ctx context.Context) ([]any, error) {
	var units []*pyjson.Object
	var links []sqlitedb.Row
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		if units, err = s.unitsWithTags(ctx, q); err != nil {
			return err
		}
		links, err = sqlitedb.Query(ctx, q, "SELECT source_id,target_id,relation FROM unit_links")
		return err
	})
	if err != nil {
		return nil, err
	}
	linked := map[int64]bool{}
	pairs := map[[2]int64]bool{}
	incoming, outgoing := map[int64][]link{}, map[int64][]link{}
	for _, r := range links {
		a, b := r.Int("source_id"), r.Int("target_id")
		linked[a], linked[b] = true, true
		pairs[[2]int64{min(a, b), max(a, b)}] = true
		outgoing[a] = append(outgoing[a], link{b, r.Str("relation")})
		incoming[b] = append(incoming[b], link{a, r.Str("relation")})
	}
	id := func(u *pyjson.Object) int64 { return u.Value("id").(int64) }
	str := func(u *pyjson.Object, k string) string { return pyjson.Str(u.Value(k)) }
	var suggestions []*pyjson.Object
	simple := func(prefix, kind, severity, title, detail string, u *pyjson.Object, action *pyjson.Object) {
		suggestions = append(suggestions, pyjson.NewObject("id", sid(prefix, id(u)), "kind", kind, "severity", severity,
			"title", title, "detail", detail, "unit_ids", []any{id(u)}, "ops", []any{}, "action", action))
	}

	// 1. Ideas/questions/claims sharing a topic without an edge.
	byTopic := map[string][]*pyjson.Object{}
	var topicOrder []string
	for _, u := range units {
		switch str(u, "type") {
		case "idea", "question", "claim":
			for _, v := range freeTopics(u) {
				if _, ok := byTopic[v]; !ok {
					topicOrder = append(topicOrder, v)
				}
				byTopic[v] = append(byTopic[v], u)
			}
		}
	}
	seenPairs := map[[2]int64]bool{}
outer:
	for _, topic := range topicOrder {
		group := byTopic[topic]
		if len(group) < 2 {
			continue
		}
		for i, left := range group {
			for _, right := range group[i+1:] {
				key := [2]int64{min(id(left), id(right)), max(id(left), id(right))}
				if pairs[key] || seenPairs[key] {
					continue
				}
				rset := map[string]bool{}
				for _, v := range freeTopics(right) {
					rset[v] = true
				}
				var shared []string
				for _, v := range freeTopics(left) {
					if rset[v] {
						shared = append(shared, v)
					}
				}
				if len(shared) == 0 {
					continue
				}
				seenPairs[key] = true
				suggestions = append(suggestions, pyjson.NewObject(
					"id", sid("share", key[0], key[1]), "kind", "shared-topic", "severity", "low",
					"title", "Connect “"+str(left, "title")+"” and “"+str(right, "title")+"”",
					"detail", "They share "+strings.Join(shared, ", ")+" and have no edge yet.",
					"unit_ids", []any{id(left), id(right)},
					"ops", []any{pyjson.NewObject("op", "link", "source", id(left), "target", id(right), "relation", "relates_to")},
					"action", pyjson.NewObject("tab", "ideas", "unit_id", id(left))))
				if len(suggestions) >= 3 {
					break outer
				}
			}
		}
	}
	has := func(list []link, rels ...string) bool {
		for _, l := range list {
			for _, r := range rels {
				if l.relation == r {
					return true
				}
			}
		}
		return false
	}
	for _, u := range units {
		if str(u, "type") == "idea" && !linked[id(u)] {
			simple("orphan", "orphan-idea", "medium", "Unlinked idea: "+str(u, "title"),
				"No edge enters or leaves this idea. Connect it to a question, a claim, or an experiment.", u,
				pyjson.NewObject("tab", "ideas", "unit_id", id(u)))
		}
	}
	for _, u := range units {
		if str(u, "type") != "claim" {
			continue
		}
		in := incoming[id(u)]
		action := pyjson.NewObject("tab", "map", "unit_id", id(u))
		if !has(in, "supports", "validates", "contradicts") {
			simple("claim-evidence", "claim-no-evidence", "high", "Claim lacks evidence: "+str(u, "title"),
				"No evidence or experiment points at this claim. Link one before promoting it.", u, action)
		} else if !has(in, "validates", "targets") {
			simple("claim-exp", "claim-no-experiment", "medium", "Claim has evidence but no experiment: "+str(u, "title"),
				"An experiment should validate the claim, or the evidence needs a design record.", u, action)
		}
	}
	for _, u := range units {
		st := str(u, "status")
		if str(u, "type") != "task" || (st != "open" && st != "active" && st != "review") {
			continue
		}
		if !has(outgoing[id(u)], "assigned_to") {
			simple("unassigned", "task-unassigned", "medium", "Task has no agent: "+str(u, "title"),
				"Assign an owner before dispatch; otherwise the handoff has no accountability.", u,
				pyjson.NewObject("tab", "tasks", "unit_id", id(u)))
		}
	}
	for _, u := range units {
		st := str(u, "status")
		if str(u, "type") != "experiment" || (st != "active" && st != "open") {
			continue
		}
		if !has(incoming[id(u)], "part_of") {
			simple("exp-task", "experiment-no-task", "medium", "Experiment has no task: "+str(u, "title"),
				"An active experiment needs at least one task that can be scheduled and stopped.", u,
				pyjson.NewObject("tab", "plan", "sub", "experiment", "unit_id", id(u)))
		}
	}
	for _, u := range units {
		if str(u, "status") != "blocked" {
			continue
		}
		if !has(incoming[id(u)], "blocks", "depends_on") && !has(outgoing[id(u)], "blocks", "depends_on") {
			simple("blocked", "blocked-no-reason", "high", "Blocked without a blocker: "+str(u, "title"),
				"Add the task or decision that must clear before this can move.", u,
				pyjson.NewObject("tab", "map", "unit_id", id(u)))
		}
	}
	severity := map[string]int{"high": 0, "medium": 1, "low": 2}
	sort.SliceStable(suggestions, func(i, j int) bool {
		a, b := suggestions[i], suggestions[j]
		sa, sb := severity[str(a, "severity")], severity[str(b, "severity")]
		if sa != sb {
			return sa < sb
		}
		return str(a, "kind") < str(b, "kind")
	})
	out := []any{}
	for i, sg := range suggestions {
		if i == 18 {
			break
		}
		out = append(out, sg)
	}
	return out, nil
}

// AgentContext is assistant.context(store).
func (s *Store) AgentContext(ctx context.Context) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		units, err := s.unitsWithTags(ctx, q)
		if err != nil {
			return err
		}
		stats, err := s.stats(ctx, q)
		if err != nil {
			return err
		}
		activity, err := activityRows(ctx, q, 15, nil)
		if err != nil {
			return err
		}
		tasks, exps, claims := []any{}, []any{}, []any{}
		for _, u := range units {
			t, st := pyjson.Str(u.Value("type")), pyjson.Str(u.Value("status"))
			switch {
			case t == "task" && (st == "open" || st == "active" || st == "review") && len(tasks) < 24:
				assignees := []any{}
				for _, a := range u.Value("assignees").([]any) {
					assignees = append(assignees, a.(*pyjson.Object).Value("uid"))
				}
				tasks = append(tasks, pyjson.NewObject("uid", u.Value("uid"), "title", u.Value("title"), "status", st,
					"importance", u.Value("importance"), "end_at", u.Value("end_at"), "assignees", assignees))
			case t == "experiment" && (st == "open" || st == "active") && len(exps) < 16:
				exps = append(exps, pyjson.NewObject("uid", u.Value("uid"), "title", u.Value("title"), "status", st,
					"end_at", u.Value("end_at")))
			case t == "claim" && (st == "open" || st == "active") && len(claims) < 16:
				claims = append(claims, pyjson.NewObject("uid", u.Value("uid"), "title", u.Value("title"), "status", st,
					"importance", u.Value("importance")))
			}
		}
		out = pyjson.NewObject("project", s.Manifest.Snapshot(), "counts", stats, "active_tasks", tasks,
			"active_experiments", exps, "open_claims", claims, "activity", activity,
			"links", "GET /api/agent/v1/units?q=&type=&limit=100",
			"write_contract", pyjson.NewObject(
				"create", "POST /api/agent/v1/units", "update", "PATCH /api/agent/v1/units/{id}",
				"link", "POST /api/agent/v1/links",
				"comment_reply", "POST /api/agent/v1/units/{id}/comments {body, reply_to}",
				"comment_resolve", "POST /api/agent/v1/units/{id}/comments/{comment_id}/resolve",
				"auth", "Authorization: Bearer <agent token>"))
		return nil
	})
	return out, err
}

// TaskContextUnits is the unit/link part of App.task_context.
func (s *Store) TaskContextUnits(ctx context.Context) ([]any, []any, *pyjson.Object, error) {
	var units, links []any
	var stats *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		listing, err := s.listUnits(ctx, q, Filters{Limit: 1000, Sort: "updated"}, false)
		if err != nil {
			return err
		}
		graph, err := s.graph(ctx, q, Filters{}, false, 1200, false)
		if err != nil {
			return err
		}
		for _, u := range listing.Value("items").([]*pyjson.Object) {
			body := strOrEmpty(u.Value("body"))
			tags := u.Value("tags")
			slim := pyjson.NewObject("id", u.Value("id"), "uid", u.Value("uid"), "type", u.Value("type"),
				"title", u.Value("title"), "body", prefix600(body), "status", u.Value("status"),
				"importance", u.Value("importance"), "tags", tags, "start_at", u.Value("start_at"),
				"end_at", u.Value("end_at"), "origin", u.Value("origin"), "source_ref", u.Value("source_ref"),
				"updated_at", u.Value("updated_at"), "comments_open", u.Value("comments_open"))
			if n, _ := u.Value("comments_open").(int64); n != 0 {
				comments, err := commentsOf(ctx, q, u.Value("id").(int64))
				if err != nil {
					return err
				}
				open := []any{}
				for _, c := range comments {
					co := c.(*pyjson.Object)
					if co.Value("author_kind") == "user" && co.Value("resolved") == false {
						open = append(open, pyjson.NewObject("id", co.Value("id"), "kind", co.Value("kind"),
							"author", co.Value("author"), "created_at", co.Value("created_at"),
							"body", prefix600(pyjson.Str(co.Value("body")))))
					}
				}
				slim.Set("open_comments", open)
			}
			units = append(units, slim)
		}
		for _, e := range graph.Value("edges").([]any) {
			eo := e.(*pyjson.Object)
			links = append(links, pyjson.NewObject("source", eo.Value("source"), "target", eo.Value("target"),
				"relation", eo.Value("relation"), "note", eo.Value("note")))
		}
		stats, err = s.stats(ctx, q)
		return err
	})
	if units == nil {
		units = []any{}
	}
	if links == nil {
		links = []any{}
	}
	return units, links, stats, err
}

// GraphEdges is store.graph({"limit": 1200})["edges"].
func (s *Store) GraphEdges(ctx context.Context) ([]any, error) {
	var edges []any
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		g, err := s.graph(ctx, q, Filters{}, false, 1200, false)
		if err == nil {
			edges = g.Value("edges").([]any)
		}
		return err
	})
	return edges, err
}

// Stats is Store.stats().
func (s *Store) Stats(ctx context.Context) (*pyjson.Object, error) {
	var out *pyjson.Object
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		out, err = s.stats(ctx, q)
		return err
	})
	return out, err
}

func prefix600(s string) string { return pytext.Prefix(s, 600) }
