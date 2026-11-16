package main

import (
	"context"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/mobile"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/pyargparse"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/server"
	"arkstudio/go/internal/store"
	"arkstudio/go/internal/webui"
)

const cliReaders = 2

// ---------------------------------------------------------------- server-init

func serverInit(ns pyargparse.Namespace) (int, error) {
	var password string
	if f := ns.Str("password_file"); f != "" {
		var err error
		if password, err = auth.ReadPasswordFile(f); err != nil {
			return 0, err
		}
	} else {
		first, err := getpass("Initial owner password: ")
		if err != nil {
			return 0, err
		}
		second, err := getpass("Confirm password: ")
		if err != nil {
			return 0, err
		}
		if first != second {
			return 0, &auth.ConfigError{Message: "Passwords do not match"}
		}
		password = first
	}
	result, err := initializeServer(ns.Str("config"), ns.Str("data_dir"), ns.Str("owner"), password,
		ns.Int("port"), ns.Str("public_url"))
	if err != nil {
		return 0, err
	}
	printBytes(pyjson.DumpsASCII(result))
	return 0, nil
}

// initializeServer is server_config.initialize.
func initializeServer(path, dataDir, username, password string, port int64, publicURL string) (*pyjson.Object, error) {
	target := project.Resolve(expandUser(path))
	data := project.Resolve(expandUser(dataDir))
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	nonEmpty := false
	if exists(data) {
		entries, err := os.ReadDir(data)
		if err != nil {
			return nil, err
		}
		nonEmpty = len(entries) > 0
	}
	if exists(target) || nonEmpty {
		return nil, &auth.ConfigError{Message: "Configuration or nonempty data directory already exists; initialization never overwrites an installation"}
	}
	username, err := auth.ValidateUsername(username)
	if err != nil {
		return nil, err
	}
	if pytext.Len(password) < 12 {
		return nil, &auth.ConfigError{Message: "Initial owner password must contain at least 12 characters"}
	}
	if port < 1 || port > 65535 {
		return nil, &auth.ConfigError{Message: "Server port must be between 1 and 65535"}
	}
	if publicURL != "" {
		if publicURL, err = mobile.NormalizePublicURL(publicURL); err != nil {
			return nil, err
		}
	}
	j := func(parts ...string) string { return filepath.Join(append([]string{data}, parts...)...) }
	values := pyjson.NewObject("root", j("projects"), "org_db", j("org.sqlite3"), "private_dir", j("private"),
		"sessions_root", j("agent-sessions"), "gpu_servers_config", j("private", "gpu_servers.yaml"),
		"gpu_queue_db", j("gpu-queue.sqlite3"), "blackbox_dir", j("blackbox"), "auth_user", username,
		"require_auth", true, "host", "127.0.0.1", "port", port, "public_url", publicURL, "no_demo", true)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(data, 0o700); err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		os.Chmod(data, 0o700) //nolint:errcheck
	}
	hash, err := auth.HashPassword(password, auth.Iterations)
	if err != nil {
		return nil, err
	}
	m, err := org.Open(filepath.Join(data, "org.sqlite3"), 1)
	if err != nil {
		return nil, err
	}
	err = m.Bootstrap(context.Background(), username, hash, false, nil)
	m.Close()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, werr := f.Write(store.TextFile(asciiJSON(pyjson.DumpsIndent(values, 2)) + "\n"))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, werr
	}
	return pyjson.NewObject("config", target, "data", data, "owner", username, "port", port), nil
}

// asciiJSON applies ensure_ascii=True to json.dumps(..., ensure_ascii=False) output.
func asciiJSON(b []byte) string {
	var out strings.Builder
	const hexdigits = "0123456789abcdef"
	esc := func(r rune) {
		out.WriteString(`\u`)
		out.WriteByte(hexdigits[r>>12&0xf])
		out.WriteByte(hexdigits[r>>8&0xf])
		out.WriteByte(hexdigits[r>>4&0xf])
		out.WriteByte(hexdigits[r&0xf])
	}
	for _, r := range string(b) {
		switch {
		case r < 0x80:
			out.WriteRune(r)
		case r > 0xffff:
			r -= 0x10000
			esc(0xd800 + r>>10)
			esc(0xdc00 + r&0x3ff)
		default:
			esc(r)
		}
	}
	return out.String()
}

// ---------------------------------------------------------------- new / demo

func newProject(root, name, description, template string, demo bool) (int, error) {
	useDemoAssets()
	m, err := project.NewManager(root, cliReaders)
	if err != nil {
		return 0, err
	}
	defer m.Close()
	p, err := m.Create(context.Background(), name, description, template, demo, "")
	if err != nil {
		return 0, err
	}
	summary, err := p.Summary()
	if err != nil {
		return 0, err
	}
	printBytes(pyjson.DumpsIndent(summary, 2))
	return 0, nil
}

// useDemoAssets points project creation at the repository's demo-assets
// folder beside web/ (Python reads it relative to the package), as the
// server does when it serves web/ from disk.
func useDemoAssets() {
	if project.DemoAssetsDir != "" {
		return
	}
	if a, err := webui.Locate(os.Getenv("ARK_WEB_ROOT")); err == nil && a.Dir != "" {
		dir := filepath.Join(filepath.Dir(a.Dir), "demo-assets")
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			project.DemoAssetsDir = dir
		}
	}
}

// openStore is __main__._open_store + manager.open(path).
func openStore(path string) (*project.Manager, *project.Project, error) {
	folder := project.Resolve(path)
	if _, ok := project.ReadManifest(folder); !ok {
		return nil, nil, &project.Error{Message: "not an ARK project: " + folder}
	}
	m, err := project.NewManager(filepath.Dir(folder), cliReaders)
	if err != nil {
		return nil, nil, err
	}
	p, err := m.Open(folder)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	return m, p, nil
}

// ---------------------------------------------------------------- export

func exportCommand(ns pyargparse.Namespace) (int, error) {
	m, p, err := openStore(ns.Str("project"))
	if err != nil {
		return 0, err
	}
	defer m.Close()
	ctx := context.Background()
	format := ns.Str("format")
	if format == "bundle" {
		return exportBundle(ctx, ns, p)
	}
	var data []byte
	switch format {
	case "json":
		obj, err := p.Store.ProjectJSON(ctx)
		if err != nil {
			return 0, err
		}
		data = pyjson.DumpsIndent(obj, 2)
	case "markdown", "csv", "svg":
		var text string
		switch format {
		case "markdown":
			text, err = p.Store.ProjectMarkdown(ctx)
		case "csv":
			text, err = p.Store.ProjectCSV(ctx)
		default:
			text, err = p.Store.GraphSVG(ctx)
		}
		if err != nil {
			return 0, err
		}
		data = []byte(text)
	}
	if out := ns.Str("out"); out != "" {
		if err := os.WriteFile(out, store.TextFile(string(data)), 0o644); err != nil {
			return 0, err
		}
		pyprint(out)
	} else {
		printBytes(data)
	}
	return 0, nil
}

func exportBundle(ctx context.Context, ns pyargparse.Namespace, p *project.Project) (int, error) {
	bundle := p.Store.NewBundle(p.Path, p.Name(), p.Store.LocalMediaResolver(p.Path, ns.List("media_root")))
	refs, err := store.ParseRefs(ns.Str("units"))
	if err != nil {
		return 0, err
	}
	if err := bundle.Collect(ctx, refs, ns.Int("depth")); err != nil {
		return 0, err
	}
	write := func(path string) (*pyjson.Object, error) {
		f, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		manifest, err := bundle.WriteZip(f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return manifest, err
	}
	out := ns.Str("out")
	isDir := false
	if out != "" {
		fi, err := os.Stat(out)
		isDir = err == nil && fi.IsDir()
	}
	var manifest *pyjson.Object
	if out == "" || isDir {
		folder := out
		if folder == "" {
			folder, _ = os.Getwd()
		}
		tmp := filepath.Join(folder, "bundle.zip")
		if manifest, err = write(tmp); err != nil {
			return 0, err
		}
		final := filepath.Join(folder, pyjson.Str(manifest.Value("root"))+".zip")
		if err := os.Rename(tmp, final); err != nil {
			return 0, err
		}
		out = final
	} else {
		if manifest, err = write(out); err != nil {
			return 0, err
		}
		out = pyPath(out)
	}
	files, _ := manifest.Value("files").([]any)
	nodes, _ := manifest.Value("nodes").([]any)
	missing := []any{}
	for _, f := range files {
		if o, ok := f.(*pyjson.Object); ok && pyjson.Truthy(o.Value("error")) {
			missing = append(missing, o)
		}
	}
	printBytes(pyjson.DumpsIndent(pyjson.NewObject("path", out, "nodes", int64(len(nodes)),
		"files", int64(len(files)-len(missing)), "missing", missing), 2))
	return 0, nil
}

// ---------------------------------------------------------------- agent context

func agentContext(ns pyargparse.Namespace) (int, error) {
	ctx := context.Background()
	var p *project.Project
	if path := ns.Str("project"); path != "" {
		m, opened, err := openStore(path)
		if err != nil {
			return 0, err
		}
		defer m.Close()
		p = opened
	} else {
		m, err := project.NewManager(ns.Str("root"), cliReaders)
		if err != nil {
			return 0, err
		}
		defer m.Close()
		projects := m.ListProjects()
		if len(projects) == 0 {
			eprintln("No projects. Run `python -m arkstudio serve` to create the demo.")
			return 2, nil
		}
		if p, err = m.Open(pyjson.Str(projects[0].Value("path"))); err != nil {
			return 0, err
		}
	}
	data, err := p.Store.AgentContext(ctx)
	if err != nil {
		return 0, err
	}
	printBytes(pyjson.DumpsIndent(data, 2))
	return 0, nil
}

// ---------------------------------------------------------------- skills

func skillsCommand(ns pyargparse.Namespace) (int, error) {
	command, _ := ns["skills_command"].(string)
	if command == "" {
		command = "list"
	}
	reindent := func(raw string) error {
		v, err := pyjson.Loads(raw)
		if err != nil {
			return err
		}
		printBytes(pyjson.DumpsIndent(v, 2))
		return nil
	}
	switch command {
	case "list":
		raw, _ := server.StaticPayload("skills_catalog")
		v, err := pyjson.Loads(raw)
		if err != nil {
			return 0, err
		}
		printBytes(pyjson.DumpsIndent(v.(*pyjson.Object).Value("skills"), 2))
		return 0, nil
	case "show":
		id := ns.Str("skill")
		raw, ok := server.StaticPayload("skill:" + pytext.Lower(pytext.Strip(id)))
		if !ok {
			return 0, &cliValueError{"unknown skill: " + id}
		}
		return 0, reindent(raw)
	case "interface":
		raw, _ := server.StaticPayload("skills_interface")
		return 0, reindent(raw)
	case "setup":
		return setupCommand(ns)
	case "brief":
		return briefCommand(ns)
	case "render":
		return renderCommand(ns)
	case "validate":
		return validateCommand(ns)
	}
	eprintf("error: `skills %s` is not ported to the Go CLI yet; run `python -m arkstudio skills %s`\n", command, command)
	return 2, nil
}

// ---------------------------------------------------------------- mobile-tunnel

var shlexUnsafe = regexp.MustCompile(`[^\w@%+=:,./-]`)

// shlexQuote is shlex.quote.
func shlexQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !shlexUnsafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// tunnelCommand is mobile.tunnel_command.
func tunnelCommand(target, bind string, localPort, arkPort, sshPort int64) ([]string, error) {
	lp, err := mobile.ValidatePort(localPort, "local port", 0, false)
	if err != nil {
		return nil, err
	}
	ap, err := mobile.ValidatePort(arkPort, "ARK port", 0, false)
	if err != nil {
		return nil, err
	}
	sp, err := mobile.ValidatePort(sshPort, "SSH port", 0, false)
	if err != nil {
		return nil, err
	}
	if bind == "" {
		bind = "0.0.0.0"
	}
	bind = pytext.Strip(bind)
	if _, err := netip.ParseAddr(strings.Trim(bind, "[]")); err != nil {
		return nil, &mobile.Error{Status: 400, Message: "tunnel bind address must be an IP address", Code: "INVALID_BIND"}
	}
	target = pytext.Strip(target)
	if target == "" {
		target = "user@server"
	}
	if strings.ContainsAny(target, "\r\n\t ") {
		return nil, &mobile.Error{Status: 400, Message: "SSH target must not contain whitespace", Code: "INVALID_SSH_TARGET"}
	}
	argv := []string{"ssh", "-N"}
	if sp != 22 {
		argv = append(argv, "-p", strconv.Itoa(sp))
	}
	argv = append(argv, "-L", bind+":"+strconv.Itoa(lp)+":127.0.0.1:"+strconv.Itoa(ap), target)
	return argv, nil
}

func mobileTunnel(ns pyargparse.Namespace) (int, error) {
	argv, err := tunnelCommand(ns.Str("ssh"), ns.Str("bind"), ns.Int("local_port"), ns.Int("ark_port"), ns.Int("ssh_port"))
	if err != nil {
		return 0, err
	}
	if id := ns.Str("identity"); id != "" {
		argv = append([]string{argv[0], "-i", pyPath(expandUser(id))}, argv[1:]...)
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		eprintf("error: OpenSSH client not found (%s)\n", argv[0])
		return 2, nil
	}
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shlexQuote(a)
	}
	pyprint("ARK mobile tunnel")
	pyprintf("  command: %s\n", strings.Join(quoted, " "))
	pyprintf("  LAN URL: http://<this-computer-LAN-IP>:%d/\n", ns.Int("local_port"))
	pyprint("  The phone must join the same network; the phone cannot run SSH itself.")
	pyprint("  WARNING: this listens on every interface. Use a trusted LAN, keep the")
	pyprint("  pairing token read-only unless you need writes, and stop the tunnel when done.")
	pyprint("  WARNING: plain HTTP on a LAN is not a secure context, so install/offline")
	pyprint("  caching may be unavailable in the phone browser. See docs/guide/mobile.md.")
	pyprint("Press Ctrl+C to close the tunnel.")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		eprintf("error: could not start ssh: %s\n", err)
		return 2, nil
	}
	waitChild(cmd, nil)
	return 0, nil
}

// waitChild waits for cmd; on Ctrl+C/SIGTERM it terminates the child, waits
// 5 s and kills it (the finally block of run_tunnel / remote.run).  poll, if
// set, runs every 500 ms and stops waiting when it returns false.
func waitChild(cmd *exec.Cmd, poll func() bool) {
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }() //nolint:errcheck
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	select {
	case <-done:
		return
	case <-sig:
	}
	terminate(cmd)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cmd.Process.Kill() //nolint:errcheck
		<-done
	}
}

func terminate(cmd *exec.Cmd) {
	if runtime.GOOS == "windows" {
		cmd.Process.Kill() //nolint:errcheck // Popen.terminate is TerminateProcess on Windows
		return
	}
	cmd.Process.Signal(syscall.SIGTERM) //nolint:errcheck
}

// ---------------------------------------------------------------- remote

func healthy(url string) bool {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(strings.TrimRight(url, "/") + "/api/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start() //nolint:errcheck // webbrowser.open ignores failures
}

// remote is remote.run.
func remote(ns pyargparse.Namespace) (int, error) {
	host := ns.Str("ssh")
	if host == "" {
		eprintln("--ssh host is required")
		return 2, nil
	}
	target := host
	if u := ns.Str("user"); u != "" {
		target = u + "@" + host
	}
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		eprintln("OpenSSH client (ssh) is not available")
		return 2, nil
	}
	local := ns.Int("local_port")
	args := []string{"-N", "-p", strconv.FormatInt(ns.Int("ssh_port"), 10),
		"-L", "127.0.0.1:" + strconv.FormatInt(local, 10) + ":127.0.0.1:" + strconv.FormatInt(ns.Int("remote_port"), 10)}
	if id := ns.Str("identity"); id != "" {
		args = append(args, "-i", pyPath(expandUser(id)))
	}
	args = append(args, target)
	tunnel := exec.Command(ssh, args...)
	tunnel.Stdin, tunnel.Stdout, tunnel.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := tunnel.Start(); err != nil {
		return 0, err
	}
	exited := make(chan struct{})
	go func() { tunnel.Wait(); close(exited) }() //nolint:errcheck
	alive := func() bool {
		select {
		case <-exited:
			return false
		default:
			return true
		}
	}
	var mirror *exec.Cmd
	defer func() {
		if mirror != nil && mirror.ProcessState == nil {
			terminate(mirror)
		}
		if alive() {
			terminate(tunnel)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	interrupted := func() bool {
		select {
		case <-sig:
			return true
		default:
			return false
		}
	}
	url := "http://127.0.0.1:" + strconv.FormatInt(local, 10) + "/"
	deadline := time.Now().Add(25 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if interrupted() {
			return 0, nil
		}
		if !alive() {
			eprintln("SSH tunnel exited before the ARK server became reachable")
			return 1, nil
		}
		if healthy(url) {
			ready = true
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !ready {
		eprintf("ARK server did not answer at %s\n", url)
		return 1, nil
	}
	if mount, rpath := ns.Str("mount"), ns.Str("remote_path"); mount != "" && rpath != "" {
		if sshfs, err := exec.LookPath("sshfs"); err == nil {
			dir := pyPath(expandUser(mount))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return 0, err
			}
			mirror = exec.Command(sshfs, target+":"+rpath, dir)
			mirror.Stdout, mirror.Stderr = os.Stdout, os.Stderr
			if err := mirror.Start(); err != nil {
				return 0, err
			}
			go mirror.Wait() //nolint:errcheck
			pyprintf("Mirrored %s at %s (sshfs pid %d)\n", rpath, mount, mirror.Process.Pid)
		} else {
			eprintln("sshfs is not installed; skipping the optional folder mirror")
		}
	}
	pyprintf("ARK remote project: %s\n", url)
	pyprint("Press Ctrl+C to close the tunnel.")
	if !ns.Bool("no_open") {
		openBrowser(url)
	}
	for {
		select {
		case <-exited:
			return 0, nil
		case <-sig:
			return 0, nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}
