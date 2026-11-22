// Command ark-server is the Go port of `python -m arkstudio`.
//
//	ark-server serve --root runtime/projects --host 127.0.0.1 --port 8890
//	ark-server new "My paper" --template paper
//	ark-server export --project runtime/projects/My-paper.ark --format markdown
//	ark-server routes [--json]     # contract routes this build implements
//	ark-server version
//
// Every subcommand and flag of arkstudio/__main__.py is parsed the same way
// (internal/compat/pyargparse runs Python's argparse algorithm over a snapshot of
// the Python parser), with the same defaults, environment fallbacks, output,
// exit codes and file effects; see cli.go and the "CLI and main-sync"
// section of docs/development/go-port/README.md for the exceptions.  `serve`
// also takes Go-only tuning flags, accepted only when spelled in full
// (docs/development/go-port/concurrency.md).  No subcommand means `serve`.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	_ "net/http/pprof" // only served when ARK_PPROF is set
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"arkstudio/go/internal/api/desktopgate"
	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/compat/pyargparse"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/server"
	"arkstudio/go/internal/store"
)

// version is set at release build time: -ldflags "-X main.version=<VERSION>".
var version = "2.0-go"

type options struct {
	config, root, host, sessionsRoot, publicURL, publicIP, webRoot string
	authUser, authPassword, authPasswordFile, authPasswordHash     string
	port, authSessionTTL                                           int
	requireAuth, noDemo, expose, detectPublicIP, trustProxy        bool
	mediaRoots                                                     []string
	readPool, heavyLimit, maxSubprocesses, maxInFlight             int
	requestTimeout                                                 time.Duration
}

func defaultRoot() string {
	if r := os.Getenv("ARK_ROOT"); r != "" {
		return r
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, "runtime", "projects")
}

func routes(argv []string) int {
	asJSON := len(argv) > 0 && argv[0] == "--json"
	items := append(append([]server.PortedRoute(nil), server.Ported...), server.GoOnlyRoutes...)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if asJSON {
		out := make([]map[string]any, len(items))
		for i, r := range items {
			out[i] = map[string]any{"id": r.ID, "methods": strings.Split(r.Methods, ",")}
		}
		json.NewEncoder(os.Stdout).Encode(out) //nolint:errcheck
		return 0
	}
	for _, r := range items {
		fmt.Println(fmt.Sprintf("%-16s", r.Methods), r.ID)
	}
	return 0
}

func serveOptions(ns pyargparse.Namespace) *options {
	host, _ := ns["host"].(string)
	timeout, _ := ns["request_timeout"].(time.Duration)
	return &options{
		config: ns.Str("config"), root: ns.Str("root"), host: host, sessionsRoot: ns.Str("sessions_root"),
		publicURL: ns.Str("public_url"), publicIP: ns.Str("public_ip"), webRoot: ns.Str("web_root"),
		authUser: ns.Str("auth_user"), authPassword: ns.Str("auth_password"),
		authPasswordFile: ns.Str("auth_password_file"), authPasswordHash: ns.Str("auth_password_hash"),
		port: int(ns.Int("port")), authSessionTTL: int(ns.Int("auth_session_ttl")),
		requireAuth: ns.Bool("require_auth"), noDemo: ns.Bool("no_demo"), expose: ns.Bool("expose"),
		detectPublicIP: ns.Bool("detect_public_ip"), trustProxy: ns.Bool("trust_proxy"),
		mediaRoots: ns.List("media_root"), readPool: int(ns.Int("read_pool")), heavyLimit: int(ns.Int("heavy_limit")),
		maxSubprocesses: int(ns.Int("max_subprocesses")), maxInFlight: int(ns.Int("max_in_flight")),
		requestTimeout: timeout,
	}
}

// serveCommand is the `serve` branch of __main__.main, including --config.
func serveCommand(parser *pyargparse.Parser, ns pyargparse.Namespace, argv []string) (int, error) {
	if ns.Str("config") != "" {
		cli, env, err := loadConfig(ns.Str("config"))
		if err != nil {
			return 0, err
		}
		for _, part := range argv {
			switch strings.SplitN(part, "=", 2)[0] {
			case "--auth-password", "--auth-password-file", "--auth-password-hash":
				delete(cli, "auth_password")
				delete(cli, "auth_password_file")
				delete(cli, "auth_password_hash")
			}
		}
		for _, key := range []string{"ARK_AUTH_USER", "ARK_AUTH_PASSWORD", "ARK_AUTH_PASSWORD_FILE", "ARK_AUTH_PASSWORD_HASH", "ARK_AUTH_SYNC"} {
			os.Unsetenv(key)
		}
		for k, v := range env {
			os.Setenv(k, v)
		}
		sub := parser.Sub("serve")
		for k, v := range cli {
			sub.Default(k, v)
		}
		if ns, err = parser.Parse(argv, stdout, stderr); err != nil {
			return exitCode(err), nil
		}
	}
	return serve(serveOptions(ns))
}

func serve(o *options) (int, error) {
	desktopSecret := desktopgate.SecretFromEnv() // before anything can spawn a child
	expose := o.expose || envBool("ARK_PUBLIC")
	host := pytext.Strip(o.host)
	if host == "" {
		host = pytext.Strip(os.Getenv("ARK_HOST"))
	}
	if host == "" {
		host = "127.0.0.1"
		if expose {
			host = "0.0.0.0"
		}
	}
	if expose && server.IsLoopbackHost(host) {
		eprintln("warning: --expose was requested together with a loopback --host; binding loopback only.")
		expose = false
	}
	authMgr, err := buildAuth(o)
	if err != nil {
		return 0, err
	}
	if o.requireAuth && authMgr == nil {
		return 0, &auth.ConfigError{Message: "Account authentication is required; configure an initial owner or restore the organization database"}
	}
	if !server.IsLoopbackHost(host) && authMgr == nil {
		eprintf("error: refusing to bind ARK to %s without account authentication. Set --auth-user and "+
			"ARK_AUTH_PASSWORD (or --auth-password-hash / --auth-password-file), or leave the default 127.0.0.1 bind. "+
			"ARK never exposes itself publicly without a sign-in.\n", host)
		return 2, nil
	}
	if o.authPassword != "" {
		eprintln("warning: --auth-password is visible to other processes; prefer ARK_AUTH_PASSWORD or ARK_AUTH_PASSWORD_HASH.")
	}
	app, err := server.NewApp(server.Config{
		Root: o.root, Host: host, Port: o.port, NoDemo: o.noDemo, MediaRoots: o.mediaRoots,
		SessionsRoot: o.sessionsRoot, PublicURL: o.publicURL, Expose: expose, PublicIP: o.publicIP,
		DetectPublicIP: o.detectPublicIP, TrustProxy: o.trustProxy, WebRoot: o.webRoot,
		ReadPool: o.readPool, RequestTimeout: o.requestTimeout, HeavyLimit: o.heavyLimit,
		SubprocessMax: o.maxSubprocesses, MaxInFlight: o.maxInFlight,
		ReleaseVersion: releaseVersion(),
		// The desktop app's sessions end with the app, as before.
		SessionRelay: desktopSecret == "",
	}, authMgr)
	if err != nil {
		return 0, err
	}
	defer app.Close()
	listener, err := net.Listen("tcp", net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(o.port)))
	if err != nil {
		eprintln("error:", err)
		return 1, nil
	}
	port := listener.Addr().(*net.TCPAddr).Port
	app.Bind(host, port)
	app.StartGPUQueue()                                         // serve(): app.gpu.start_configured_queue()
	app.StartSessionNudge(context.Background(), 30*time.Second) // Go only: wake idle ARK-launched agents (T-0053)
	var handler http.Handler = app
	if desktopSecret != "" {
		if !server.IsLoopbackHost(host) {
			eprintln("error: ARK_DESKTOP_SECRET is only supported on a loopback --host")
			return 2, nil
		}
		handler = desktopgate.New(app, desktopSecret, port, app.IssuedToken)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second,
		MaxHeaderBytes: 64 << 10}
	srv.RegisterOnShutdown(app.Streams.Close)
	printBanner(app, host, port, authMgr)
	if os.Getenv("ARK_DEBUG") != "" {
		go func() {
			for range time.Tick(10 * time.Second) {
				st := &store.Stats
				eprintf("snapshot hits=%d patches=%d rebuilds=%d gaps=%d\n",
					st.Hits.Load(), st.Patches.Load(), st.Rebuilds.Load(), st.Gaps.Load())
			}
		}()
	}
	if addr := os.Getenv("ARK_PPROF"); addr != "" && !loopbackAddr(addr) {
		eprintln("warning: ARK_PPROF must be a loopback address such as 127.0.0.1:6061; profiling disabled")
	} else if addr != "" {
		// Opt-in profiling on its own (loopback) listener, never on the app port.
		go func() {
			eprintln("pprof:", http.ListenAndServe(addr, http.DefaultServeMux))
		}()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if desktopSecret != "" {
		// The desktop app holds our stdin open; EOF means it quit (or died):
		// shut down gracefully instead of lingering as an orphan.
		go func() {
			io.Copy(io.Discard, os.Stdin) //nolint:errcheck
			stop()
		}()
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(listener) }()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			eprintln("error:", err)
			return 1, nil
		}
	case <-ctx.Done():
		// Graceful shutdown: stop accepting, let in-flight requests finish.
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown) //nolint:errcheck
	}
	return 0, nil
}

func printBanner(app *server.App, host string, port int, authMgr *auth.Manager) {
	loopback := server.IsLoopbackHost(host)
	pyprintf("ARK Studio (Go): http://%s:%d/\n", host, port)
	pyprintf("Project root: %s\n", app.Root)
	if authMgr != nil {
		pyprintf("Sign-in: required (user: %s; sessions expire after %ds)\n", authMgr.Username, authMgr.SessionTTL)
	} else {
		pyprint("Sign-in: not configured (loopback only)")
	}
	if authMgr != nil || !loopback {
		pyprint("Agent token: redacted (read runtime/projects/.agent-token)")
	} else {
		pyprintf("Agent token: %s\n", app.AgentToken())
	}
	if app.PublicURL() != "" {
		pyprintf("Mobile public URL: %s\n", app.PublicURL())
	}
	pyprint("Press Ctrl+C to stop.")
}

func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	return err == nil && server.IsLoopbackHost(host)
}
