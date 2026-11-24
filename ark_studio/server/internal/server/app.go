package server

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/blackbox"
	"arkstudio/go/internal/mobile"

	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/platform/procx"
	"arkstudio/go/internal/presence"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/providers"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/store"
	"arkstudio/go/internal/stream"
	"arkstudio/go/internal/webui"
	"arkstudio/go/internal/workspace"
	"arkstudio/go/internal/worktrees"
)

// Config is the resolved `serve` configuration (CLI + env + --config).
type Config struct {
	Root           string
	Host           string
	Port           int
	NoDemo         bool
	MediaRoots     []string
	SessionsRoot   string
	PublicURL      string
	Expose         bool
	PublicIP       string
	DetectPublicIP bool
	TrustProxy     bool
	WebRoot        string // Go only: serve web/ from this folder
	// Go only: the release version (ldflags), shown in Settings › About
	// through <meta name="ark-version">; empty for development builds.
	ReleaseVersion string
	// Go only: agent sessions run under a relay and survive a server
	// restart (Linux; internal/sessions/relay.go).  Off for the desktop app.
	SessionRelay bool
	// Go only: standby mode (readonly.go): API writes are refused and the
	// web app shows a banner.  StandbyStatus is the replication status file.
	ReadOnly      bool
	StandbyStatus string

	// Concurrency knobs (Go only, docs/development/go-port/concurrency.md).
	ReadPool       int           // read connections per project database
	RequestTimeout time.Duration // per-request deadline for non-streaming routes
	HeavyLimit     int           // concurrent heavy reads (graph/units/project)
	SubprocessMax  int           // concurrent child processes (git, agents, ssh)
	MaxInFlight    int           // admitted requests; beyond this callers wait
}

// App is server.App: process-wide state shared by every request.
type App struct {
	Cfg      Config
	Root     string
	Projects *project.Manager
	Org      *org.Manager
	Auth     *auth.Manager // nil = loopback mode without accounts
	Assets   *webui.Assets

	BindHost     string
	Port         int
	allowedHosts map[string]bool
	extraHosts   map[string]bool
	publicURL    string

	mu              sync.Mutex
	project         *project.Project  // local mode: the open project
	sessionProjects map[string]string // auth mode: sid hash -> project path
	Blackbox        *blackbox.BlackBox
	Mobile          *mobile.Manager
	appRelease      releaseResolver // Go-only: latest GitHub release of the APKs
	apks            apkCache        // Go-only: hash and version of the dist/ APKs (app_manifest.go)
	restoreMu       sync.Mutex      // App.lock around blackbox restore

	Heavy      *procx.Pool
	Subprocess *procx.Pool
	Admission  *procx.Pool
	responses  *responseCache
	// Streams ends every long-lived stream on shutdown (internal/stream);
	// cmd/ark-server registers Streams.Close with RegisterOnShutdown.
	Streams   *stream.Hub
	Sessions  *sessions.Manager
	Providers *providers.Catalog // read-only agent CLI catalog
	Worktrees *worktrees.Manager // agent worktrees (bound per request)
	Terminals *workspace.Manager // the Editor's terminal registry
	failures  *failureLimiter
	StartedAt string
	activity  projectActivity   // my_activity per project (project_activity.go)
	writers   lastWriters       // latest writer per project (project_people.go)
	reviews   projectNeedsCards // Needs you cards per project (project_reviews.go)
	// Presence: which agent is on which node, memory only (routes_presence.go, Go only).
	Presence *presence.Hub
	invites  inviteLimiter // public invite routes (routes_invites.go, Go only)
	// Phones through a Cloudflare Tunnel (mobile_tunnel.go, Go only).
	cfTunnel mobileTunnel
}

// NewApp is App.__init__ for the ported subset.
func NewApp(cfg Config, authMgr *auth.Manager) (*App, error) {
	// List previews name a video's poster frame only when one can be made.
	store.PosterFrames = VideoPosters()
	if cfg.ReadPool <= 0 {
		cfg.ReadPool = max(8, 2*runtime.NumCPU())
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.HeavyLimit <= 0 {
		// Heavy builds must never hold every read connection, or cheap reads
		// (revision polling, unit detail) queue behind them.
		cfg.HeavyLimit = max(1, min(runtime.NumCPU()/2, cfg.ReadPool/2))
	}
	if cfg.SubprocessMax <= 0 {
		cfg.SubprocessMax = max(2, runtime.NumCPU()/2)
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = 4096
	}
	assets, err := webui.Locate(cfg.WebRoot)
	if err != nil {
		return nil, err
	}
	manager, err := project.NewManager(cfg.Root, cfg.ReadPool)
	if err != nil {
		return nil, err
	}
	a := &App{Cfg: cfg, Root: manager.Root, Projects: manager, Auth: authMgr, Assets: assets,
		BindHost: "127.0.0.1", allowedHosts: map[string]bool{}, extraHosts: map[string]bool{},
		sessionProjects: map[string]string{}, responses: newResponseCache(), Streams: stream.NewHub(), Terminals: workspace.NewManager(),
		failures: &failureLimiter{buckets: map[string][]float64{}},
		Heavy:    procx.NewPool(cfg.HeavyLimit), Subprocess: procx.NewPool(cfg.SubprocessMax),
		Admission: procx.NewPool(cfg.MaxInFlight), Presence: presence.New()}
	a.StartedAt = time.Now().UTC().Format("2006-01-02T15:04:05") + "+00:00"
	if a.Assets != nil && a.Assets.Dir != "" {
		if dir := project.FindDemoAssets(a.Assets.Dir); dir != "" {
			project.DemoAssetsDir = dir
		}
	}
	if project.DemoAssetsDir == "" && project.DemoAssetsFS == nil {
		project.DemoAssetsFS = webui.EmbeddedDemo()
	}
	a.Mobile = mobile.New(filepath.Join(a.Root, ".mobile-sessions.json"))
	if authMgr != nil {
		if err := authMgr.EnablePersistence(a.Root); err != nil {
			return nil, err
		}
	}
	sessionsRoot := cfg.SessionsRoot
	if sessionsRoot == "" {
		sessionsRoot = filepath.Join(filepath.Dir(a.Root), "agent-sessions")
	}
	if a.Sessions, err = sessions.Open(sessionsRoot); err != nil {
		return nil, fmt.Errorf("agent sessions: %w", err)
	}
	a.Sessions.Relay = a.Sessions.Relay && cfg.SessionRelay
	presets, err := providers.DefaultPresets()
	if err != nil {
		return nil, fmt.Errorf("task presets: %w", err)
	}
	a.Providers = providers.NewCatalog(presets, a.Subprocess)
	a.Worktrees = worktrees.New(nil)
	a.configureSessions()
	if a.Org, err = org.Open(org.DefaultPath(a.Root), 4); err != nil {
		return nil, fmt.Errorf("organization database: %w", err)
	}
	if authMgr != nil {
		authMgr.SetAccounts(orgAccounts{a.Org})
	}
	a.loadProjectStorage(context.Background()) // routes_storage.go
	ctx := context.Background()
	if os.Getenv("ARK_REPLAY_CLOCK") == "1" {
		project.ReplayIDs.Store(true)
		project.SeedDatesFixed.Store(true)
	}
	if !cfg.NoDemo && len(manager.ListProjects()) == 0 {
		// ProjectManager(seed_demo=True): the first-run demo project.
		if a.project, err = manager.Create(ctx, project.PredActorName, project.PredActorDescription, "demo", true, ""); err != nil {
			return nil, err
		}
	}
	summaries := manager.ListProjects()
	if authMgr != nil {
		sync := pytext.Strip(strings.ToLower(os.Getenv("ARK_AUTH_SYNC")))
		err = a.Org.Bootstrap(ctx, authMgr.Username, authMgr.PasswordHash, sync == "1" || sync == "true" || sync == "yes" || sync == "on", summaries)
	} else {
		err = a.Org.Bootstrap(ctx, "", "", false, summaries)
	}
	if err != nil {
		return nil, fmt.Errorf("organization bootstrap: %w", err)
	}
	a.recoverMoves() // routes_projectmove.go: moves a restart interrupted
	raw := pytext.Strip(cfg.PublicURL)
	if raw == "" {
		raw = pytext.Strip(os.Getenv("ARK_PUBLIC_URL"))
	}
	if raw != "" {
		if a.publicURL, err = NormalizePublicURL(raw); err != nil {
			return nil, fmt.Errorf("invalid public URL: %w", err)
		}
	}
	a.configureAuthTransport()
	for _, item := range strings.Split(os.Getenv("ARK_ALLOWED_HOSTS"), ",") {
		if t := strings.ToLower(pytext.Strip(item)); t != "" {
			a.extraHosts[t] = true
		}
	}
	if a.publicURL != "" {
		a.allowAdvertisedURL(a.publicURL)
	}
	if len(summaries) == 0 {
		// App.__init__ without --no-demo created PredActor before the org
		// bootstrap (ProjectManager(seed_demo=True)); with it, a blank project.
		if a.project, err = manager.Create(ctx, "My Research", "", "blank", false, ""); err != nil {
			return nil, err
		}
	} else if a.project != nil {
		// created before the org bootstrap
	} else if a.project, err = manager.Open(pyjson.Str(summaries[0].Value("path"))); err != nil {
		return nil, err
	}
	// Python runs the one-time v5 creator-assignee backfill on the first
	// request (App.require).  On a large project it scans activity once per
	// unassigned unit and every first request waits for it; run it before
	// the listener opens instead.  Same result, only earlier.
	boot := &reqCtx{app: a, ctx: ctx}
	if err := a.project.Store.BackfillCreatorAssignees(ctx, boot.accountName); err != nil {
		return nil, fmt.Errorf("creator-assignee backfill: %w", err)
	}
	if err := a.adoptLegacyChat(ctx, a.project); err != nil {
		return nil, fmt.Errorf("legacy chat owner: %w", err)
	}
	a.setupBlackbox(ctx)
	// Agent sessions the previous server left running (relay.go).
	if attached, ended := a.Sessions.Resume(); attached+ended > 0 {
		fmt.Fprintf(os.Stderr, "agent sessions: %d re-attached, %d ended while the server was down\n", attached, ended)
	}
	return a, nil
}

// Close releases every database.  Relayed agent sessions keep running;
// the next server re-attaches them.
func (a *App) Close() {
	a.closeTunnel() // mobile_tunnel.go: cloudflared ends with the server
	if a.Sessions != nil {
		a.Sessions.Detach()
	}
	a.Terminals.Shutdown()
	a.Projects.Close()
	a.Org.Close()
}

type orgAccounts struct{ m *org.Manager }

func (o orgAccounts) Account(ctx context.Context, username string) (string, string, bool, error) {
	row, ok, err := o.m.Account(ctx, username)
	if err != nil || !ok {
		return "", "", false, err
	}
	return row.Str("username"), row.Str("password_hash"), true, nil
}

// cookiePathFromURL is auth.cookie_path_from_url.
func cookiePathFromURL(public string) string {
	u, err := url.Parse(pytext.Strip(public))
	if public == "" || err != nil {
		return "/"
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	return auth.NormalizeCookiePath(p)
}

func (a *App) configureAuthTransport() {
	if a.Auth == nil || a.publicURL == "" {
		return
	}
	a.Auth.CookiePath = cookiePathFromURL(a.publicURL)
	a.Auth.Secure = strings.HasPrefix(strings.ToLower(a.publicURL), "https://")
}

// NormalizePublicURL is mobile.normalize_public_url.
func NormalizePublicURL(value string) (string, error) {
	text := pytext.Strip(value)
	if text == "" {
		return "", fmt.Errorf("public URL is empty")
	}
	if pytext.Len(text) > 500 || strings.ContainsAny(text, "\r\n\t ") {
		return "", fmt.Errorf("public URL contains whitespace")
	}
	u, err := url.Parse(text)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("public URL must start with http:// or https://")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("public URL has no host")
	}
	if u.User != nil {
		return "", fmt.Errorf("public URL must not contain credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("public URL must not contain a query or fragment")
	}
	port := 0
	if p := u.Port(); p != "" {
		if port, err = strconv.Atoi(p); err != nil || port < 1 || port > 65535 {
			return "", fmt.Errorf("public URL port is out of range")
		}
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	netloc := host
	if port != 0 {
		netloc += ":" + strconv.Itoa(port)
	}
	return u.Scheme + "://" + netloc + path, nil
}

// allowAdvertisedURL is App.allow_advertised_url.
func (a *App) allowAdvertisedURL(raw string) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return
	}
	host := strings.ToLower(u.Hostname())
	if a.isTunnelHostname(host) {
		return // only the tunnel listener answers the tunnel's name (tunnel_gate.go)
	}
	port := 80
	if u.Scheme == "https" {
		port = 443
	}
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.allowedHosts[host+":"+strconv.Itoa(port)] = true
	a.allowedHosts[host] = true
}

// IsLoopbackHost is mobile.is_loopback_host.
func IsLoopbackHost(host string) bool {
	text := strings.ToLower(strings.Trim(pytext.Strip(host), "[]"))
	return text == "localhost" || text == "127.0.0.1" || text == "::1" || text == "0:0:0:0:0:0:0:1"
}

// hostAllowed is App.host_allowed (DNS-rebinding guard).
func (a *App) hostAllowed(hostHeader string) bool {
	host := pytext.Strip(hostHeader)
	if host == "" {
		return false
	}
	normalized := strings.ToLower(host)
	a.mu.Lock()
	allowed := a.allowedHosts[normalized]
	a.mu.Unlock()
	if allowed || a.extraHosts[normalized] {
		return true
	}
	if hostname := urlHostname(normalized); hostname != "" {
		a.mu.Lock()
		allowed = a.allowedHosts[hostname]
		a.mu.Unlock()
		if allowed || a.extraHosts[hostname] {
			return true
		}
	}
	if !IsLoopbackHost(a.BindHost) {
		return a.publicURL == ""
	}
	return false
}

// urlHostname is urlparse("//" + host).hostname.
func urlHostname(hostport string) string {
	netloc := hostport
	if i := strings.IndexAny(netloc, "/?#"); i >= 0 {
		netloc = netloc[:i]
	}
	if i := strings.LastIndex(netloc, "@"); i >= 0 {
		netloc = netloc[i+1:]
	}
	if strings.HasPrefix(netloc, "[") {
		if end := strings.Index(netloc, "]"); end > 0 {
			return strings.ToLower(netloc[1:end])
		}
		return ""
	}
	if i := strings.Index(netloc, ":"); i >= 0 {
		netloc = netloc[:i]
	}
	return strings.ToLower(netloc)
}

// urlPort is urlparse("//" + host).port (ok=false: none or invalid).
func urlPort(hostport string) (int, bool, bool) {
	netloc := hostport
	if strings.HasPrefix(netloc, "[") {
		end := strings.Index(netloc, "]")
		if end < 0 {
			return 0, false, false
		}
		netloc = netloc[end+1:]
	} else if i := strings.Index(netloc, ":"); i >= 0 {
		netloc = netloc[i:]
	} else {
		return 0, false, true
	}
	_, p, found := strings.Cut(netloc, ":")
	if !found || p == "" {
		return 0, false, true
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 0 || n > 65535 {
		return 0, false, false
	}
	return n, true, true
}

// Bind records the listening address (serve()).
func (a *App) Bind(host string, port int) {
	a.BindHost, a.Port = host, port
	a.mu.Lock()
	for _, h := range []string{host, "127.0.0.1", "localhost"} {
		a.allowedHosts[h+":"+strconv.Itoa(port)] = true
	}
	if host == "::1" || host == "[::1]" {
		a.allowedHosts["[::1]:"+strconv.Itoa(port)] = true
	}
	a.mu.Unlock()
	a.configureAuthTransport()
	if a.publicURL != "" {
		a.allowAdvertisedURL(a.publicURL)
	}
}

// AgentToken is ProjectManager.agent_token (printed on loopback start).
func (a *App) AgentToken() string { return a.Projects.AgentToken }

// PublicURL returns the normalized advertised URL.
func (a *App) PublicURL() string { return a.publicURL }

// splitHostPort tolerates a missing port.
func splitHostPort(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
