package store

import (
	"context"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// defaultTopics is Store.DEFAULT_TOPICS: (meta "default" key, title, type).
var defaultTopics = [][3]string{
	{"story", "Story", "story"},
	{"related", "Related work", "related_work"},
	{"method", "Method", "method"},
	{"experiment", "Experiment", "experiment"},
}

// EnsureDefaultTopics is Store.ensure_default_topics: create the built-in
// topics once per project (an archived one does not come back).
func (s *Store) EnsureDefaultTopics(ctx context.Context, who Who) ([]*pyjson.Object, error) {
	done := false
	present := map[string]bool{}
	err := s.read(ctx, func(q sqlitedb.Querier) error {
		v, _, err := MetaGet(ctx, q, DefaultTopicsFlag)
		if err != nil || v == "1" {
			done = v == "1"
			return err
		}
		rows, err := sqlitedb.Query(ctx, q, "SELECT meta_json FROM units WHERE type='topic'")
		if err != nil {
			return err
		}
		for _, r := range rows {
			key := parseDetail(r.Get("meta_json")).Value("default")
			if !pyjson.Truthy(key) {
				key = ""
			}
			present[pyjson.Str(key)] = true
		}
		return nil
	})
	if err != nil || done {
		return nil, err
	}
	var created []*pyjson.Object
	for _, t := range defaultTopics {
		if present[t[0]] {
			continue
		}
		unit, err := s.CreateUnit(ctx, pyjson.NewObject(
			"type", "topic", "title", t[1], "status", "active", "importance", "p1",
			"origin", "system", "assignee", nil, "meta", pyjson.NewObject("default", t[0]),
			"design", pyjson.NewObject("rule", pyjson.NewObject("types", []any{t[2]})),
		), "system:ark", who)
		if err != nil {
			return created, err
		}
		created = append(created, unit)
	}
	return created, s.SetMeta(ctx, DefaultTopicsFlag, "1")
}
