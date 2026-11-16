package server

import (
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/search"
)

// Workbench Search: GET /api/search/nodes and GET /api/workspace/grep, as a
// plain JSON reply or (stream=1) NDJSON lines.

// searchOptions is _search_options: the case / word / regex flags.
func (c *reqCtx) searchOptions() search.Options {
	flag := func(name string) bool { return pyBoolArg(c.query.First(name, "0")) }
	return search.Options{Case: flag("case"), Word: flag("word"), Regex: flag("regex")}
}

func searchErr(err error) error {
	var se *search.Error
	var ie *search.InternalError
	switch {
	case errors.As(err, &se):
		return apiErr(400, se.Message, "")
	case errors.As(err, &ie):
		return apiErr(500, "Internal error: "+ie.Message, "")
	}
	return err
}

func (c *reqCtx) searchNodes() error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	text, o := c.query.First("q", ""), c.searchOptions()
	if pyBoolArg(c.query.First("stream", "0")) {
		if _, err := search.BuildMatcher(text, o); err != nil {
			return searchErr(err)
		}
		return c.streamSearch("node", func(cancelled func() bool, emit search.Emit) (*pyjson.Object, error) {
			return search.IterNodes(c.r.Context(), p.Store.DB, text, o, cancelled, emit)
		})
	}
	out, err := search.Nodes(c.ctx, p.Store.DB, text, o)
	if err != nil {
		return searchErr(err)
	}
	c.ok(out)
	return nil
}

func (c *reqCtx) workspaceGrep(root string) error {
	text, o := c.query.First("q", ""), c.searchOptions()
	include, exclude := c.query.First("include", ""), c.query.First("exclude", "")
	if pyBoolArg(c.query.First("stream", "0")) {
		if _, err := search.BuildMatcher(text, o); err != nil {
			return searchErr(err)
		}
		return c.streamSearch("file", func(cancelled func() bool, emit search.Emit) (*pyjson.Object, error) {
			return search.IterFiles(root, text, o, include, exclude, cancelled, emit)
		})
	}
	out, err := search.Files(root, text, o, include, exclude)
	if err != nil {
		return searchErr(err)
	}
	c.ok(out)
	return nil
}

// searchRuns is _SEARCH_RUNS: the newest run token per client search id
// (insertion-ordered like a dict, at most 500 ids).
var searchRuns = struct {
	sync.Mutex
	tokens map[string]int64
	order  []string
	seq    int64
}{tokens: map[string]int64{}}

func searchRunSet(sid string) int64 {
	searchRuns.Lock()
	defer searchRuns.Unlock()
	searchRuns.seq++
	token := searchRuns.seq
	if sid == "" {
		return token
	}
	if _, ok := searchRuns.tokens[sid]; !ok {
		searchRuns.order = append(searchRuns.order, sid)
	}
	searchRuns.tokens[sid] = token
	for len(searchRuns.tokens) > 500 {
		oldest := searchRuns.order[0]
		searchRuns.order = searchRuns.order[1:]
		delete(searchRuns.tokens, oldest)
	}
	return token
}

func searchRunCurrent(sid string, token int64) bool {
	searchRuns.Lock()
	defer searchRuns.Unlock()
	return searchRuns.tokens[sid] == token
}

func searchRunDone(sid string, token int64) {
	searchRuns.Lock()
	defer searchRuns.Unlock()
	if searchRuns.tokens[sid] == token {
		delete(searchRuns.tokens, sid)
		for i, s := range searchRuns.order {
			if s == sid {
				searchRuns.order = append(searchRuns.order[:i], searchRuns.order[i+1:]...)
				break
			}
		}
	}
}

// streamSearch is Handler._stream_search: one {"type": kind, kind: item}
// line per result, then {"type": "done", ...summary, "cancelled": bool}.
// A newer request with the same sid or the client leaving stops the scan.
func (c *reqCtx) streamSearch(kind string, run func(cancelled func() bool, emit search.Emit) (*pyjson.Object, error)) error {
	sid := pytext.Prefix(c.query.First("sid", ""), 80)
	token := searchRunSet(sid)
	defer func() {
		if sid != "" {
			searchRunDone(sid, token)
		}
	}()
	c.detachFromRequestLimits()
	clientDone := c.r.Context().Done()
	var checked time.Time
	gone, isCancelled := false, false
	cancelled := func() bool {
		if sid != "" && !searchRunCurrent(sid, token) {
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
	h := c.w.Header()
	h.Set("Content-Type", "application/x-ndjson; charset=utf-8")
	extra := headerList{{"X-Accel-Buffering", "no"}, {"Connection", "close"}}
	gz := AcceptsGzip(c.header("Accept-Encoding"))
	if gz {
		extra = append(extra, [2]string{"Content-Encoding", "gzip"}, [2]string{"Vary", "Accept-Encoding"})
	}
	securityHeaders(h, extra)
	c.w.WriteHeader(200)
	rc := http.NewResponseController(c.w)
	var out io.Writer = c.w
	var zw *gzip.Writer
	if gz {
		zw, _ = gzip.NewWriterLevel(c.w, 5)
		out = zw
	}
	failed := false
	write := func(event *pyjson.Object) bool {
		if failed {
			return false
		}
		rc.SetWriteDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
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
		if err := rc.Flush(); err != nil {
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
		var se *search.Error
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
