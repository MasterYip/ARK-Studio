package gpu

// T-0018: per-project GPU server scope and the member view (scope.go).

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

const scopeConfig = `enabled: true
defaults:
  port: 2222
  user: alice
servers:
  - name: a100-box
    host: a.example.internal
    tags: [a100]
    note: "rack 3"
  - name: h20
    host: h20.example.internal
    port: 22
    tags: [h20]
  - name: spare
    host: spare.example.internal
queue:
  enabled: true
  poll_seconds: 300
  min_free_memory_mb: 0
  max_utilization_pct: 100
`

// scopeService is a Service over a temporary server list; the probe runner
// records the servers it was asked for and fails with an ssh error that
// quotes the address (like OpenSSH does).
func scopeService(t *testing.T, config string) (*Service, *[]string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gpu_servers.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARK_GPU_QUEUE_DB", filepath.Join(dir, "queue.sqlite3"))
	t.Setenv("ARK_GPU_PROBE", "")
	var mu sync.Mutex
	var probed []string
	svc := &Service{ConfigPathOverride: path, Replay: true,
		Runner: func(server *pyjson.Object, timeout int) CommandResult {
			mu.Lock()
			probed = append(probed, pyjson.Str(server.Value("name")))
			mu.Unlock()
			host := pyjson.Str(server.Value("host"))
			return CommandResult{ReturnCode: 255, ErrorKind: "unreachable",
				Message: "ssh: connect to host " + host + " port 22: Connection refused (alice@" + host + ")"}
		}}
	t.Cleanup(svc.Close)
	return svc, &probed
}

var (
	member     = Scope{Registered: []string{" A100-BOX "}}
	admin      = Scope{Registered: []string{"a100-box"}, Admin: true}
	unreg      = Scope{}
	unregAdmin = Scope{Admin: true}
)

func codeOf(err error) (int, string) {
	var ge *Error
	if errors.As(err, &ge) {
		return ge.Status, ge.Code
	}
	return 0, ""
}

func nameList(items any) string {
	var out []string
	list, _ := items.([]any)
	for _, item := range list {
		if o, ok := item.(*pyjson.Object); ok {
			n := o.Value("name")
			if n == nil {
				n = o.Value("server")
			}
			out = append(out, pyjson.Str(n))
		} else {
			out = append(out, pyjson.Str(item))
		}
	}
	return strings.Join(out, ",")
}

// noAddress fails when the JSON of v mentions any configured address,
// the ssh user or the config path.
func noAddress(t *testing.T, what string, v *pyjson.Object, path string) {
	t.Helper()
	text := string(pyjson.Dumps(v))
	for _, leak := range []string{"example.internal", "alice", `"host"`, `"port"`, `"user"`, "config_path",
		"ssh_preview", filepath.Base(path)} {
		if strings.Contains(text, leak) {
			t.Errorf("%s leaks %q: %s", what, leak, text)
		}
	}
}

func TestScopeAllows(t *testing.T) {
	cases := []struct {
		sc   Scope
		name string
		want bool
	}{
		{Unscoped(), "anything", true},
		{member, "a100-box", true},
		{member, "  a100-BOX", true},
		{member, "h20", false},
		{member, "", false},
		{unreg, "a100-box", false},
		{Scope{Registered: []string{""}}, "", false},
	}
	for _, tc := range cases {
		if got := tc.sc.Allows(tc.name); got != tc.want {
			t.Errorf("%+v.Allows(%q) = %v, want %v", tc.sc, tc.name, got, tc.want)
		}
	}
}

func TestServersForScopeAndView(t *testing.T) {
	svc, _ := scopeService(t, scopeConfig)
	cases := []struct {
		name      string
		sc        Scope
		servers   string
		admin     bool
		hint      bool
		available string
	}{
		{"unscoped (pre-T-0018)", Unscoped(), "a100-box,h20,spare", true, false, ""},
		{"member, registered", member, "a100-box", false, false, "h20,spare"},
		{"admin, registered", admin, "a100-box", true, false, "h20,spare"},
		{"member, nothing registered", unreg, "", false, true, "a100-box,h20,spare"},
		{"local/admin, nothing registered", unregAdmin, "", true, true, "a100-box,h20,spare"},
	}
	for _, tc := range cases {
		out, err := svc.ServersFor(tc.sc, "p1")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := nameList(out.Value("servers")); got != tc.servers {
			t.Errorf("%s: servers %q, want %q", tc.name, got, tc.servers)
		}
		if out.Value("count") != int64(len(out.Value("servers").([]any))) {
			t.Errorf("%s: count %v", tc.name, out.Value("count"))
		}
		if tc.admin {
			if !out.Has("config_path") || !out.Has("defaults") {
				t.Errorf("%s: admin view lost config_path/defaults", tc.name)
			}
			for _, item := range out.Value("servers").([]any) {
				if s := item.(*pyjson.Object); !s.Has("host") || !s.Has("port") || !s.Has("user") {
					t.Errorf("%s: admin server without address: %s", tc.name, pyjson.Dumps(s))
				}
			}
		} else {
			noAddress(t, tc.name, out, svc.ConfigPath())
			for _, item := range out.Value("servers").([]any) {
				if keys := strings.Join(item.(*pyjson.Object).Keys(), ","); keys != "name,tags,enabled" &&
					keys != "name,tags" {
					t.Errorf("%s: member server keys %s", tc.name, keys)
				}
			}
		}
		reg, _ := out.Value("registration").(*pyjson.Object)
		if tc.sc.All {
			if reg != nil {
				t.Errorf("%s: unscoped reply has registration", tc.name)
			}
			continue
		}
		if reg == nil {
			t.Fatalf("%s: no registration", tc.name)
		}
		if got := nameList(reg.Value("available")); got != tc.available {
			t.Errorf("%s: available %q, want %q", tc.name, got, tc.available)
		}
		if (reg.Value("hint_code") == HintNotRegistered) != tc.hint || (tc.hint && !pyjson.Truthy(reg.Value("hint"))) {
			t.Errorf("%s: hint %v", tc.name, pyjson.Dumps(reg))
		}
	}
	// A registered name the list does not know is reported, not shown.
	out, _ := svc.ServersFor(Scope{Registered: []string{"gone", "h20"}}, "p1")
	reg := out.Value("registration").(*pyjson.Object)
	if nameList(out.Value("servers")) != "h20" || nameList(reg.Value("missing")) != "gone" {
		t.Errorf("missing: %s", pyjson.Dumps(out))
	}
}

func TestStatusAndQueueStatusMemberView(t *testing.T) {
	svc, _ := scopeService(t, scopeConfig)
	for _, tc := range []struct {
		sc    Scope
		count int64
	}{{Unscoped(), 3}, {member, 1}, {admin, 1}, {unreg, 0}} {
		st, err := svc.StatusFor(tc.sc)
		if err != nil {
			t.Fatal(err)
		}
		if st.Value("server_count") != tc.count {
			t.Errorf("%+v: server_count %v, want %d", tc.sc, st.Value("server_count"), tc.count)
		}
		q, err := svc.QueueStatusFor(tc.sc)
		if err != nil {
			t.Fatal(err)
		}
		if tc.sc.Admin {
			if !st.Has("config_path") || !q.Has("config_path") {
				t.Errorf("%+v: admin lost config_path", tc.sc)
			}
			continue
		}
		noAddress(t, "status", st, svc.ConfigPath())
		noAddress(t, "queue status", q, svc.ConfigPath())
	}
	// An invalid list: members get a generic error (the parser text describes the file).
	bad, _ := scopeService(t, "servers:\n  - name: a\n    host: 'root@secret.example.internal'\n")
	st, _ := bad.StatusFor(member)
	if st.Value("config_error") != memberConfigError {
		t.Errorf("member config_error %v", st.Value("config_error"))
	}
	if st, _ := bad.StatusFor(admin); !strings.Contains(pyjson.Str(st.Value("config_error")), "servers[") {
		t.Errorf("admin config_error %v", st.Value("config_error"))
	}
	if _, err := bad.ProbeFor(member, nil, "p1"); err == nil || strings.Contains(err.Error(), "secret") ||
		strings.Contains(err.Error(), "gpu_servers") {
		t.Errorf("member probe error %v", err)
	}
}

func TestProbeForRefusalsAndScope(t *testing.T) {
	svc, probed := scopeService(t, scopeConfig)
	refusals := []struct {
		body *pyjson.Object
		sc   Scope
	}{
		{pyjson.NewObject("server", "h20"), member},
		{pyjson.NewObject("servers", []any{"a100-box", "h20"}), member},
		{pyjson.NewObject("server", "no-such-server"), member}, // unknown looks the same
		{pyjson.NewObject("server", "a100-box"), unreg},
		{pyjson.NewObject("server", "h20"), admin}, // admins are scoped too
	}
	for _, tc := range refusals {
		_, err := svc.ProbeFor(tc.sc, tc.body, "p1")
		if status, code := codeOf(err); status != 404 || code != CodeNotRegistered {
			t.Errorf("%s as %+v: %d %s (%v), want 404 %s", pyjson.Dumps(tc.body), tc.sc, status, code, err, CodeNotRegistered)
		}
	}
	if len(*probed) != 0 {
		t.Fatalf("refused probes reached ssh: %v", *probed)
	}
	// Nothing registered: an empty reply with the hint, nothing probed.
	out, err := svc.ProbeFor(unreg, nil, "p1")
	if err != nil || nameList(out.Value("servers")) != "" || len(*probed) != 0 {
		t.Fatalf("unregistered probe: %v %v %v", err, out, *probed)
	}
	if out.Value("registration").(*pyjson.Object).Value("hint_code") != HintNotRegistered {
		t.Errorf("no hint: %s", pyjson.Dumps(out))
	}
	// Registered: only that server is probed; the member reply has no address.
	out, err = svc.ProbeFor(member, nil, "p1")
	if err != nil || strings.Join(*probed, ",") != "a100-box" || nameList(out.Value("servers")) != "a100-box" {
		t.Fatalf("member probe: %v %v %v", err, *probed, out)
	}
	noAddress(t, "member probe", out, svc.ConfigPath())
	msg := pyjson.Str(out.Value("servers").([]any)[0].(*pyjson.Object).Value("error").(*pyjson.Object).Value("message"))
	if !strings.Contains(msg, "<host>") {
		t.Errorf("member probe error not scrubbed: %q", msg)
	}
	// The case-insensitive registration probes the configured spelling.
	*probed = nil
	if _, err := svc.ProbeFor(member, pyjson.NewObject("server", "A100-Box"), "p1"); err != nil || strings.Join(*probed, ",") != "a100-box" {
		t.Errorf("canonical name: %v %v", err, *probed)
	}
	// The admin reply keeps the address.
	out, _ = svc.ProbeFor(admin, nil, "p1")
	if r := out.Value("servers").([]any)[0].(*pyjson.Object); r.Value("host") != "a.example.internal" || !out.Has("config_path") {
		t.Errorf("admin probe: %s", pyjson.Dumps(out))
	}
	// The last probe as other projects see it.
	if last := svc.LastProbeFor(Scope{Registered: []string{"h20"}}); nameList(last.Value("servers")) != "" {
		t.Errorf("last probe leaks to another project: %s", pyjson.Dumps(last))
	}
	last := svc.LastProbeFor(member)
	if nameList(last.Value("servers")) != "a100-box" {
		t.Errorf("last probe for member: %s", pyjson.Dumps(last))
	}
	noAddress(t, "last probe", last, svc.ConfigPath())
	// Overview: registered count; the summary only when its probe was in scope.
	if o := svc.OverviewFor(member, "p1"); o.Value("servers") != int64(1) || o.Value("probed_at") == nil {
		t.Errorf("member overview: %s", pyjson.Dumps(o))
	}
	if o := svc.OverviewFor(Scope{Registered: []string{"h20"}}, "p2"); o.Value("servers") != int64(1) || o.Value("probed_at") != nil {
		t.Errorf("other project's overview shows the summary: %s", pyjson.Dumps(o))
	}
}

func TestDashboardForRegistration(t *testing.T) {
	svc, probed := scopeService(t, scopeConfig)
	out, err := svc.DashboardFor(unreg, "p1")
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := out.Value("registration").(*pyjson.Object)
	if reg == nil || reg.Value("hint_code") != HintNotRegistered || len(*probed) != 0 {
		t.Errorf("dashboard: %s (probed %v)", pyjson.Dumps(out), *probed)
	}
	if out.Value("probe").(*pyjson.Object).Has("registration") {
		t.Error("registration duplicated inside probe")
	}
}

func TestPrepareDispatchForViews(t *testing.T) {
	svc, _ := scopeService(t, scopeConfig)
	body := func(server string) *pyjson.Object {
		b := pyjson.NewObject("command", "python train.py")
		if server != "" {
			b.Set("server", server)
		}
		return b
	}
	for _, tc := range []struct {
		sc     Scope
		server string
		status int
		code   string
	}{
		{member, "h20", 404, CodeNotRegistered},
		{member, "zzz", 404, CodeNotRegistered},
		{unreg, "", 403, CodeNoneRegistered},
		{unregAdmin, "", 403, CodeNoneRegistered},
	} {
		_, err := svc.PrepareDispatchFor(tc.sc, body(tc.server))
		if status, code := codeOf(err); status != tc.status || code != tc.code {
			t.Errorf("%+v %q: %d %s, want %d %s", tc.sc, tc.server, status, code, tc.status, tc.code)
		}
	}
	// One registered server: it is the default; the member gets no address.
	out, err := svc.PrepareDispatchFor(member, body(""))
	if err != nil {
		t.Fatal(err)
	}
	noAddress(t, "member dispatch", out, svc.ConfigPath())
	if out.Value("request").(*pyjson.Object).Value("server") != "a100-box" {
		t.Errorf("member dispatch: %s", pyjson.Dumps(out))
	}
	out, err = svc.PrepareDispatchFor(admin, body("a100-box"))
	if err != nil || !strings.Contains(pyjson.Str(out.Value("ssh_preview")), "alice@a.example.internal") {
		t.Errorf("admin dispatch: %v %s", err, pyjson.Dumps(out))
	}
	// Disabled probing: the member error does not name the config path.
	off, _ := scopeService(t, strings.Replace(scopeConfig, "enabled: true", "enabled: false", 1))
	_, err = off.PrepareDispatchFor(member, body(""))
	if status, code := codeOf(err); status != 403 || code != "disabled" || strings.Contains(err.Error(), "gpu_servers") {
		t.Errorf("disabled member: %d %s %v", status, code, err)
	}
	if _, err := off.PrepareDispatchFor(admin, body("")); !strings.Contains(err.Error(), "gpu_servers.yaml") {
		t.Errorf("disabled admin keeps the path: %v", err)
	}
}

func TestQueueSubmitForRefusals(t *testing.T) {
	svc, _ := scopeService(t, scopeConfig)
	job := func(server string) *pyjson.Object {
		b := pyjson.NewObject("command", "python train.py")
		if server != "" {
			b.Set("server", server)
		}
		return b
	}
	for _, tc := range []struct {
		sc     Scope
		body   *pyjson.Object
		status int
		code   string
	}{
		{member, job("h20"), 404, CodeNotRegistered},
		{member, job("nope"), 404, CodeNotRegistered},
		{unreg, job(""), 403, CodeNoneRegistered},
		{unreg, job("a100-box"), 404, CodeNotRegistered},
	} {
		_, err := svc.QueueSubmitFor(tc.sc, tc.body, "ana", "p1", "", "")
		if status, code := codeOf(err); status != tc.status || code != tc.code {
			t.Errorf("%+v %s: %d %s (%v), want %d %s", tc.sc, pyjson.Dumps(tc.body), status, code, err, tc.status, tc.code)
		}
	}
	// Validation errors keep their order (an invalid job is not a scope error).
	if _, err := svc.QueueSubmitFor(unreg, pyjson.NewObject("command", ""), "ana", "p1", "", ""); err == nil ||
		strings.Contains(err.Error(), "registered") {
		t.Errorf("invalid job as unregistered: %v", err)
	}
	out, err := svc.QueueSubmitFor(member, job("A100-BOX"), "ana", "p1", "", "")
	if err != nil || out.Value("server") != "a100-box" || out.Value("project") != "p1" {
		t.Fatalf("member submit: %v %v", err, out)
	}
	if _, err := svc.QueueSubmitFor(member, job(""), "ana", "p1", "", ""); err != nil {
		t.Errorf("member submit without server: %v", err)
	}
	// Migration aid: the project's jobs name a server it has not registered.
	out, _ = svc.ServersFor(Scope{Registered: []string{"h20"}}, "p1")
	if got := nameList(out.Value("registration").(*pyjson.Object).Value("suggested")); got != "a100-box" {
		t.Errorf("suggested %q, want a100-box", got)
	}
	if out, _ = svc.ServersFor(unreg, "other"); nameList(out.Value("registration").(*pyjson.Object).Value("suggested")) != "" {
		t.Error("another project's jobs are suggested")
	}
}

// The worker places a job only on its project's registered servers.
func TestQueueTickPlacesOnRegisteredServersOnly(t *testing.T) {
	svc, _ := scopeService(t, scopeConfig)
	svc.AllowedFor = func(project, owner string) Scope {
		switch project {
		case "p-h20":
			return Scope{Registered: []string{"h20"}}
		case "p-spare":
			return Scope{Registered: []string{"SPARE"}}
		}
		return Scope{}
	}
	q, err := svc.queueService()
	if err != nil {
		t.Fatal(err)
	}
	q.ProbeRunner = func(server *pyjson.Object, timeout int) CommandResult {
		return CommandResult{Stdout: "0, NVIDIA A100, 81920, 0, 0, 0, 30, 50\n"}
	}
	q.CommandRunner = func(server *pyjson.Object, command string, timeout int) CommandResult {
		return CommandResult{Stdout: "4242\n"}
	}
	ids := map[string]string{}
	for _, project := range []string{"p-h20", "p-spare", "p-none"} {
		job, err := q.Submit(pyjson.NewObject("command", "python x.py", "project", project, "owner", "ana",
			"min_free_memory_mb", int64(1)), false)
		if err != nil {
			t.Fatal(err)
		}
		ids[project] = pyjson.Str(job.Value("id"))
	}
	if err := q.Tick(); err != nil {
		t.Fatal(err)
	}
	for project, want := range map[string]string{"p-h20": "h20", "p-spare": "spare", "p-none": ""} {
		job, err := q.Get(ids[project])
		if err != nil {
			t.Fatal(err)
		}
		server := pyjson.Str(orEmpty(job.Value("server")))
		if server != want || (want == "" && job.Value("status") != "queued") {
			t.Errorf("%s: placed on %q (%v), want %q", project, server, job.Value("status"), want)
		}
	}
}
