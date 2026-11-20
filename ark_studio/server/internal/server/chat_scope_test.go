package server

// T-0028: the Assistant chat is per user and project.  Local mode keeps the
// pre-v10 rows (empty user) in the local user's thread; an account server
// gives them to the project owner once; project archives carry other users'
// threads only for a project admin signed in through the browser.

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/sqlitedb"
)

func chatScopeApp(t *testing.T) (*App, *project.Project) {
	t.Helper()
	root := t.TempDir()
	app, err := NewApp(Config{Root: filepath.Join(root, "projects"), NoDemo: true,
		SessionsRoot: filepath.Join(root, "sessions")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.Bind("127.0.0.1", 8765)
	if r := gpuCall(t, app, "POST", "/api/projects", map[string]any{"name": "Chat scope"}, nil); r.code != 200 {
		t.Fatalf("create project: %d %s", r.code, r.raw)
	}
	p, err := (&reqCtx{app: app, ctx: context.Background()}).currentProject()
	if err != nil || p == nil {
		t.Fatalf("no current project: %v", err)
	}
	ctx := context.Background()
	if err := p.Store.DB.Write(ctx, func(q sqlitedb.Querier) error {
		for _, text := range []string{"legacy-one", "legacy-two"} {
			if _, err := q.ExecContext(ctx, "INSERT INTO chat_messages(at,role,content,tools_json,meta_json,user) "+
				"VALUES('t','user',?,'[]','{}','')", text); err != nil {
				return err
			}
		}
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range [][2]string{{"local", "local-own"}, {"ana", "ana-secret"}, {"owner", "owner-own"}} {
		if _, err := p.Store.AddChat(ctx, "user", m[1], nil, nil, m[0]); err != nil {
			t.Fatal(err)
		}
	}
	return app, p
}

func chatTexts(v any) string {
	var out []string
	list, _ := v.([]any)
	for _, m := range list {
		out = append(out, pyjson.Str(m.(map[string]any)["content"]))
	}
	return strings.Join(out, ",")
}

// archiveChat exports p keeping user's chat (nil: everyone's) and returns
// the chat rows of the archived database and whether its raw file
// mentions secret anywhere (free pages included).
func archiveChat(t *testing.T, p *project.Project, user *string, secret string) (string, bool) {
	t.Helper()
	var buf bytes.Buffer
	manifest, err := project.WriteArchiveChat(context.Background(), p, &buf, t.TempDir(), false, user)
	if err != nil {
		t.Fatal(err)
	}
	excluded := string(pyjson.Dumps(manifest.Value("excluded")))
	if strings.Contains(excluded, project.ArchiveChatExcluded) != (user != nil) {
		t.Errorf("manifest excluded: %s", excluded)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "ark.sqlite3")
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/ark.sqlite3") {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(rc)
			rc.Close()
			if err := os.WriteFile(dbPath, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT user || ':' || content FROM chat_messages ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return strings.Join(out, ","), secret != "" && bytes.Contains(raw, []byte(secret))
}

func mustChatUser(t *testing.T, c *reqCtx, p *project.Project) *string {
	t.Helper()
	u, err := c.archiveChatUser(p)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestChatLocalModeKeepsLegacyRows(t *testing.T) {
	app, p := chatScopeApp(t)
	ctx := context.Background()
	r := gpuCall(t, app, "GET", "/api/chat?limit=400", nil, nil)
	if r.code != 200 || chatTexts(r.body["messages"]) != "legacy-one,legacy-two,local-own" {
		t.Fatalf("local history: %d %s", r.code, r.raw)
	}
	// Local mode adopts nothing: the rows keep user '' (an account server
	// can still give them to the owner later).
	if err := app.adoptLegacyChat(ctx, p); err != nil {
		t.Fatal(err)
	}
	// The local export is the whole project.
	if u := mustChatUser(t, &reqCtx{app: app, ctx: ctx}, p); u != nil {
		t.Fatalf("local archive user: %q", *u)
	}
	if got, _ := archiveChat(t, p, nil, ""); got != ":legacy-one,:legacy-two,local:local-own,ana:ana-secret,owner:owner-own" {
		t.Errorf("local export chat: %q", got)
	}
	r = gpuCall(t, app, "DELETE", "/api/chat", nil, nil)
	if r.code != 200 || r.body["cleared"] != 3.0 {
		t.Fatalf("local clear: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/chat", nil, nil); r.code != 200 || chatTexts(r.body["messages"]) != "" {
		t.Errorf("after clear: %d %s", r.code, r.raw)
	}
	other := "ana"
	if rows, err := p.Store.ChatHistory(ctx, 400, &other, false); err != nil || len(rows) != 1 {
		t.Errorf("another user's thread after the local clear: %d %v", len(rows), err)
	}
}

func TestChatAccountModeScope(t *testing.T) {
	app, p := chatScopeApp(t)
	ctx := context.Background()
	if err := app.Org.Bootstrap(ctx, "owner", "x", false, nil); err != nil {
		t.Fatal(err)
	}
	for name, role := range map[string]string{"boss": "admin", "ana": "member"} {
		if _, err := app.Org.CreateUser(ctx, "owner", pyjson.NewObject("username", name,
			"password", "correct horse battery", "role", role)); err != nil {
			t.Fatal(err)
		}
	}
	app.Auth = &auth.Manager{} // account mode (only its presence is consulted here)
	defer func() { app.Auth = nil }()

	// The project was created by the local user: the org owner adopts the
	// pre-v10 rows, once.
	if err := app.adoptLegacyChat(ctx, p); err != nil {
		t.Fatal(err)
	}
	for user, want := range map[string]string{"owner": "legacy-one,legacy-two,owner-own", "ana": "ana-secret",
		"boss": "", "local": "local-own", "": ""} {
		u := user
		rows, err := p.Store.ChatHistory(ctx, 400, &u, false)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range rows {
			got = append(got, pyjson.Str(r.Value("content")))
		}
		if strings.Join(got, ",") != want {
			t.Errorf("%q: %q, want %q", user, strings.Join(got, ","), want)
		}
	}

	// Whose chat a project archive keeps.
	session := func(name string) *auth.Session { return &auth.Session{Username: name} }
	cases := []struct {
		name string
		c    *reqCtx
		want string // "" = everyone's
	}{
		{"owner browser", &reqCtx{app: app, ctx: ctx, session: session("owner"), user: "owner"}, ""},
		{"org admin browser", &reqCtx{app: app, ctx: ctx, session: session("boss"), user: "boss"}, ""},
		{"member browser", &reqCtx{app: app, ctx: ctx, session: session("ana"), user: "ana"}, "ana"},
		{"owner agent token", &reqCtx{app: app, ctx: ctx, user: "owner", token: &personalToken{}}, "owner"},
		{"owner task session", &reqCtx{app: app, ctx: ctx, session: session("owner"), user: "owner",
			task: &sessions.Session{}}, "owner"},
	}
	for _, tc := range cases {
		u := mustChatUser(t, tc.c, p)
		if (u == nil) != (tc.want == "") || (u != nil && *u != tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, u, tc.want)
		}
	}
	ana := "ana"
	if got, leaked := archiveChat(t, p, &ana, "legacy-one"); got != "ana:ana-secret" || leaked {
		t.Errorf("member archive: %q leaked=%v", got, leaked)
	}
	if _, leaked := archiveChat(t, p, &ana, "owner-own"); leaked {
		t.Errorf("member archive keeps the owner's text in free pages")
	}
	if got, _ := archiveChat(t, p, nil, ""); got != "owner:legacy-one,owner:legacy-two,local:local-own,ana:ana-secret,owner:owner-own" {
		t.Errorf("admin archive: %q", got)
	}
}
