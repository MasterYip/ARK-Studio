package org

// Home starter-prompt tokens (Go-only, UX-18; one per user and project since
// 2026-11-22).  POST /api/personal/agent-tokens with {"purpose": "starter"}
// runs the Home "Copy prompt" flow:
//
//	mode "copy" (default)  {project, revoke_old?}  the project's starter token,
//	                       minted only when there is none or it expired or was
//	                       revoked; "reused" says which
//	mode "new"             {project, revoke_old?}  a new token; the previous
//	                       starter token of the project is revoked
//	mode "peek"            {project?}  the current token's item (no secret) and
//	                       how many older starter tokens are active (of the project,
//	                       or of all projects and the unscoped ones)
//	mode "cleanup"         {project?}  revoke the older starter tokens (all
//	                       projects and the unscoped ones without project)
//
// When a starter token is minted its value is also kept, sealed with the
// server's SecretBox (SealSecret: key <private>/org/secret.key, 0600, or
// ARK_ORG_SECRET_KEY), in starter_token_secrets, bound by the AAD to the
// user, project and token id.  Only this endpoint opens it, for the signed-in
// owner (routes_org.go personalTokensGuard refuses agent, task and phone
// tokens).  Starter tokens minted before have no stored value: they keep
// working, cannot be reused, and count as "older" for revoke_old and cleanup.
// There is no limit on the number of active tokens (since 2026-11-23).

import (
	"context"
	"regexp"
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/sqlitedb"
)

const starterSecretSchema = `CREATE TABLE IF NOT EXISTS starter_token_secrets(
  user_id TEXT NOT NULL,
  project TEXT NOT NULL,
  token_id TEXT NOT NULL,
  secret TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(user_id, project)
);
CREATE INDEX IF NOT EXISTS starter_token_secrets_token ON starter_token_secrets(token_id);
`

const starterLabelPrefix = "Home starter prompt"

var reStarterProject = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,39}$`)

type starterReq struct {
	project, mode string
	revokeOld     bool
}

// starterRequest reads the optional "purpose" members of a create.
func starterRequest(body *pyjson.Object) (starterReq, bool, error) {
	purpose := pytext.Lower(pytext.Strip(strArg(body.Value("purpose"))))
	if purpose == "" {
		return starterReq{}, false, nil
	}
	if purpose != "starter" {
		return starterReq{}, false, orgErr(400, "purpose must be starter (or omitted)", "AGENT_TOKEN_PURPOSE_INVALID")
	}
	req := starterReq{
		mode:      pytext.Lower(pytext.Strip(strArg(body.Value("mode")))),
		project:   pytext.Strip(strArg(body.Value("project"))),
		revokeOld: pyjson.Truthy(body.Value("revoke_old")),
	}
	if req.mode == "" {
		req.mode = "copy"
	}
	switch req.mode {
	case "copy", "new", "peek", "cleanup":
	default:
		return req, false, orgErr(400, "mode must be copy, new, peek or cleanup", "AGENT_TOKEN_MODE_INVALID")
	}
	if !((req.mode == "cleanup" || req.mode == "peek") && req.project == "") && !reStarterProject.MatchString(req.project) {
		return req, false, orgErr(400, "a starter token needs the project id", "AGENT_TOKEN_PROJECT_INVALID")
	}
	return req, true, nil
}

func starterLabelFor(project string) string {
	return starterLabelPrefix + " (" + project + ")"
}

func starterLabel(project string, now float64) string {
	return starterLabelFor(project) + " – " + time.Unix(0, int64(now*1e9)).UTC().Format("2006-01-02")
}

// isStarterLabel: a starter label of the project, or of any project and the
// unscoped ones ("Home starter prompt – <date>") when project is "".
func isStarterLabel(label, project string) bool {
	if project != "" {
		return strings.HasPrefix(label, starterLabelFor(project)+" – ")
	}
	return strings.HasPrefix(label, starterLabelPrefix+" (") || strings.HasPrefix(label, starterLabelPrefix+" – ")
}

func starterAAD(userID, project, tokenID string) string {
	return "ark-starter-token\x00" + userID + "\x00" + project + "\x00" + tokenID
}

func (m *Manager) tokenExpired(r sqlitedb.Row) bool {
	return m.tokenPublic(r).Value("expired") == true
}

// starterCurrent is the project's stored starter token if it still works.
func (m *Manager) starterCurrent(ctx context.Context, q sqlitedb.Querier, userID, project string) (sqlitedb.Row, string, bool, error) {
	stored, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM starter_token_secrets WHERE user_id=? AND project=?", userID, project)
	if err != nil || !ok {
		return sqlitedb.Row{}, "", false, err
	}
	tok, ok, err := sqlitedb.QueryOne(ctx, q, "SELECT * FROM agent_tokens WHERE id=? AND user_id=? AND revoked_at IS NULL",
		stored.Str("token_id"), userID)
	if err != nil || !ok || m.tokenExpired(tok) {
		return sqlitedb.Row{}, "", false, err
	}
	return tok, stored.Str("secret"), true, nil
}

// starterOld lists the user's unrevoked starter tokens (of the project, or
// all when project is "") that are no project's current stored token, and
// how many of them are active.
func (m *Manager) starterOld(ctx context.Context, q sqlitedb.Querier, userID, project string) ([]string, int, error) {
	rows, err := sqlitedb.Query(ctx, q, "SELECT * FROM agent_tokens WHERE user_id=? AND revoked_at IS NULL "+
		"AND id NOT IN (SELECT token_id FROM starter_token_secrets WHERE user_id=?) ORDER BY created_at, id", userID, userID)
	if err != nil {
		return nil, 0, err
	}
	var ids []string
	active := 0
	for _, r := range rows {
		if !isStarterLabel(r.Str("label"), project) {
			continue
		}
		ids = append(ids, r.Str("id"))
		if !m.tokenExpired(r) {
			active++
		}
	}
	return ids, active, nil
}

func revokeIDs(ctx context.Context, q sqlitedb.Querier, userID string, ids []string) ([]any, error) {
	at := isoAt(TokenClock())
	out := []any{}
	for _, id := range ids {
		res, err := q.ExecContext(ctx, "UPDATE agent_tokens SET revoked_at=? WHERE id=? AND user_id=? AND revoked_at IS NULL", at, id, userID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			out = append(out, id)
		}
		if _, err := q.ExecContext(ctx, "DELETE FROM starter_token_secrets WHERE user_id=? AND token_id=?", userID, id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// starterToken answers a create with purpose "starter" (see the file comment).
func (m *Manager) starterToken(ctx context.Context, user sqlitedb.Row, username string, req starterReq, scope string, days int64) (*pyjson.Object, error) {
	uid := user.Str("id")
	if req.mode == "peek" {
		var current any
		old := 0
		err := m.DB.Read(ctx, func(q sqlitedb.Querier) error {
			if req.project != "" {
				tok, _, ok, err := m.starterCurrent(ctx, q, uid, req.project)
				if err != nil {
					return err
				}
				if ok {
					current = m.tokenPublic(tok)
				}
			}
			var err error
			_, old, err = m.starterOld(ctx, q, uid, req.project)
			return err
		})
		if err != nil {
			return nil, err
		}
		return m.withTokenList(ctx, username, pyjson.NewObject("current", current, "old_tokens", int64(old)))
	}
	var (
		out     = pyjson.NewObject()
		revoked []any
		created string
		reused  string
	)
	err := m.DB.Write(ctx, func(q sqlitedb.Querier) error {
		var revoke []string
		if req.revokeOld || req.mode == "cleanup" {
			ids, _, err := m.starterOld(ctx, q, uid, req.project)
			if err != nil {
				return err
			}
			revoke = ids
		}
		if req.mode == "cleanup" {
			var err error
			revoked, err = revokeIDs(ctx, q, uid, revoke)
			return err
		}
		tok, sealed, ok, err := m.starterCurrent(ctx, q, uid, req.project)
		if err != nil {
			return err
		}
		if ok && req.mode == "copy" {
			if secret, err := OpenSecret(sealed, starterAAD(uid, req.project, tok.Str("id"))); err == nil {
				if revoked, err = revokeIDs(ctx, q, uid, revoke); err != nil {
					return err
				}
				reused = tok.Str("id")
				out.Set("token", secret)
				out.Set("item", m.tokenPublic(tok))
				return nil
			}
			// Unreadable with this host's key: mint a new one below; the
			// old token keeps working and counts as an older one.
		}
		if ok && req.mode == "new" {
			revoke = append(revoke, tok.Str("id"))
		}
		if revoked, err = revokeIDs(ctx, q, uid, revoke); err != nil {
			return err
		}
		minted, err := insertToken(ctx, q, uid, starterLabel(req.project, TokenClock()), scope, days)
		if err != nil {
			return err
		}
		sealed, err = SealSecret(minted.secret, starterAAD(uid, req.project, minted.id))
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, "INSERT INTO starter_token_secrets(user_id,project,token_id,secret,created_at) VALUES(?,?,?,?,?) "+
			"ON CONFLICT(user_id,project) DO UPDATE SET token_id=excluded.token_id, secret=excluded.secret, created_at=excluded.created_at",
			uid, req.project, minted.id, sealed, isoAt(TokenClock())); err != nil {
			return err
		}
		created = minted.id
		out.Set("token", minted.secret)
		out.Set("item", minted.item)
		return nil
	}, nil)
	if err != nil {
		return nil, err
	}
	if created != "" {
		if err := m.Log(ctx, uid, "personal.agent_token.create", "", pyjson.NewObject("token", created, "scope", scope,
			"purpose", "starter", "project", req.project)); err != nil {
			return nil, err
		}
	}
	if reused != "" {
		if err := m.Log(ctx, uid, "personal.agent_token.starter_reuse", "", pyjson.NewObject("token", reused, "project", req.project)); err != nil {
			return nil, err
		}
	}
	if len(revoked) > 0 {
		if err := m.Log(ctx, uid, "personal.agent_token.rotate", "", pyjson.NewObject("token", created,
			"project", req.project, "revoked", revoked)); err != nil {
			return nil, err
		}
	}
	if req.mode == "cleanup" {
		out.Set("revoked", revoked)
	} else {
		// Go-only (tests/python/contract/go_only.py "agent-token-starter-rotation").
		out.Set("reused", reused != "")
		out.Set("rotated", revoked)
	}
	return m.withTokenList(ctx, username, out)
}
