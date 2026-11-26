// Package search is the HTTP side of Workbench Search: GET /api/search/nodes
// and GET /api/workspace/grep, as a plain JSON reply or (stream=1) NDJSON
// lines (the matching branches of arkstudio/server.py over internal/search).
// internal/server registers it (router.go, routes_workspace.go).
package search

import (
	"compress/gzip"
	"errors"
	"io"
	"sync"
	"time"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	dom "arkstudio/go/internal/search"
)

// Options is _search_options: the case / word / regex flags.
func Options(c api.Ctx) dom.Options {
	flag := func(name string) bool { return api.BoolArg(c.Query().First(name, "0")) }
	return dom.Options{Case: flag("case"), Word: flag("word"), Regex: flag("regex")}
}

// Err maps the search package errors like do_GET.
func Err(err error) error {
	var se *dom.Error
	var ie *dom.InternalError
	switch {
	case errors.As(err, &se):
		return api.Err(400, se.Message, "")
	case errors.As(err, &ie):
		return api.Err(500, "Internal error: "+ie.Message, "")
	}
	return err
}

// Nodes answers GET /api/search/nodes.
func Nodes(c api.Ctx) error {
	p, err := c.Require("read")
	if err != nil {
		return err
	}
	q := c.Query()
	text, o := q.First("q", ""), Options(c)
	if api.BoolArg(q.First("stream", "0")) {
		if _, err := dom.BuildMatcher(text, o); err != nil {
			return Err(err)
		}
		return Stream(c, "node", func(cancelled func() bool, emit dom.Emit) (*pyjson.Object, error) {
			return dom.IterNodes(c.RequestContext(), p.Store.DB, text, o, cancelled, emit)
		})
	}
	out, err := dom.Nodes(c.Context(), p.Store.DB, text, o)
	if err != nil {
		return Err(err)
	}
	c.OK(out)
	return nil
}

// Grep answers GET /api/workspace/grep for the workspace root.
func Grep(c api.Ctx, root string) error {
	q := c.Query()
	text, o := q.First("q", ""), Options(c)
	include, exclude := q.First("include", ""), q.First("exclude", "")
	if api.BoolArg(q.First("stream", "0")) {
		if _, err := dom.BuildMatcher(text, o); err != nil {
			return Err(err)
		}
		return Stream(c, "file", func(cancelled func() bool, emit dom.Emit) (*pyjson.Object, error) {
			return dom.IterFiles(root, text, o, include, exclude, cancelled, emit)
		})
	}
	out, err := dom.Files(root, text, o, include, exclude)
	if err != nil {
		return Err(err)
	}
	c.OK(out)
	return nil
}

// runs is _SEARCH_RUNS: the newest run token per client search id
// (insertion-ordered like a dict, at most 500 ids).
type runs struct {
	sync.Mutex
	tokens map[string]int64
	order  []string
	seq    int64
}

var searchRuns = &runs{tokens: map[string]int64{}}

func (r *runs) set(sid string) int64 {
	r.Lock()
	defer r.Unlock()
	r.seq++
	token := r.seq
	if sid == "" {
		return token
	}
	if _, ok := r.tokens[sid]; !ok {
		r.order = append(r.order, sid)
	}
	r.tokens[sid] = token
	for len(r.tokens) > 500 {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.tokens, oldest)
	}
	return token
}

func (r *runs) current(sid string, token int64) bool {
	r.Lock()
	defer r.Unlock()
	return r.tokens[sid] == token
}

func (r *runs) done(sid string, token int64) {
	r.Lock()
	defer r.Unlock()
	if r.tokens[sid] == token {
		delete(r.tokens, sid)
		for i, s := range r.order {
			if s == sid {
				r.order = append(r.order[:i], r.order[i+1:]...)
				break
			}
		}
	}
}

// Stream is Handler._stream_search: one {"type": kind, kind: item} line per
// result, then {"type": "done", ...summary, "cancelled": bool}.  A newer
// request with the same sid or the client leaving stops the scan.
func Stream(c api.Ctx, kind string, run func(cancelled func() bool, emit dom.Emit) (*pyjson.Object, error)) error {
	sid := pytext.Prefix(c.Query().First("sid", ""), 80)
	token := searchRuns.set(sid)
	defer func() {
		if sid != "" {
			searchRuns.done(sid, token)
		}
	}()
	c.DetachFromRequestLimits()
	clientDone := c.RequestContext().Done()
	var checked time.Time
	gone, isCancelled := false, false
	cancelled := func() bool {
		if sid != "" && !searchRuns.current(sid, token) {
			isCancelled = true
		} else {
			if time.Since(checked) >= 50*time.Millisecond {
				checked = time.Now()
				select {
				case <-clientDone:
					gone = true
				default:
				}
			}
			isCancelled = gone
		}
		return isCancelled
	}
	extra := [][2]string{{"X-Accel-Buffering", "no"}, {"Connection", "close"}}
	gz := api.AcceptsGzip(c.Header("Accept-Encoding"))
	if gz {
		extra = append(extra, [2]string{"Content-Encoding", "gzip"}, [2]string{"Vary", "Accept-Encoding"})
	}
	st := c.StartStream("application/x-ndjson; charset=utf-8", extra)
	var out io.Writer = st
	var zw *gzip.Writer
	if gz {
		zw, _ = gzip.NewWriterLevel(st, 5)
		out = zw
	}
	failed := false
	write := func(event *pyjson.Object) bool {
		if failed {
			return false
		}
		st.SetWriteDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
		line := append(pyjson.Dumps(event), '\n')
		if _, err := out.Write(line); err != nil {
			failed = true
			return false
		}
		if zw != nil {
			if err := zw.Flush(); err != nil {
				failed = true
				return false
			}
		}
		if err := st.Flush(); err != nil {
			failed = true
			return false
		}
		return true
	}
	summary, err := run(cancelled, func(item *pyjson.Object) bool {
		return write(pyjson.NewObject("type", kind, kind, item))
	})
	switch {
	case failed || (err == nil && summary == nil):
		return nil
	case err != nil:
		msg := err.Error()
		var se *dom.Error
		if errors.As(err, &se) {
			msg = se.Message
		}
		write(pyjson.NewObject("type", "error", "message", msg))
	default:
		done := pyjson.NewObject("type", "done")
		done.Update(summary)
		done.Set("cancelled", isCancelled)
		write(done)
	}
	if zw != nil && !failed {
		zw.Close() //nolint:errcheck
	}
	return nil
}

// Replace answers POST /api/workspace/replace: a preview (default) or, with
// "apply": true and the previewed "files" ([{path, hash}]), the rewrite.
// Changing files needs write access to the project.
func Replace(c api.Ctx, root string, body *pyjson.Object) error {
	if _, err := c.Require("write"); err != nil {
		return err
	}
	str := func(key string) string {
		if v, ok := body.Value(key).(string); ok {
			return v
		}
		return ""
	}
	o := dom.Options{Case: api.BoolArg(body.Value("case")), Word: api.BoolArg(body.Value("word")), Regex: api.BoolArg(body.Value("regex"))}
	if _, ok := body.Value("replace").(string); !ok {
		return api.Err(400, "replace must be a string", "")
	}
	if !api.BoolArg(body.Value("apply")) {
		out, err := dom.ReplacePreview(root, str("q"), o, str("include"), str("exclude"), str("replace"))
		if err != nil {
			return Err(err)
		}
		c.OK(out)
		return nil
	}
	list, ok := body.Value("files").([]any)
	if !ok {
		return api.Err(400, "files must be a list of {path, hash}", "")
	}
	refs := make([]dom.FileRef, 0, len(list))
	for _, item := range list {
		o, ok := item.(*pyjson.Object)
		if !ok {
			return api.Err(400, "files must be a list of {path, hash}", "")
		}
		path, _ := o.Value("path").(string)
		hash, _ := o.Value("hash").(string)
		if path == "" {
			return api.Err(400, "files must be a list of {path, hash}", "")
		}
		refs = append(refs, dom.FileRef{Path: path, Hash: hash})
	}
	out, err := dom.ReplaceApply(root, str("q"), o, str("replace"), refs)
	if err != nil {
		return Err(err)
	}
	c.OK(out)
	return nil
}
