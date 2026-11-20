package server

// Review requests and review groups (Go only; store/reviewrounds.go,
// store/reviewgroups.go, docs/agents/working-mode.md).
//
//   GET  /api/units/{ref}/review                    rounds, note, anchors, changes since the last review
//   GET  /api/reviews[?status=review|sent-back]     review groups
//   POST /api/reviews                               create a group {title, summary, review_note, members}
//   GET  /api/reviews/{id}                          one group
//   POST /api/reviews/{id}                          update / resubmit (status review) / close (status closed)
//   POST /api/reviews/{id}/decide                   {action: accept|send_back, note, members: {uid: ...}}
//   POST /api/reviews/{id}/comments                 {body}
//   POST /api/reviews/{id}/comments/{cid}/resolve   {resolved}
//
// The agent API has the same routes under /api/agent/v1/ except decide
// (only a person accepts or sends back).

import (
	"strconv"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/store"
)

var (
	reUnitReview        = pyRe(`^/api/units/([^/]+)/review$`, false)
	reReviews           = pyRe(`^/api/reviews$`, false)
	reReview            = pyRe(`^/api/reviews/([^/]+)$`, false)
	reReviewDecide      = pyRe(`^/api/reviews/([^/]+)/decide$`, false)
	reReviewComments    = pyRe(`^/api/reviews/([^/]+)/comments$`, false)
	reReviewResolve     = pyRe(`^/api/reviews/([^/]+)/comments/(\d+)/resolve$`, false)
	reAgentUnitReview   = pyRe(`^/api/agent/v1/units/([^/]+)/review$`, false)
	reAgentReviews      = pyRe(`^/api/agent/v1/reviews$`, false)
	reAgentReview       = pyRe(`^/api/agent/v1/reviews/([^/]+)$`, false)
	reAgentReviewCmts   = pyRe(`^/api/agent/v1/reviews/([^/]+)/comments$`, false)
	reAgentReviewSolved = pyRe(`^/api/agent/v1/reviews/([^/]+)/comments/(\d+)/resolve$`, false)
)

// reviewRoutesGoOnly are the Go-only ids of this file (GoOnlyRoutes).
var reviewRoutesGoOnly = []PortedRoute{
	{`goonly:regex:^/api/units/([^/]+)/review$`, "GET,HEAD"},
	{`goonly:regex:^/api/reviews$`, "GET,HEAD,POST"},
	{`goonly:regex:^/api/reviews/([^/]+)$`, "GET,HEAD,POST"},
	{`goonly:regex:^/api/reviews/([^/]+)/decide$`, "POST"},
	{`goonly:regex:^/api/reviews/([^/]+)/comments$`, "POST"},
	{`goonly:regex:^/api/reviews/([^/]+)/comments/(\d+)/resolve$`, "POST"},
	{`goonly:regex:^/api/agent/v1/units/([^/]+)/review$`, "GET,HEAD"},
	{`goonly:regex:^/api/agent/v1/reviews$`, "GET,HEAD,POST"},
	{`goonly:regex:^/api/agent/v1/reviews/([^/]+)$`, "GET,HEAD,POST"},
	{`goonly:regex:^/api/agent/v1/reviews/([^/]+)/comments$`, "POST"},
	{`goonly:regex:^/api/agent/v1/reviews/([^/]+)/comments/(\d+)/resolve$`, "POST"},
}

func reviewsGetPath(path string) bool {
	return reUnitReview.MatchString(path) || reReviews.MatchString(path) || reReview.MatchString(path)
}

// reviewsGet answers the GET routes of the web API.
func (c *reqCtx) reviewsGet(path string) error {
	p, err := c.require("read")
	if err != nil {
		return err
	}
	return c.reviewsRead(p.Store, path, reUnitReview, reReviews, reReview)
}

func (c *reqCtx) reviewsRead(s *store.Store, path string, unitRe, listRe, oneRe interface {
	FindStringSubmatch(string) []string
}) error {
	var out *pyjson.Object
	var err error
	if m := unitRe.FindStringSubmatch(path); m != nil {
		out, err = s.ReviewInfo(c.ctx, m[1])
	} else if m := listRe.FindStringSubmatch(path); m != nil {
		status := firstOrEmpty(c.query["status"])
		var groups []any
		if groups, err = s.ReviewGroups(c.ctx, status); err == nil {
			out = pyjson.NewObject("reviews", groups)
		}
	} else if m := oneRe.FindStringSubmatch(path); m != nil {
		var g *pyjson.Object
		if g, err = s.ReviewGroup(c.ctx, m[1]); err == nil {
			out = pyjson.NewObject("review", g)
		}
	} else {
		return apiErr(404, "Unknown endpoint: "+path, "")
	}
	if err != nil {
		return err
	}
	c.ok(out)
	return nil
}

func firstOrEmpty(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

func isReviewsPostPath(path string) bool {
	return reReviews.MatchString(path) || reReview.MatchString(path) || reReviewDecide.MatchString(path) ||
		reReviewComments.MatchString(path) || reReviewResolve.MatchString(path)
}

// reviewsPost answers the POST routes of the web API.
func (c *reqCtx) reviewsPost(path string) error {
	p, err := c.require("write")
	if err != nil {
		return err
	}
	s := p.Store
	body := c.body
	if body == nil {
		body = pyjson.NewObject()
	}
	switch {
	case reReviews.MatchString(path):
		return c.reviewReply(s.CreateReviewGroup(c.ctx, body, c.writer))
	case reReviewDecide.MatchString(path):
		return c.reviewReply(s.DecideReviewGroup(c.ctx, reReviewDecide.FindStringSubmatch(path)[1], body, c.actor(), c.writer, c.who(p)))
	case reReviewComments.MatchString(path):
		return c.reviewReply(s.AddReviewGroupComment(c.ctx, reReviewComments.FindStringSubmatch(path)[1], body.Value("body"), c.actor(), c.writer))
	case reReviewResolve.MatchString(path):
		m := reReviewResolve.FindStringSubmatch(path)
		return c.reviewResolve(s, m[1], m[2], body, c.writer)
	case reReview.MatchString(path):
		return c.reviewReply(s.UpdateReviewGroup(c.ctx, reReview.FindStringSubmatch(path)[1], body, c.writer))
	}
	return apiErr(404, "Unknown endpoint: "+path, "")
}

func (c *reqCtx) reviewResolve(s *store.Store, ref, cid string, body *pyjson.Object, actor string) error {
	id, err := strconv.ParseInt(cid, 10, 64)
	if err != nil {
		return apiErr(400, "comment id must be an integer", "")
	}
	resolved := true
	if v, ok := body.Get("resolved"); ok && v == false {
		resolved = false
	}
	return c.reviewReply(s.ResolveReviewGroupComment(c.ctx, ref, id, resolved, actor))
}

// reviewReply answers {review, warnings} (or {closed} once a group is gone).
func (c *reqCtx) reviewReply(g *pyjson.Object, err error) error {
	if err != nil {
		return err
	}
	out := pyjson.NewObject("review", g)
	if w, ok := g.Get("warnings"); ok {
		out.Set("warnings", w)
	} else {
		out.Set("warnings", []any{})
	}
	if v, ok := g.Get("closed"); ok {
		out.Set("closed", v)
	}
	c.ok(out)
	return nil
}

// agentReviewsGet answers the agent API's review GET routes (handled false
// for other paths).
func (c *reqCtx) agentReviewsGet(p *project.Project, path string) (bool, error) {
	if !reAgentUnitReview.MatchString(path) && !reAgentReviews.MatchString(path) && !reAgentReview.MatchString(path) {
		return false, nil
	}
	return true, c.reviewsRead(p.Store, path, reAgentUnitReview, reAgentReviews, reAgentReview)
}

// agentReviewsPost answers the agent API's review POST routes.
func (c *reqCtx) agentReviewsPost(p *project.Project, path string, body *pyjson.Object, actor string) (bool, error) {
	s := p.Store
	switch {
	case reAgentReviews.MatchString(path):
		return true, c.reviewReply(s.CreateReviewGroup(c.ctx, body, actor))
	case reAgentReviewCmts.MatchString(path):
		return true, c.reviewReply(s.AddReviewGroupComment(c.ctx, reAgentReviewCmts.FindStringSubmatch(path)[1], body.Value("body"), "", agentActor(actor)))
	case reAgentReviewSolved.MatchString(path):
		m := reAgentReviewSolved.FindStringSubmatch(path)
		return true, c.reviewResolve(s, m[1], m[2], body, agentActor(actor))
	case reAgentReview.MatchString(path):
		if v, ok := body.Get("action"); ok && v != nil {
			return true, apiErr(403, "only a person accepts or sends back a review group", "")
		}
		return true, c.reviewReply(s.UpdateReviewGroup(c.ctx, reAgentReview.FindStringSubmatch(path)[1], body, actor))
	}
	return false, nil
}

// withReviewWarnings adds the unresolved anchors of a review_note set in a
// unit write to its reply ({unit, review_warnings}).
func (c *reqCtx) withReviewWarnings(s *store.Store, ref string, body *pyjson.Object, out *pyjson.Object) {
	note, ok := body.Get("review_note")
	text, isStr := note.(string)
	if !ok || !isStr || text == "" {
		return
	}
	if warnings, err := s.ReviewNoteWarnings(c.ctx, ref, text); err == nil {
		if warnings == nil {
			warnings = []any{}
		}
		out.Set("review_warnings", warnings)
	}
}
