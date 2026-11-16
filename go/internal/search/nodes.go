package search

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"time"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

var internalTag = regexp.MustCompile(`\A(type|status|importance):`)

type nodeField struct {
	field, text string
	extra       *pyjson.Object
}

type nodeRow struct {
	head   *pyjson.Object
	fields []nodeField
}

// nodeRows is _node_rows: non-archived nodes, most recently updated first,
// with their searchable fields.
func nodeRows(ctx context.Context, db *sqlitedb.DB) ([]nodeRow, error) {
	type unit struct {
		id                    int64
		uid, typ, title, body sql.NullString
		status                any
	}
	type comment struct {
		id           int64
		author, body sql.NullString
	}
	var units []unit
	tags := map[int64][]string{}
	comments := map[int64][]comment{}
	err := db.Read(ctx, func(q sqlitedb.Querier) error {
		rows, err := q.QueryContext(ctx, "SELECT id, uid, type, title, body, status FROM units WHERE archived=0 ORDER BY updated_at DESC")
		if err != nil {
			return err
		}
		for rows.Next() {
			var u unit
			if err := rows.Scan(&u.id, &u.uid, &u.typ, &u.title, &u.body, &u.status); err != nil {
				rows.Close()
				return err
			}
			if b, ok := u.status.([]byte); ok {
				u.status = string(b)
			}
			units = append(units, u)
		}
		rows.Close()
		if rows, err = q.QueryContext(ctx, "SELECT unit_id, tag FROM unit_tags"); err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var tag string
			if err := rows.Scan(&id, &tag); err != nil {
				rows.Close()
				return err
			}
			tags[id] = append(tags[id], tag)
		}
		rows.Close()
		if rows, err = q.QueryContext(ctx, "SELECT id, unit_id, author, body FROM unit_comments ORDER BY created_at"); err != nil {
			return err
		}
		for rows.Next() {
			var c comment
			var unitID int64
			if err := rows.Scan(&c.id, &unitID, &c.author, &c.body); err != nil {
				rows.Close()
				return err
			}
			comments[unitID] = append(comments[unitID], c)
		}
		rows.Close()
		return nil
	})
	if err != nil {
		return nil, err
	}
	nullable := func(s sql.NullString) any {
		if !s.Valid {
			return nil
		}
		return s.String
	}
	out := make([]nodeRow, 0, len(units))
	for _, u := range units {
		fields := []nodeField{{"title", u.title.String, nil}, {"body", u.body.String, nil}}
		var visible []string
		for _, t := range tags[u.id] {
			if !internalTag.MatchString(t) {
				visible = append(visible, t)
			}
		}
		if len(visible) > 0 {
			fields = append(fields, nodeField{"tags", strings.Join(visible, " · "), nil})
		}
		for _, c := range comments[u.id] {
			fields = append(fields, nodeField{"comment", c.body.String,
				pyjson.NewObject("comment_id", c.id, "author", nullable(c.author))})
		}
		head := pyjson.NewObject("id", u.id, "uid", nullable(u.uid), "type", nullable(u.typ),
			"title", nullable(u.title), "status", u.status)
		out = append(out, nodeRow{head, fields})
	}
	return out, nil
}

// IterNodes is iter_search_nodes: emits each matching node and returns the
// summary (nil when emit stopped the scan).
func IterNodes(ctx context.Context, db *sqlitedb.DB, query string, o Options, cancelled func() bool, emit Emit) (*pyjson.Object, error) {
	re, err := BuildMatcher(query, o)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	rows, err := nodeRows(ctx, db)
	if err != nil {
		return nil, err
	}
	budget := stepBudget
	total, found := 0, 0
	truncated, timedOut := false, false
	for _, row := range rows {
		if cancelled != nil && cancelled() {
			truncated = true
			break
		}
		if time.Since(started) >= TimeLimit || budget <= 0 {
			timedOut, truncated = true, true
			break
		}
		if found >= MaxItems || total >= MaxResults {
			truncated = true
			break
		}
		var lines []any
		left := min(MaxPerItem, MaxResults-total)
		var failed error
		for _, f := range row.fields {
			if left <= 0 || f.text == "" {
				continue
			}
			hits, _, err := lineMatches(f.text, re, left, &budget)
			if err != nil {
				failed = err
				break
			}
			for _, h := range hits {
				item := pyjson.NewObject("field", f.field)
				item.Update(h.object())
				if f.extra != nil {
					item.Update(f.extra)
				}
				lines = append(lines, item)
			}
			left -= len(hits)
		}
		if failed != nil {
			timedOut, truncated = true, true
			break
		}
		if len(lines) > 0 {
			total += len(lines)
			found++
			node := row.head.Copy()
			node.Set("matches", lines)
			if !emit(node) {
				return nil, nil
			}
		}
	}
	return pyjson.NewObject("query", query, "total", total, "truncated", truncated,
		"timed_out", timedOut, "elapsed_ms", elapsedMS(started)), nil
}

// Nodes is search_nodes: the plain (non-streamed) reply.
func Nodes(ctx context.Context, db *sqlitedb.DB, query string, o Options) (*pyjson.Object, error) {
	nodes := []any{}
	summary, err := IterNodes(ctx, db, query, o, nil, func(item *pyjson.Object) bool {
		nodes = append(nodes, item)
		return true
	})
	if err != nil {
		return nil, err
	}
	out := pyjson.NewObject("query", summary.Value("query"), "nodes", nodes)
	for _, k := range []string{"total", "truncated", "timed_out", "elapsed_ms"} {
		out.Set(k, summary.Value(k))
	}
	return out, nil
}
