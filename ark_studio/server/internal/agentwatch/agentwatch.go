// Package agentwatch turns the project activity log into the update feed of
// GET /api/agent/v1/updates (Go only, T-0044): what people did since a
// cursor that an agent should act on (comments and replies, Accept / Send
// back / decisions on review nodes, changes to the agent's units, new
// decision nodes and tasks assigned to it, setup confirmations, messages).
// It is pure: internal/store reads the rows, internal/server serves them.
package agentwatch

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/store"
)

// Item kinds, in the order the docs list them.
const (
	KindComment        = "comment"          // a person commented on a unit
	KindReply          = "reply"            // a person replied to this agent's comment
	KindMention        = "mention"          // a comment mentions the agent (@agent, @<agent name>)
	KindSentBack       = "sent_back"        // Send back on a review node (status → active)
	KindAccepted       = "accepted"         // Accept on a review node (status → done)
	KindDecided        = "decided"          // a decision node was answered
	KindReopened       = "comment_reopened" // a resolved comment was reopened
	KindStatus         = "status"           // status change on the agent's unit
	KindAssignee       = "assignee"         // assignee change on the agent's unit
	KindImportance     = "importance"       // importance change on the agent's unit
	KindArchived       = "archived"         // the agent's unit was deleted (archived)
	KindAssigned       = "assigned"         // a task was assigned to the agent's user
	KindDecision       = "decision"         // a person created a decision node
	KindSetupConfirmed = "setup_confirmed"  // a setup layer was confirmed
	KindMessage        = "message"          // a message to this agent (experimental)
)

// Kinds lists every item kind.
var Kinds = []string{KindComment, KindReply, KindMention, KindSentBack, KindAccepted, KindDecided, KindReopened,
	KindStatus, KindAssignee, KindImportance, KindArchived, KindAssigned, KindDecision, KindSetupConfirmed, KindMessage}

// Viewer is who asks: the agent and the account it acts for.
type Viewer struct {
	User       string          // account name the token acts for ("" when unknown)
	AgentActor string          // this agent's write actor, agent:<user>[/<name>]
	AgentName  string          // X-Agent-Name ("" when none)
	TokenID    string          // personal token id ("" for the server token)
	TokenLabel string          // personal token label
	AnyAgent   bool            // the server token: every agent:* actor counts as this agent
	Mine       bool            // only units the agent or its user created or is assigned to
	Kinds      map[string]bool // nil = every kind
}

// ParseKinds parses ?kinds=a,b (empty = nil, every kind).
func ParseKinds(raw string) (map[string]bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	out := map[string]bool{}
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if !contains(Kinds, k) {
			return nil, fmt.Errorf("unknown kind %q (kinds: %s)", k, strings.Join(Kinds, ", "))
		}
		out[k] = true
	}
	return out, nil
}

// ------------------------------------------------------------------ cursor

// Cursor is "u1.<epoch>.<activity id>".  The epoch is the project's change
// log id, which a snapshot restore renews; clients treat the whole string
// as opaque.
func Cursor(epoch string, id int64) string { return "u1." + epoch + "." + strconv.FormatInt(id, 10) }

// ParseCursor reads a cursor; ok is false for anything malformed.
func ParseCursor(c string) (epoch string, id int64, ok bool) {
	rest, found := strings.CutPrefix(c, "u1.")
	if !found {
		return "", 0, false
	}
	i := strings.LastIndex(rest, ".")
	if i < 0 {
		return "", 0, false
	}
	n, err := strconv.ParseInt(rest[i+1:], 10, 64)
	if err != nil || n < 0 {
		return "", 0, false
	}
	return rest[:i], n, true
}

// ------------------------------------------------------------------ actors

// IsHuman reports an actor that is a person (or the Assistant acting in a
// person's browser): not an agent token, not the system.
func IsHuman(actor string) bool {
	a := strings.ToLower(actor)
	return actor != "" && !strings.HasPrefix(a, "agent:") && !strings.HasPrefix(a, "system")
}

// ownAgent reports an actor or comment author that is an agent of the
// viewer's account (agent:<user> or agent:<user>/<name>); for the server
// token, any agent.
func (v Viewer) ownAgent(actor string) bool {
	if !strings.HasPrefix(actor, "agent:") {
		return false
	}
	if v.User == "" || v.AnyAgent {
		return true
	}
	rest := strings.ToLower(actor[len("agent:"):])
	user := strings.ToLower(v.User)
	return rest == user || strings.HasPrefix(rest, user+"/")
}

func (v Viewer) isUser(name string) bool {
	return v.User != "" && strings.EqualFold(strings.TrimSpace(name), v.User)
}

// mine: the agent or its user created the unit, or it is assigned to the user.
func (v Viewer) mine(r store.UpdateRow) bool {
	if r.UnitID == 0 {
		return false
	}
	return v.ownAgent(r.Creator) || v.isUser(r.Assignee) || v.isUser(r.Creator) ||
		(v.User != "" && strings.EqualFold(r.Creator, "human:"+v.User))
}

var mentionRe = regexp.MustCompile(`(?i)(^|[^\w@])@([\w.:/-]+)`)

func (v Viewer) mentions(body string) bool {
	names := []string{"agent", "agents"}
	if v.AgentName != "" {
		names = append(names, v.AgentName)
	}
	if v.AgentActor != "" {
		names = append(names, v.AgentActor)
	}
	for _, m := range mentionRe.FindAllStringSubmatch(body, -1) {
		got := strings.TrimRight(m[2], ".:/-")
		for _, n := range names {
			if strings.EqualFold(got, n) {
				return true
			}
		}
	}
	return false
}

// MessageFor reports whether a message (store.Messages object) is meant for
// this viewer: the owner is the viewer's account and the target is any of
// its agents, this token, or this token's label.
func (v Viewer) MessageFor(m *pyjson.Object) bool {
	if m == nil || !v.isUser(pyjson.Str(m.Value("owner"))) {
		return false
	}
	target := pyjson.Str(m.Value("target"))
	switch {
	case target == "" || target == "mine":
		return true
	case strings.HasPrefix(target, "token:"):
		return v.TokenID != "" && target[len("token:"):] == v.TokenID
	case strings.HasPrefix(target, "label:"):
		return v.TokenLabel != "" && strings.EqualFold(target[len("label:"):], v.TokenLabel)
	}
	return false
}

// ------------------------------------------------------------------ items

// MessageIDs returns the message ids the rows mention (agent.message).
func MessageIDs(rows []store.UpdateRow) []int64 {
	var out []int64
	for _, r := range rows {
		if r.Action == store.MessageAction {
			if id, ok := r.Detail.Value("message_id").(int64); ok {
				out = append(out, id)
			}
		}
	}
	return out
}

// Items classifies rows (oldest first) into update items.  messages are the
// rows' messages by id (nil when messaging is off).  A review answer
// (sent_back / accepted / decided comment) absorbs the status change the
// browser writes right after it, so the agent sees one item.
func Items(rows []store.UpdateRow, v Viewer, messages map[int64]*pyjson.Object) []*pyjson.Object {
	var out []*pyjson.Object
	answered := map[int64]*pyjson.Object{} // unit id -> its review answer item in this batch
	for _, r := range rows {
		for _, it := range v.classify(r, messages, answered) {
			if v.Kinds != nil && !v.Kinds[pyjson.Str(it.Value("kind"))] {
				continue
			}
			out = append(out, it)
		}
	}
	return out
}

func (v Viewer) classify(r store.UpdateRow, messages map[int64]*pyjson.Object, answered map[int64]*pyjson.Object) []*pyjson.Object {
	if r.Action == store.MessageAction {
		id, _ := r.Detail.Value("message_id").(int64)
		m := messages[id]
		if m == nil || !v.MessageFor(m) {
			return nil
		}
		it := v.item(r, KindMessage, fmt.Sprintf("%s sent you a message%s: %s", pyjson.Str(m.Value("sender")),
			onUnit(r), excerpt(pyjson.Str(m.Value("body")))))
		it.Set("actor", m.Value("sender"))
		it.Set("message", pyjson.NewObject("id", id, "body", m.Value("body"), "status", m.Value("status"),
			"ack", fmt.Sprintf("/api/agent/v1/messages/%d/ack", id),
			"reply", fmt.Sprintf("/api/agent/v1/messages/%d/reply", id)))
		if r.UnitID == 0 {
			it.Set("link", fmt.Sprintf("/api/agent/v1/messages/%d", id))
		}
		return []*pyjson.Object{it}
	}
	if !IsHuman(r.Actor) {
		return nil
	}
	if strings.HasPrefix(r.Action, "review.group.") {
		return v.groupItems(r) // Go only: review groups (store/reviewgroups.go)
	}
	if r.UnitID != 0 && r.UID == "" {
		return nil // the unit is gone (purged)
	}
	mine := v.mine(r)
	if v.Mine && !mine && !strings.HasPrefix(r.Action, "setup.") {
		return nil
	}
	switch {
	case r.Action == "comment.create" && r.Comment != nil:
		c := r.Comment
		if strings.HasPrefix(c.Author, "agent:") {
			return nil
		}
		kind := KindComment
		verb := "commented on"
		switch c.Kind {
		case "sent_back":
			kind, verb = KindSentBack, "sent back"
		case "accepted":
			kind, verb = KindAccepted, "accepted"
		case "decided":
			kind, verb = KindDecided, "decided"
		default:
			if c.ReplyTo != 0 && v.ownAgent(c.ReplyToAuthor) {
				kind, verb = KindReply, "replied to your comment on"
			} else if v.mentions(c.Body) {
				kind, verb = KindMention, "mentioned you on"
			}
		}
		it := v.item(r, kind, fmt.Sprintf("%s %s %s: %s", c.Author, verb, unitLabel(r), excerpt(c.Body)))
		it.Set("actor", c.Author)
		var replyTo any
		if c.ReplyTo != 0 {
			replyTo = c.ReplyTo
		}
		it.Set("comment", pyjson.NewObject("id", c.ID, "body", c.Body, "kind", c.Kind, "reply_to", replyTo,
			"resolved", c.Resolved))
		it.Set("link", "/api/agent/v1/units/"+r.UID+"/comments")
		if kind == KindSentBack || kind == KindAccepted || kind == KindDecided {
			answered[r.UnitID] = it
		}
		return []*pyjson.Object{it}
	case r.Action == "comment.reopen" && r.Comment != nil:
		if strings.HasPrefix(r.Comment.Author, "agent:") {
			return nil
		}
		it := v.item(r, KindReopened, fmt.Sprintf("a comment on %s was reopened: %s", unitLabel(r), excerpt(r.Comment.Body)))
		it.Set("comment", pyjson.NewObject("id", r.Comment.ID, "body", r.Comment.Body, "kind", r.Comment.Kind,
			"reply_to", nilIfZero(r.Comment.ReplyTo), "resolved", r.Comment.Resolved))
		it.Set("link", "/api/agent/v1/units/"+r.UID+"/comments")
		return []*pyjson.Object{it}
	case r.Action == "unit.create":
		if r.Type == "decision" {
			return []*pyjson.Object{v.item(r, KindDecision, fmt.Sprintf("new decision %s", unitLabel(r)))}
		}
		if r.Type == "task" && v.isUser(pyjson.Str(r.Detail.Value("assignee"))) && !v.isUser(r.Actor) {
			return []*pyjson.Object{v.item(r, KindAssigned, fmt.Sprintf("new task %s assigned to %s", unitLabel(r), v.User))}
		}
		return nil
	case r.Action == "unit.update":
		return v.updateItems(r, mine, answered)
	case r.Action == "unit.archive":
		if !mine {
			return nil
		}
		return []*pyjson.Object{v.item(r, KindArchived, fmt.Sprintf("%s was deleted", unitLabel(r)))}
	case strings.HasPrefix(r.Action, "setup.") && strings.HasSuffix(r.Action, ".confirmed"):
		layer := strings.TrimSuffix(strings.TrimPrefix(r.Action, "setup."), ".confirmed")
		it := v.item(r, KindSetupConfirmed, fmt.Sprintf("the %s setup layer was confirmed", layer))
		it.Set("layer", layer).Set("link", "/api/agent/v1/setup")
		return []*pyjson.Object{it}
	}
	return nil
}

// groupItems turns a person's decision on, or comment in, a review group
// into an item for the agent that created the group.
func (v Viewer) groupItems(r store.UpdateRow) []*pyjson.Object {
	creator := pyjson.Str(r.Detail.Value("created_by"))
	mine := v.ownAgent(creator) || (v.User != "" && strings.EqualFold(creator, "human:"+v.User))
	if v.Mine && !mine {
		return nil
	}
	ref := pyjson.Str(r.Detail.Value("review"))
	label := ref + " \"" + pytext.Excerpt(pyjson.Str(r.Detail.Value("title")), 60) + "\""
	var it *pyjson.Object
	switch r.Action {
	case "review.group.decide":
		kind, verb := KindAccepted, "accepted review group"
		if list, ok := r.Detail.Value("members").([]any); ok {
			for _, m := range list {
				if o, ok := m.(*pyjson.Object); ok && o.Value("outcome") == "sent-back" {
					kind, verb = KindSentBack, "sent back (part of) review group"
				}
			}
		}
		it = v.item(r, kind, fmt.Sprintf("%s %s %s: %s", r.Actor, verb, label, excerpt(pyjson.Str(r.Detail.Value("note")))))
		it.Set("review", pyjson.NewObject("id", ref, "title", r.Detail.Value("title"), "note", r.Detail.Value("note"),
			"members", r.Detail.Value("members")))
	case "review.group.comment":
		if pyjson.Str(r.Detail.Value("kind")) == "agent_reply" {
			return nil
		}
		it = v.item(r, KindComment, fmt.Sprintf("%s commented on review group %s: %s", r.Actor, label, excerpt(pyjson.Str(r.Detail.Value("body")))))
		it.Set("review", pyjson.NewObject("id", ref, "title", r.Detail.Value("title")))
		it.Set("comment", pyjson.NewObject("id", r.Detail.Value("comment_id"), "body", r.Detail.Value("body"), "kind", "comment",
			"reply_to", nil, "resolved", false))
	default:
		return nil
	}
	it.Set("link", "/api/agent/v1/reviews/"+ref)
	return []*pyjson.Object{it}
}

func (v Viewer) updateItems(r store.UpdateRow, mine bool, answered map[int64]*pyjson.Object) []*pyjson.Object {
	fields := map[string]bool{}
	if list, ok := r.Detail.Value("fields").([]any); ok {
		for _, f := range list {
			fields[pyjson.Str(f)] = true
		}
	}
	var out []*pyjson.Object
	if fields["assignee"] {
		a, _ := r.Detail.Value("assignee").(*pyjson.Object)
		var from, to any
		if a != nil {
			from, to = a.Value("from"), a.Value("to")
		}
		if v.isUser(pyjson.Str(to)) && !v.isUser(pyjson.Str(from)) {
			it := v.item(r, KindAssigned, fmt.Sprintf("%s was assigned to %s", unitLabel(r), v.User))
			it.Set("change", pyjson.NewObject("field", "assignee", "from", from, "to", to))
			out = append(out, it)
		} else if mine || v.isUser(pyjson.Str(from)) {
			it := v.item(r, KindAssignee, fmt.Sprintf("assignee of %s: %s → %s", unitLabel(r), orNone(from), orNone(to)))
			it.Set("change", pyjson.NewObject("field", "assignee", "from", from, "to", to))
			out = append(out, it)
		}
	}
	if !mine {
		return out
	}
	if fields["status"] {
		s, _ := r.Detail.Value("status").(*pyjson.Object)
		var from, to any
		if s != nil {
			from, to = s.Value("from"), s.Value("to")
		}
		change := pyjson.NewObject("field", "status", "from", from, "to", to)
		if prev := answered[r.UnitID]; prev != nil {
			prev.Set("change", change) // Accept / Send back / Decide set the status next
			delete(answered, r.UnitID)
		} else {
			it := v.item(r, KindStatus, fmt.Sprintf("status of %s: %s → %s", unitLabel(r), orNone(from), orNone(to)))
			it.Set("change", change)
			out = append(out, it)
		}
	}
	if fields["importance"] {
		it := v.item(r, KindImportance, fmt.Sprintf("importance of %s is now %s", unitLabel(r), r.Priority))
		it.Set("change", pyjson.NewObject("field", "importance", "from", nil, "to", r.Priority))
		out = append(out, it)
	}
	return out
}

// item is the common shape: {id, kind, unit, uid, title, type, actor, at,
// summary, link}.
func (v Viewer) item(r store.UpdateRow, kind, summary string) *pyjson.Object {
	var unit, uid, title, typ, status any
	link := ""
	if r.UnitID != 0 {
		unit, uid, title, typ, status = r.UnitID, r.UID, r.Title, r.Type, r.Status
		link = "/api/agent/v1/units/" + r.UID
	}
	return pyjson.NewObject("id", "a"+strconv.FormatInt(r.ID, 10), "kind", kind, "unit", unit, "uid", uid,
		"title", title, "type", typ, "status", status, "actor", r.Actor, "at", r.At, "summary", summary, "link", link)
}

func unitLabel(r store.UpdateRow) string {
	if r.UID == "" {
		return "the project"
	}
	return r.UID + " \"" + pytext.Excerpt(r.Title, 60) + "\""
}

func onUnit(r store.UpdateRow) string {
	if r.UID == "" {
		return ""
	}
	return " about " + unitLabel(r)
}

func excerpt(s string) string { return pytext.Excerpt(s, 160) }

func orNone(v any) string {
	if s := pyjson.Str(v); v != nil && s != "" {
		return s
	}
	return "none"
}

func nilIfZero(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// SortedKinds returns the kinds of a filter, sorted (for echoing).
func SortedKinds(k map[string]bool) []any {
	if k == nil {
		return nil
	}
	keys := make([]string, 0, len(k))
	for x := range k {
		keys = append(keys, x)
	}
	sort.Strings(keys)
	out := make([]any, len(keys))
	for i, x := range keys {
		out[i] = x
	}
	return out
}
