package agentwatch

// Routing (Go only, 2026-11-26): who an update is meant for.  Every item of
// the feed says so ("for_you", "reason", "audience", "target_sessions"), and
// the wake-ups (the session nudger, `agent watch --until-update`, the
// watcher scripts) wake only for items meant for the agent.
//
//   - A person's answer on a unit (comment, Accept, Send back, decision,
//     status, importance, delete, move) goes to the agents that wrote the
//     unit: the one that last handed it to review, the one that created it
//     and the one that last wrote it.  Writers are told apart by the ARK
//     session, else the agent's own session id (X-ARK-Agent-Session), else
//     the token and device (store.Writer).
//   - A reply goes to the writer of the comment it answers.
//   - When no such writer is known, or every ARK session among them ended,
//     the update goes to the project as before (audience "project"): it is
//     for every agent of the account whose units it concerns.
//   - Messages, finished GPU jobs, mentions, assignments and setup
//     confirmations keep their own addressing.
//
// The feed itself still lists every update; for_you only marks the ones to
// act on.

import (
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/store"
)

// Audiences of an item.
const (
	AudienceSession = "session" // the agents that wrote the unit or comment
	AudienceProject = "project" // every agent of the account (no writer known)
)

// owner is one writer an update is routed to and why.
type owner struct {
	w   store.Writer
	why string // "submitted", "created", "last edited", "comment"
}

// is reports whether the viewer is the writer w.
func (v Viewer) is(w store.Writer) bool {
	me := v.Self
	switch {
	case w.Session != "":
		return me.Session == w.Session
	case me.Session != "":
		return false // an ARK session did not write it
	case w.AgentSession != "" && me.AgentSession != "":
		return me.AgentSession == w.AgentSession
	case w.Token != "":
		return me.Token == w.Token && (w.Host == "" || me.Host == "" || strings.EqualFold(w.Host, me.Host))
	}
	return false
}

// alive reports whether a writer can still be reached: an ARK session that
// ended cannot; an agent outside ARK is taken to be there.
func (v Viewer) alive(w store.Writer) bool {
	if !w.Known() {
		return false
	}
	return w.Session == "" || v.SessionAlive == nil || v.SessionAlive(w.Session)
}

// owners are the writers an update on r goes to (nil: the project).
func (v Viewer) owners(r store.UpdateRow) []owner {
	uw := r.Writers
	if uw == nil {
		return nil
	}
	if c := r.Comment; c != nil && c.ReplyTo != 0 && r.Action == "comment.create" {
		if w, ok := uw.Comments[c.ReplyTo]; ok && v.alive(w) {
			return []owner{{w, "comment"}}
		}
	}
	var out []owner
	for _, o := range []owner{{uw.Review, "submitted"}, {uw.Created, "created"}, {uw.Edited, "last edited"}} {
		if !v.alive(o.w) {
			continue
		}
		dup := false
		for _, have := range out {
			dup = dup || have.w == o.w
		}
		if !dup {
			out = append(out, o)
		}
	}
	return out
}

// owns reports whether the viewer is one of the writers an update on r goes to.
func (v Viewer) owns(r store.UpdateRow) bool {
	for _, o := range v.owners(r) {
		if v.is(o.w) {
			return true
		}
	}
	return false
}

// unitRouted are the kinds routed to the unit's writers.
var unitRouted = map[string]bool{KindComment: true, KindReply: true, KindSentBack: true, KindAccepted: true,
	KindDecided: true, KindReopened: true, KindStatus: true, KindImportance: true, KindArchived: true, KindMoved: true}

// annotate sets for_you, reason, audience and target_sessions on an item of row r.
func (v Viewer) annotate(r store.UpdateRow, it *pyjson.Object) {
	kind := pyjson.Str(it.Value("kind"))
	set := func(forYou bool, reason, audience string, targets []any) {
		if targets == nil {
			targets = []any{}
		}
		it.Set("for_you", forYou).Set("reason", reason).Set("audience", audience).Set("target_sessions", targets)
	}
	switch kind {
	case KindMessage:
		set(true, "message to you", AudienceSession, nil)
		return
	case KindGPUJobDone:
		set(true, "GPU job you queued", AudienceSession, nil)
		return
	case KindMention:
		set(true, "mentions you", AudienceProject, nil)
		return
	case KindAssigned:
		set(true, "assigned to you", AudienceProject, nil)
		return
	case KindSetupConfirmed:
		set(true, "project setup", AudienceProject, nil)
		return
	}
	if !unitRouted[kind] || r.UnitID == 0 {
		set(v.legacyForYou(r), "project update", AudienceProject, nil)
		return
	}
	owners := v.owners(r)
	if len(owners) == 0 {
		set(v.legacyForYou(r), "fallback: no owner", AudienceProject, nil)
		return
	}
	var targets []any
	seen := map[string]bool{}
	for _, o := range owners {
		id := o.w.Session
		if id == "" {
			id = o.w.AgentSession
		}
		if id != "" && !seen[id] {
			seen[id] = true
			targets = append(targets, id)
		}
	}
	for _, o := range owners {
		if v.is(o.w) {
			set(true, o.reason(r, true), AudienceSession, targets)
			return
		}
	}
	set(false, owners[0].reason(r, false), AudienceSession, targets)
}

// reason says why the update goes to this owner: "you submitted X-0065",
// "reply to your comment #142"; for another agent, "for the agent that
// submitted X-0065".
func (o owner) reason(r store.UpdateRow, you bool) string {
	if o.why == "comment" {
		ref := "#" + strconv.FormatInt(r.Comment.ReplyTo, 10)
		if you {
			return "reply to your comment " + ref
		}
		return "reply to another agent's comment " + ref
	}
	if you {
		return "you " + o.why + " " + r.UID
	}
	return "for the agent that " + o.why + " " + r.UID
}

// legacyForYou is the project-wide rule: the update concerns units of the
// viewer's account (what --mine and the wake-ups always used).
func (v Viewer) legacyForYou(r store.UpdateRow) bool {
	if strings.HasPrefix(r.Action, "review.group.") {
		creator := pyjson.Str(r.Detail.Value("created_by"))
		return v.ownAgent(creator) || (v.User != "" && strings.EqualFold(creator, "human:"+v.User))
	}
	if strings.HasPrefix(r.Action, "setup.") {
		return true
	}
	if v.mine(r) {
		return true
	}
	if r.Action == "unit.update" { // the assignee changed away from the viewer's user
		if a, ok := r.Detail.Value("assignee").(*pyjson.Object); ok && v.isUser(pyjson.Str(a.Value("from"))) {
			return true
		}
	}
	return false
}

// ForYouCount counts the items meant for the viewer.
func ForYouCount(items []*pyjson.Object) int64 {
	var n int64
	for _, it := range items {
		if it.Value("for_you") == true {
			n++
		}
	}
	return n
}
