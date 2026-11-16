package sessionwatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scenario is one CLI invocation sequence shared by the Go-only assertions
// and the differential test against session_watch.py.
type scenario struct {
	name string
	// setup prepares files under dir and the fake server, returning the
	// arguments, the env passed to run() and the home directory ("" = none).
	setup func(t *testing.T, dir string, srv *fakeARK) (Options, map[string]string, string)
	// between[i] runs after invocation i (so len(between)+1 invocations).
	between []func(t *testing.T, dir string, srv *fakeARK)
}

const sid = "ark-ses-watch01"

var claudeLines = []string{
	`{"type": "user", "message": {"role": "user", "content": "hello ark-ses-watch01"}}`,
	`{"type": "assistant", "message": {"role": "assistant", "content": [{"type": "text", "text": "hi"}]}}`,
	`{"type": "user", "message": {"role": "user", "content": "more"}}`,
}

func baseOpts(srv *fakeARK, file string) Options {
	return Options{ID: sid, File: file, Server: srv.url(), TokenEnv: "ARK_TOKEN", Interval: 60, Once: true}
}

func tokenEnv() map[string]string { return map[string]string{"ARK_TOKEN": "arka_test_token"} }

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
}

func fileScenario(name string, data []byte, tweak func(o *Options, env map[string]string, srv *fakeARK)) scenario {
	return scenario{name: name, setup: func(t *testing.T, dir string, srv *fakeARK) (Options, map[string]string, string) {
		file := filepath.Join(dir, "session.jsonl")
		write(t, file, data)
		o, env := baseOpts(srv, file), tokenEnv()
		if tweak != nil {
			tweak(&o, env, srv)
		}
		return o, env, ""
	}}
}

func errorOn(n int, raw string) func(o *Options, env map[string]string, srv *fakeARK) {
	return func(o *Options, env map[string]string, srv *fakeARK) { srv.override = map[int]string{n: raw} }
}

func scenarios() []scenario {
	partial := append(jsonl(claudeLines[:2]...), []byte(`{"type": "us`)...)
	big := []string{}
	for i := 0; i < 12; i++ {
		big = append(big, `{"type": "notice", "message": "line `+strings.Repeat("x", 40+i)+`"}`)
	}
	big = append(big[:4], append([]string{`{"type": "notice", "message": "` + strings.Repeat("y", 900) + `"}`}, big[4:]...)...)

	list := []scenario{
		fileScenario("once_upload", partial, func(o *Options, env map[string]string, srv *fakeARK) {
			o.Title, o.Agent, o.Project = "My run ü", "claude", "proj-1"
		}),
		{
			name: "incremental_append",
			setup: func(t *testing.T, dir string, srv *fakeARK) (Options, map[string]string, string) {
				file := filepath.Join(dir, "session.jsonl")
				write(t, file, partial)
				return baseOpts(srv, file), tokenEnv(), ""
			},
			between: []func(*testing.T, string, *fakeARK){
				func(t *testing.T, dir string, _ *fakeARK) {}, // nothing new: no POST
				func(t *testing.T, dir string, _ *fakeARK) {
					appendFile(t, filepath.Join(dir, "session.jsonl"), []byte("er\"}\n"+claudeLines[2]+"\n"))
				},
			},
		},
		{
			name: "shrink_replace",
			setup: func(t *testing.T, dir string, srv *fakeARK) (Options, map[string]string, string) {
				file := filepath.Join(dir, "session.jsonl")
				write(t, file, jsonl(claudeLines...))
				return baseOpts(srv, file), tokenEnv(), ""
			},
			between: []func(*testing.T, string, *fakeARK){
				func(t *testing.T, dir string, _ *fakeARK) {
					write(t, filepath.Join(dir, "session.jsonl"), jsonl(claudeLines[0]))
				},
			},
		},
		fileScenario("token_missing", jsonl(claudeLines...), func(o *Options, env map[string]string, srv *fakeARK) {
			delete(env, "ARK_TOKEN")
		}),
		fileScenario("token_blank_custom_env", jsonl(claudeLines...), func(o *Options, env map[string]string, srv *fakeARK) {
			o.TokenEnv = "MY_TOK"
			env["MY_TOK"] = " \t　"
		}),
		{
			name: "not_found",
			setup: func(t *testing.T, dir string, srv *fakeARK) (Options, map[string]string, string) {
				home := filepath.Join(dir, "home")
				write(t, filepath.Join(home, ".claude", "projects", "-w", "old.jsonl"), jsonl(`{"type": "user"}`))
				o := baseOpts(srv, "")
				o.ID = "ark-ses-missing1"
				return o, tokenEnv(), home
			},
		},
		{
			name: "auto_detect",
			setup: func(t *testing.T, dir string, srv *fakeARK) (Options, map[string]string, string) {
				home := filepath.Join(dir, "home")
				claude := filepath.Join(home, ".claude", "projects", "-work-repo")
				write(t, filepath.Join(claude, "old.jsonl"), jsonl(`{"type": "user", "message": {"content": "other"}}`))
				target := filepath.Join(claude, "1458aee0.jsonl")
				write(t, target, jsonl(claudeLines...))
				old := time.Now().Add(-time.Hour)
				_ = os.Chtimes(filepath.Join(claude, "old.jsonl"), old, old)
				write(t, filepath.Join(home, ".codex", "sessions", "2026", "11", "01", "rollout-x.jsonl"), jsonl(`{"type": "session_meta"}`))
				write(t, filepath.Join(home, ".dsh", "sessions", "a.jsonl.zstd"), []byte("ark-ses-watch01 compressed"))
				o := baseOpts(srv, "")
				o.ID = "  ARK-SES-WATCH01 "
				return o, tokenEnv(), home
			},
		},
		{
			name: "auto_detect_codex_home_env",
			setup: func(t *testing.T, dir string, srv *fakeARK) (Options, map[string]string, string) {
				home := filepath.Join(dir, "home")
				codex := filepath.Join(dir, "codexhome")
				write(t, filepath.Join(codex, "sessions", "2026", "x.JSONL"), jsonl(claudeLines...))
				env := tokenEnv()
				env["CODEX_HOME"] = filepath.ToSlash(codex) + "//"
				env["CLAUDE_CONFIG_DIR"] = filepath.Join(dir, "nothing")
				o := baseOpts(srv, "")
				return o, env, home
			},
		},
		{
			name: "agent_other_not_found",
			setup: func(t *testing.T, dir string, srv *fakeARK) (Options, map[string]string, string) {
				o := baseOpts(srv, "")
				o.Agent = "other"
				return o, tokenEnv(), filepath.Join(dir, "home")
			},
		},
		fileScenario("disabled_project", jsonl(claudeLines...), func(o *Options, env map[string]string, srv *fakeARK) {
			srv.enabled = false
		}),
		fileScenario("status_401", jsonl(claudeLines...), errorOn(0, jsonResponse(401, "Unauthorized",
			`{"error": {"code": "AGENT_TOKEN_INVALID", "message": "unknown or revoked agent token"}}`))),
		fileScenario("status_403_no_body", jsonl(claudeLines...), errorOn(0,
			"HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")),
		fileScenario("upload_403_disabled", jsonl(claudeLines...), errorOn(1, jsonResponse(403, "Forbidden",
			`{"error": {"code": "SESSION_UPLOADS_DISABLED", "message": "off"}}`))),
		fileScenario("upload_500_json", jsonl(claudeLines...), errorOn(1, jsonResponse(500, "Internal Server Error",
			`{"error": {"code": "INTERNAL", "message": "boom"}}`))),
		fileScenario("status_502_html", jsonl(claudeLines...), errorOn(0,
			"HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/html\r\nContent-Length: 13\r\n\r\n<h1>bad</h1>\n")),
		fileScenario("status_500_odd_error_fields", jsonl(claudeLines...), errorOn(0, jsonResponse(500, "Oops",
			`{"error": {"code": 7, "message": ["a", 1.0, true, null]}}`))),
		fileScenario("upload_409", jsonl(claudeLines...), errorOn(1, jsonResponse(409, "Conflict",
			`{"error": {"code": "SESSION_OFFSET_MISMATCH", "message": "moved"}}`))),
		fileScenario("chunking_and_marker", jsonl(big...), func(o *Options, env map[string]string, srv *fakeARK) {
			srv.limit = 300
		}),
		fileScenario("server_received_beyond_file", jsonl(claudeLines...), func(o *Options, env map[string]string, srv *fakeARK) {
			srv.received = 100000
		}),
		fileScenario("odd_status_values", jsonl(claudeLines...), errorOn(0, jsonResponse(200, "OK",
			`{"uploads_enabled": 1, "received_bytes": " 1_0 ", "limits": {"chunk_bytes": 64.9}}`))),
		fileScenario("negative_offsets_and_limit", jsonl(claudeLines...), errorOn(0, jsonResponse(200, "OK",
			`{"uploads_enabled": true, "received_bytes": -40, "limits": {"chunk_bytes": "-3"}}`))),
		fileScenario("huge_numbers", jsonl(claudeLines...), errorOn(0, jsonResponse(200, "OK",
			`{"uploads_enabled": true, "received_bytes": 0, "limits": {"chunk_bytes": 1e300}}`))),
		fileScenario("error_field_is_string", jsonl(claudeLines...), errorOn(0, jsonResponse(500, "X",
			`{"error": "flat"}`))),
		fileScenario("error_body_not_utf8", jsonl(claudeLines...), errorOn(0,
			"HTTP/1.1 418 I'm a teapot\r\nContent-Length: 2\r\n\r\n\xff\xfe")),
		fileScenario("continue_then_http10_until_close", jsonl(claudeLines...), errorOn(0,
			"HTTP/1.1 100 Continue\r\n\r\nHTTP/1.0 200 Fine  \r\n\r\n{\"uploads_enabled\": true}")),
		fileScenario("reason_latin1", jsonl(claudeLines...), errorOn(0,
			"HTTP/1.1 500 Fehl\xe4r \r\nContent-Length: 0\r\n\r\n")),
		fileScenario("truncated_body", jsonl(claudeLines...), errorOn(0,
			"HTTP/1.1 500 Short\r\nContent-Length: 50\r\n\r\n{\"error\"")),
		fileScenario("bad_status_line", jsonl(claudeLines...), errorOn(0, "HTTX/1.1 200 OK\r\n\r\n")),
		fileScenario("upload_result_without_received", jsonl(claudeLines...), errorOn(1, jsonResponse(200, "OK", ``))),
		fileScenario("chunked_response", jsonl(claudeLines...), errorOn(0,
			"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n"+
				"11\r\n{\"uploads_enabled\r\n0x8;ext=1\r\n\": true}\r\n0\r\n\r\n")),
		fileScenario("unicode_and_invalid_utf8", append(jsonl(`{"text": "日本語 😀"}`), []byte("{\"bad\": \"\xe2\x82A\xff\"}\n")...), func(o *Options, env map[string]string, srv *fakeARK) {
			o.Title = "标题 😀"
		}),
		fileScenario("server_base_path", jsonl(claudeLines...), func(o *Options, env map[string]string, srv *fakeARK) {
			o.Server = srv.url() + "/base//"
			o.Project = "p 1"
		}),
		fileScenario("redirect_get", jsonl(claudeLines...), errorOn(0,
			"HTTP/1.1 302 Found\r\nLocation: /api/agent/v1/sessions/"+sid+"?moved=1\r\nContent-Length: 0\r\n\r\n")),
		fileScenario("redirect_post_307", jsonl(claudeLines...), errorOn(1,
			"HTTP/1.1 307 Temporary Redirect\r\nLocation: /elsewhere\r\nContent-Length: 0\r\n\r\n")),
		fileScenario("redirect_post_303", jsonl(claudeLines...), errorOn(1,
			"HTTP/1.1 303 See Other\r\nLocation: /api/agent/v1/sessions/"+sid+"\r\nContent-Length: 0\r\n\r\n")),
		fileScenario("redirect_bad_scheme", jsonl(claudeLines...), errorOn(0,
			"HTTP/1.1 301 Moved\r\nLocation: javascript:alert(1)\r\nContent-Length: 0\r\n\r\n")),
		fileScenario("redirect_loop", jsonl(claudeLines...), func(o *Options, env map[string]string, srv *fakeARK) {
			srv.hook = func(n int, r recorded) string {
				return "HTTP/1.1 302 Found\r\nLocation: /loop\r\nContent-Length: 0\r\n\r\n"
			}
		}),
		fileScenario("remote_closed", jsonl(claudeLines...), errorOn(0, "")),
		fileScenario("connection_refused", jsonl(claudeLines...), nil),
		fileScenario("unknown_url_type", jsonl(claudeLines...), func(o *Options, env map[string]string, srv *fakeARK) {
			o.Server = "localhost:1"
		}),
		fileScenario("no_host_given", jsonl(claudeLines...), func(o *Options, env map[string]string, srv *fakeARK) {
			o.Server = "http:///nohost"
		}),
		fileScenario("status_not_a_dict", jsonl(claudeLines...), errorOn(0, jsonResponse(200, "OK", `[1, 2]`))),
		fileScenario("status_bad_json", jsonl(claudeLines...), errorOn(0, jsonResponse(200, "OK", `{oops`))),
		{
			name: "missing_file",
			setup: func(t *testing.T, dir string, srv *fakeARK) (Options, map[string]string, string) {
				return baseOpts(srv, filepath.Join(dir, "nope", "x.jsonl")), tokenEnv(), ""
			},
		},
		{
			name: "directory_as_file",
			setup: func(t *testing.T, dir string, srv *fakeARK) (Options, map[string]string, string) {
				return baseOpts(srv, filepath.ToSlash(dir)+"/./"), tokenEnv(), ""
			},
		},
		{
			name: "zstd_file",
			setup: func(t *testing.T, dir string, srv *fakeARK) (Options, map[string]string, string) {
				file := filepath.Join(dir, "s.jsonl.zstd")
				write(t, file, []byte("\x28\xb5\x2f\xfd"))
				return baseOpts(srv, file), tokenEnv(), ""
			},
		},
	}
	// connection_refused needs a dead port, filled in at setup time.
	for i := range list {
		if list[i].name == "connection_refused" {
			inner := list[i].setup
			list[i].setup = func(t *testing.T, dir string, srv *fakeARK) (Options, map[string]string, string) {
				o, env, home := inner(t, dir, srv)
				o.Server = "http://" + closedPort(t)
				return o, env, home
			}
		}
		if list[i].name == "remote_closed" {
			inner := list[i].setup
			list[i].setup = func(t *testing.T, dir string, srv *fakeARK) (Options, map[string]string, string) {
				o, env, home := inner(t, dir, srv)
				srv.hook = func(n int, r recorded) string {
					if n == 0 {
						return closeMarker
					}
					return ""
				}
				return o, env, home
			}
		}
	}
	return list
}
