package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strings"
)

// request is one HTTP call an op decided to make.  An empty name means the
// op's name and kind; a set name (topic_add, or the unit_get before a node
// edit) is reported under that name with its own write flag.  next, when
// set, gets the outcome class and the response body and may return a
// follow-up request (a node edit first reads the unit, as the web client
// has it loaded).
type request struct {
	name   string
	write  bool
	method string
	path   string
	body   string
	next   func(class string, body []byte) *request
}

type op struct {
	name   string
	write  bool
	weight float64 // probability of choosing this op, set by buildMix
	build  func(u *vuser) request
}

func get(path string) request { return request{method: "GET", path: path} }

// Reads: the phase 1/2 mix (relative weights 25:20:20:8:7:7:5).
var readOps = []struct {
	op
	base float64
}{
	{op{name: "units_list", build: func(u *vuser) request {
		return get([]string{"/api/units?slim=1&limit=50", "/api/units?slim=1&limit=50&sort=title&facets=0",
			"/api/units?q=" + url.QueryEscape("负载") + "&slim=1&limit=50", "/api/units?statuses=review&slim=1"}[rand.IntN(4)])
	}}, 25},
	{op{name: "unit_get", build: func(u *vuser) request { return get("/api/units/" + u.pick()) }}, 20},
	{op{name: "revision", build: func(u *vuser) request { return get("/api/revision") }}, 20},
	{op{name: "project_home", build: func(u *vuser) request { return get("/api/project") }}, 8},
	{op{name: "graph_snapshot", build: func(u *vuser) request { return get("/api/graph/snapshot") }}, 7},
	{op{name: "activity", build: func(u *vuser) request { return get("/api/activity?limit=40") }}, 7},
	{op{name: "comments_get", build: func(u *vuser) request { return get("/api/units/" + u.pick() + "/comments") }}, 5},
}

// Writes, with the bodies web/js sends (api.js, unit-edit.js, topics.js):
// comment POST, node edit PATCH /api/units/<uid> (after GET of the unit; the
// free tags are kept, as the inspector's draft keeps them), the topic canvas
// layout save PATCH /api/units/<topic>/members {"items": [{unit, x, y}]}, and
// topic design: POST .../members {"add": [uid]} / PATCH {"remove": [uid]}.
// None of these routes takes a revision precondition (the web client sends
// none); a 409 is still counted as a conflict, not as an error.
// extraReads is the number of reads an op sends besides its write.
var writeOps = []struct {
	op
	base       float64
	topics     bool // needs topics
	extraReads float64
}{
	{op{name: "comment_post", write: true, build: func(u *vuser) request {
		return request{method: "POST", path: "/api/units/" + u.pick() + "/comments",
			body: jsonBody(map[string]any{"body": fmt.Sprintf("load comment %d from %s 负载", rand.IntN(1e9), u.name)})}
	}}, 40, false, 0},
	{op{name: "unit_patch", write: true, build: func(u *vuser) request {
		uid := u.pickEditable()
		rq := get("/api/units/" + uid)
		rq.name = "unit_get"
		rq.next = func(class string, body []byte) *request {
			if class != "ok" {
				return nil
			}
			var detail struct {
				Unit struct {
					Tags []string `json:"tags"`
				} `json:"unit"`
			}
			json.Unmarshal(body, &detail) //nolint:errcheck // a bad body just drops the free tags
			n := rand.IntN(1e9)
			return &request{name: "unit_patch", write: true, method: "PATCH", path: "/api/units/" + uid,
				body: jsonBody(map[string]any{
					"title":  fmt.Sprintf("负载 edited node %d", n),
					"body":   fmt.Sprintf("Edited by %s during the load test.\n\n- item %d\n- 负载 **bold**", u.name, n),
					"status": []string{"inbox", "open", "active", "review", "blocked", "done"}[rand.IntN(6)],
					"tags":   withTag(freeTags(detail.Unit.Tags), "load"),
				})}
		}
		return rq
	}}, 30, false, 1},
	{op{name: "topic_layout", write: true, build: func(u *vuser) request {
		t := u.topicWithMembers()
		k := 1 + rand.IntN(min(5, len(t.members)))
		items := make([]map[string]any, 0, k)
		for _, i := range rand.Perm(len(t.members))[:k] {
			items = append(items, map[string]any{"unit": t.members[i],
				"x": float64(rand.IntN(400000)-200000) / 100, "y": float64(rand.IntN(400000)-200000) / 100})
		}
		return request{method: "PATCH", path: fmt.Sprintf("/api/units/%d/members", t.id),
			body: jsonBody(map[string]any{"items": items})}
	}}, 15, true, 0},
	{op{name: "topic_design", write: true, build: func(u *vuser) request {
		// Remove a unit this user added earlier, or add a new one.
		if n := len(u.added); n > 0 && rand.IntN(2) == 0 {
			m := u.added[n-1]
			return request{name: "topic_remove", write: true, method: "PATCH", path: fmt.Sprintf("/api/units/%d/members", m.topic),
				body: jsonBody(map[string]any{"remove": []string{m.uid}}),
				next: func(class string, _ []byte) *request {
					if class == "ok" {
						u.added = u.added[:n-1]
					}
					return nil
				}}
		}
		t := u.topics[rand.IntN(len(u.topics))]
		// Not a current member: removing a rule match would exclude it, and
		// the layout saves of other users would then fail.
		uid := u.pickEditable()
		for i := 0; i < 8 && t.memberUIDs[uid]; i++ {
			uid = u.pickEditable()
		}
		return request{name: "topic_add", write: true, method: "POST", path: fmt.Sprintf("/api/units/%d/members", t.id),
			body: jsonBody(map[string]any{"add": []string{uid}}),
			next: func(class string, body []byte) *request {
				// Only units the server reports as newly added are removed
				// later (never a fixture member or a rule match).
				var reply struct {
					Report struct {
						Added []string `json:"added"`
					} `json:"report"`
				}
				if class == "ok" && json.Unmarshal(body, &reply) == nil && len(u.added) < 16 {
					for _, a := range reply.Report.Added {
						if a == uid {
							u.added = append(u.added, added{t.id, uid})
						}
					}
				}
				return nil
			}}
	}}, 15, true, 0},
}

func jsonBody(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// freeTags drops the reserved type/status/importance tags (node-model.js).
func freeTags(tags []string) []string {
	out := []string{}
	for _, t := range tags {
		if t != "" && !strings.HasPrefix(t, "type:") && !strings.HasPrefix(t, "status:") && !strings.HasPrefix(t, "importance:") {
			out = append(out, t)
		}
	}
	return out
}

func withTag(tags []string, tag string) []string {
	for _, t := range tags {
		if t == tag {
			return tags
		}
	}
	return append(tags, tag)
}

// buildMix returns the ops with choice probabilities summing to 1, such
// that writePct% of the *requests* are writes (a node edit also sends one
// read).  Reads and writes keep their relative weights; topic ops are left
// out without topics.
func buildMix(writePct float64, haveTopics bool) []op {
	var out []op
	var readSum, writeSum, extra float64
	for _, r := range readOps {
		readSum += r.base
	}
	for _, w := range writeOps {
		if haveTopics || !w.topics {
			writeSum += w.base
			extra += w.base * w.extraReads
		}
	}
	// a = share of write ops among choices; writes / requests = a / (1 + a*x).
	wr := writePct / 100
	a := 0.0
	if writeSum > 0 {
		a = wr / (1 - wr*extra/writeSum)
	}
	for _, r := range readOps {
		o := r.op
		o.weight = (1 - a) * r.base / readSum
		out = append(out, o)
	}
	if writePct > 0 {
		for _, w := range writeOps {
			if haveTopics || !w.topics {
				o := w.op
				o.weight = a * w.base / writeSum
				out = append(out, o)
			}
		}
	}
	return out
}

func choose(mix []op) int {
	r := rand.Float64()
	for i, o := range mix {
		if r < o.weight {
			return i
		}
		r -= o.weight
	}
	return len(mix) - 1
}

// topic is one topic of the project with the unit ids of its members.
type topic struct {
	id         int
	uid        string
	members    []int
	memberUIDs map[string]bool
}

type added struct {
	topic int
	uid   string
}

func (u *vuser) pick() string { return u.uids[rand.IntN(len(u.uids))] }

// pickEditable is a unit that is not a topic (a topic cannot contain itself;
// editing a topic's status is not what a node edit models).
func (u *vuser) pickEditable() string {
	for range 8 {
		if uid := u.pick(); !u.topicUIDs[uid] {
			return uid
		}
	}
	for _, uid := range u.uids {
		if !u.topicUIDs[uid] {
			return uid
		}
	}
	return u.pick()
}

func (u *vuser) topicWithMembers() topic {
	for range 8 {
		if t := u.topics[rand.IntN(len(u.topics))]; len(t.members) > 0 {
			return t
		}
	}
	for _, t := range u.topics {
		if len(t.members) > 0 {
			return t
		}
	}
	return u.topics[0]
}

// discoverTopics reads the project's topics and their members like the
// Topics page does (GET /api/topics, GET /api/units/<id>/members).
func discoverTopics(u *vuser, base string) ([]topic, error) {
	var list struct {
		Items []struct {
			ID  int    `json:"id"`
			UID string `json:"uid"`
		} `json:"items"`
	}
	if err := u.getJSON(base+"/api/topics", &list); err != nil {
		return nil, err
	}
	var out []topic
	for _, it := range list.Items {
		var detail struct {
			Members []struct {
				ID  int    `json:"id"`
				UID string `json:"uid"`
			} `json:"members"`
		}
		if err := u.getJSON(fmt.Sprintf("%s/api/units/%d/members", base, it.ID), &detail); err != nil {
			return nil, err
		}
		t := topic{id: it.ID, uid: it.UID, memberUIDs: map[string]bool{}}
		for _, m := range detail.Members {
			t.members = append(t.members, m.ID)
			t.memberUIDs[m.UID] = true
		}
		out = append(out, t)
	}
	// Topic ops are only used when at least one topic has members to lay out.
	ok := false
	for _, t := range out {
		ok = ok || len(t.members) > 0
	}
	if !ok {
		return nil, nil
	}
	return out, nil
}

// classify maps an answer to a class: "ok" (2xx), "conflict" (409),
// "db-locked" (5xx whose body mentions a locked/busy database),
// "server-timeout" (503/504 deadline answers), "http-4xx" or "http-5xx".
func classify(status int, body []byte) string {
	switch {
	case status >= 200 && status < 300:
		return "ok"
	case status == 409:
		return "conflict"
	case status >= 500:
		low := strings.ToLower(string(body))
		if strings.Contains(low, "database is locked") || strings.Contains(low, "sqlite_busy") ||
			strings.Contains(low, "busy") || strings.Contains(low, "locked") {
			return "db-locked"
		}
		if status == 504 || (status == 503 && (strings.Contains(low, "timeout") || strings.Contains(low, "timed out"))) {
			return "server-timeout"
		}
		return "http-5xx"
	}
	return "http-4xx"
}
