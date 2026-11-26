package main

// The `python -m arkstudio` command line (arkstudio/__main__.py) on the Go
// side: the same parser tree (internal/compat/pyargparse, snapshot of the Python
// parser), the same defaults and environment fallbacks, the same output
// texts, exit codes and file effects.  Go-only additions: `routes`,
// `version` and the serve tuning flags (exact spelling only).

import (
	"arkstudio/go/internal/platform/pyerr"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/compat/pyargparse"
	"arkstudio/go/internal/demoagent"
	"arkstudio/go/internal/mobile"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/skillbrief"
	"arkstudio/go/internal/skillkit"
	"arkstudio/go/internal/store"
)

// textWriter is a Python text stream: "\n" becomes os.linesep on Windows.
type textWriter struct{ w io.Writer }

func (t textWriter) Write(b []byte) (int, error) {
	if runtime.GOOS == "windows" {
		if _, err := t.w.Write([]byte(strings.ReplaceAll(string(b), "\n", "\r\n"))); err != nil {
			return 0, err
		}
		return len(b), nil
	}
	return t.w.Write(b)
}

var (
	stdout io.Writer = textWriter{os.Stdout}
	stderr io.Writer = textWriter{os.Stderr}
)

// print(...) and print(..., file=sys.stderr).
func pyprint(a ...any)            { fmt.Fprintln(stdout, a...) }    //nolint:errcheck
func pyprintf(f string, a ...any) { fmt.Fprintf(stdout, f, a...) }  //nolint:errcheck
func eprintln(a ...any)           { fmt.Fprintln(stderr, a...) }    //nolint:errcheck
func eprintf(f string, a ...any)  { fmt.Fprintf(stderr, f, a...) }  //nolint:errcheck
func printBytes(b []byte)         { stdout.Write(append(b, '\n')) } //nolint:errcheck

// pyPath is str(pathlib.Path(p)) for the paths the CLI prints.
func pyPath(p string) string {
	if p == "" {
		return "."
	}
	return filepath.Clean(p)
}

// newParser is the arkstudio parser with the defaults of __main__.main.
func newParser() *pyargparse.Parser {
	p := pyargparse.Spec()
	root := pyPath(defaultRoot())
	envOr := func(name, def string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return def
	}
	p.Sub("server-init").Default("owner", "admin").Default("password_file", "").
		Default("port", int64(8890)).Default("public_url", "")
	serve := p.Sub("serve")
	serve.Default("config", "").Default("require_auth", false).Default("root", root).Default("host", nil).
		Default("port", int64(envInt("ARK_PORT", 8890))).Default("no_demo", false).Default("media_root", []string{}).
		Default("sessions_root", "").Default("public_url", os.Getenv("ARK_PUBLIC_URL")).
		Default("expose", envBool("ARK_PUBLIC")).Default("auth_user", "").Default("auth_password", "").
		Default("auth_password_file", "").Default("auth_password_hash", "").Default("auth_session_ttl", int64(0)).
		Default("public_ip", os.Getenv("ARK_PUBLIC_IP")).Default("detect_public_ip", envBool("ARK_DETECT_PUBLIC_IP")).
		Default("trust_proxy", envBool("ARK_TRUST_PROXY"))
	serve.AddExact("--web-root", "web_root", "store", "", os.Getenv("ARK_WEB_ROOT")).
		AddExact("--read-pool", "read_pool", "store", "int", int64(0)).
		AddExact("--heavy-limit", "heavy_limit", "store", "int", int64(0)).
		AddExact("--max-subprocesses", "max_subprocesses", "store", "int", int64(0)).
		AddExact("--max-in-flight", "max_in_flight", "store", "int", int64(0)).
		AddExact("--request-timeout", "request_timeout", "store", "duration", 30*time.Second)
	p.Sub("desktop").Default("root", root).Default("host", "127.0.0.1").Default("port", int64(8890)).
		Default("no_server", false).Default("remote_url", os.Getenv("ARK_REMOTE_URL")).Default("local", false)
	p.Sub("remote").Default("user", "").Default("ssh_port", int64(22)).Default("remote_port", int64(8890)).
		Default("local_port", int64(8890)).Default("identity", "").Default("remote_path", "").Default("mount", "").
		Default("no_open", false)
	p.Sub("mobile-tunnel").Default("ssh", envOr("ARK_SSH_TARGET", "user@server")).Default("ssh_port", int64(22)).
		Default("ark_port", int64(8890)).Default("local_port", int64(8891)).Default("bind", "0.0.0.0").
		Default("identity", "")
	p.Sub("new").Default("root", root).Default("template", "blank").Default("description", "")
	p.Sub("demo").Default("root", root)
	p.Sub("export").Default("format", "json").Default("units", "").Default("depth", int64(1)).
		Default("media_root", []string{}).Default("out", "")
	p.Sub("agent", "context").Default("project", "").Default("root", root)
	p.Sub("session-watch").Default("file", "").Default("agent", "").
		Default("server", envOr("ARK_URL", "http://127.0.0.1:8890")).Default("project", os.Getenv("ARK_PROJECT")).
		Default("token_env", "ARK_TOKEN").Default("interval", 60.0).Default("title", "").Default("once", false)
	p.Sub("skills", "render").Default("context", "").Default("out", "")
	p.Sub("skills", "validate").Default("payload", "").Default("project_root", "")
	p.Sub("skills", "brief").Default("payload", "")
	p.Sub("skills", "setup").Default("setup_action", "state").Default("project", "").Default("paths", []string{}).
		Default("payload", "").Default("propose", false).Default("digest", "").Default("note", "")
	return p
}

func main() { os.Exit(run(os.Args[1:])) }

// run is __main__.main(argv).
func run(argv []string) int {
	if len(argv) > 0 {
		switch argv[0] {
		case "routes":
			return routes(argv[1:])
		case "docs-check": // Go only: the binary embeds the agent docs (release builds)
			return docsCheck(argv[1:])
		case "session-relay": // Go only: keeps an agent session running across server restarts
			return sessions.RelayMain(argv[1:])
		case "demo-agent": // the echo preset's agent (arkstudio/demo_agent.py)
			return demoagent.Main(argv[1:], os.Stdin, os.Stdout, os.Stderr)
		case "agent": // Go only: `agent watch` (cli_agentwatch.go); `agent context` is the Python parser's
			if len(argv) > 1 && argv[1] == "watch" {
				return agentWatch(argv[2:], stdout, stderr)
			}
			if len(argv) > 1 && argv[1] == "host-id" { // this device's id for X-ARK-Host (internal/hostid)
				return agentHostID(argv[2:], stdout)
			}
			if len(argv) > 1 && argv[1] == "folder" { // the folder for one unit's work (cli_relocate.go)
				return agentFolder(argv[2:], stdout, stderr)
			}
			if len(argv) > 1 && argv[1] == "paths" { // this device's task space and paths (cli_relocate.go)
				return agentPaths(argv[2:], stdout, stderr)
			}
			if len(argv) > 1 && argv[1] == "relocate" { // move recorded paths (cli_relocate.go)
				return agentRelocate(argv[2:], stdout, stderr)
			}
		case "version", "--version":
			pyprint("ark-server", version)
			return 0
		}
	}
	parser := newParser()
	ns, err := parseCommandLine(parser, argv, stdout, stderr)
	if err != nil {
		return exitCode(err)
	}
	code, err := dispatch(parser, ns, argv)
	if err != nil {
		return reportError(err)
	}
	return code
}

// parseCommandLine is `args = parser.parse_args(argv)` plus the re-parse as
// `serve` when no subcommand was given.
func parseCommandLine(parser *pyargparse.Parser, argv []string, out, errw io.Writer) (pyargparse.Namespace, error) {
	ns, err := parser.Parse(argv, out, errw)
	if err == nil && ns["command"] == nil {
		ns, err = parser.Parse(append([]string{"serve"}, argv...), out, errw)
	}
	return ns, err
}

func exitCode(err error) int {
	var e *pyargparse.Exit
	if errors.As(err, &e) {
		return e.Code
	}
	return 1
}

// reportError is the `except (ProjectError, StoreError, ValueError)` branch
// (exit 2).  Anything else is an uncaught exception in Python: exit 1 with a
// traceback whose last line is "module.Class: message"; Go prints that line.
func reportError(err error) int {
	var (
		pe  *project.Error
		se  *store.Error
		ve  *pyerr.ValueError
		be  *store.BundleError
		ce  *auth.ConfigError
		cve *cliValueError
		oe  *org.Error
		me  *mobile.Error
		ue  *skillbrief.Uncaught
		ke  *skillkit.Error
		ske *skillkit.SkillError
	)
	switch {
	case errors.As(err, &pe), errors.As(err, &se), errors.As(err, &ve), errors.As(err, &be),
		errors.As(err, &ce), errors.As(err, &cve), errors.As(err, &ke), errors.As(err, &ske):
		eprintf("error: %s\n", err.Error())
		return 2
	case errors.As(err, &oe):
		eprintf("arkstudio.org.OrgError: %s\n", oe.Message)
	case errors.As(err, &me):
		eprintf("arkstudio.mobile.MobileError: %s\n", me.Message)
	case errors.As(err, &ue):
		eprintln(ue.Error())
	default:
		eprintf("error: %s\n", err.Error())
	}
	return 1
}

// cliValueError is a ValueError raised by CLI helpers (exit 2).
type cliValueError struct{ msg string }

func (e *cliValueError) Error() string { return e.msg }

func dispatch(parser *pyargparse.Parser, ns pyargparse.Namespace, argv []string) (int, error) {
	switch ns.Str("command") {
	case "server-init":
		return serverInit(ns)
	case "serve":
		return serveCommand(parser, ns, argv)
	case "mobile-tunnel":
		return mobileTunnel(ns)
	case "desktop":
		eprintln("error: the desktop window is part of the Python package (pywebview); run `python -m arkstudio desktop`, or start `ark-server serve` and open it in the Tauri shell")
		return 2, nil
	case "remote":
		return remote(ns)
	case "new":
		return newProject(ns.Str("root"), ns.Str("name"), ns.Str("description"), ns.Str("template"), false)
	case "demo":
		return newProject(ns.Str("root"), project.PredActorName, project.PredActorDescription, "demo", true)
	case "export":
		return exportCommand(ns)
	case "agent":
		if ns.Str("agent_command") == "context" {
			return agentContext(ns)
		}
	case "skills":
		return skillsCommand(ns)
	case "session-watch":
		return sessionWatch(ns)
	}
	parser.PrintHelp(stdout)
	return 0, nil
}
