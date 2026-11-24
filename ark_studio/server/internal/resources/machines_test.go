package resources

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/org"
)

// machineSources: the ARK server runs on a GPU server of its own list
// (gpu-lab), as on the production host; a member's laptop and a lab box.
func machineSources(t *testing.T, admin bool) *pyjson.Object {
	gpuServer := `{"name": "gpu-lab", "tags": [], "enabled": true}`
	if admin {
		gpuServer = `{"name": "gpu-lab", "host": "198.51.100.23", "port": 2022, "user": "user", "tags": [], "enabled": true}`
	}
	return obj(t, `{"now": 1800000000.0,
	  "server": {"hostname": "deskhost7k2pq", "platform": "linux", "fingerprint": "709270ec09fc", "disk": null, "load1": null},
	  "project": {"id": "p1", "name": "P", "path": "/srv/p", "db": "/srv/p/db"},
	  "sessions": null, "phones": [], "prefs": {}, "users": {},
	  "gpu": {"enabled": true, "servers": [`+gpuServer+`], "last_probe": null, "jobs": []},
	  "layers": {
	    "project": {"layer": "project", "profile": {"resources": [{"id": "repo", "kind": "repository", "name": "repo"}]},
	      "locations": [{"resource": "project/repo", "host_id": "709270ec09fc", "path_or_ref": "/data/repo"},
	                    {"resource": "project/repo", "host_id": "lab000000001", "host_label": "lab-1.example.com", "path_or_ref": "/srv/repo"}]},
	    "users": [{"layer": "user", "owner": "ana", "hosts": [{"id": "abc123abc123", "label": "laptop"}],
	      "profile": {"resources": [{"id": "x", "kind": "repository", "name": "x"}]},
	      "locations": [{"resource": "u:ana/x", "host_id": "abc123abc123", "host_label": "laptop", "path_or_ref": "/home/ana/x"}]}]}}`)
}

func machineGraph(t *testing.T, admin bool) (*pyjson.Object, *pyjson.Object) {
	t.Helper()
	src := machineSources(t, admin)
	graph, err := BuildGraph(src, "ana")
	if err != nil {
		t.Fatal(err)
	}
	return graph, src
}

func dumps(v any) string { return string(pyjson.Dumps(v)) }

func TestMachinesAutoMatch(t *testing.T) {
	graph, src := machineGraph(t, true)
	opts := ConnectOpts{ServerNames: []string{"ark-main.example.com"}, ServerAddrs: []string{"198.51.100.23"}}
	out := ConnectionsWith(graph, src, nil, opts)
	links := out.Value("links").(*pyjson.Object)
	// The ARK server's public host resolves to the GPU server's address.
	if got := dumps(links.Value("ark")); got != `{"machine": "gpu:gpu-lab", "how": "server"}` {
		t.Errorf("ark link %s", got)
	}
	if got := dumps(links.Value("gpu:gpu-lab")); got != `{"machine": "gpu:gpu-lab", "how": "self"}` {
		t.Errorf("gpu link %s", got)
	}
	if links.Has("host:abc123abc123") {
		t.Errorf("laptop linked: %s", dumps(links))
	}
	// The ARK server opens with the GPU server's address.
	if got := dumps(out.Value("devices").(*pyjson.Object).Value("ark")); got !=
		`{"host": "198.51.100.23", "user": "user", "port": 2022, "source": "gpu-config", "machine": "gpu:gpu-lab"}` {
		t.Errorf("ark connection %s", got)
	}
	// A setup host with the server's hostname follows the server's link.
	src2 := machineSources(t, true)
	users := src2.Value("layers").(*pyjson.Object).Value("users").([]any)
	rec := users[0].(*pyjson.Object)
	rec.Set("hosts", append(rec.Value("hosts").([]any), obj(t, `{"id": "709270ec0000", "label": "deskhost7k2pq"}`)))
	rec.Set("locations", append(rec.Value("locations").([]any),
		obj(t, `{"resource": "u:ana/x", "host_id": "709270ec0000", "host_label": "deskhost7k2pq", "path_or_ref": "/data/x"}`)))
	graph2, err := BuildGraph(src2, "ana")
	if err != nil {
		t.Fatal(err)
	}
	if got := dumps(ConnectionsWith(graph2, src2, nil, opts).Value("links").(*pyjson.Object).Value("host:709270ec0000")); got !=
		`{"machine": "gpu:gpu-lab", "how": "server"}` {
		t.Errorf("server-named host %s", got)
	}
	// Without the server's addresses: nothing to match, the public name is a suggestion.
	out = ConnectionsWith(graph, src, nil, ConnectOpts{ServerNames: []string{"ark-main.example.com"}})
	if out.Value("links").(*pyjson.Object).Has("ark") {
		t.Error("ark linked without an address match")
	}
	if got := dumps(out.Value("suggest")); got != `{"ark": {"host": "ark-main.example.com"}}` {
		t.Errorf("suggest %s", got)
	}

	// A device whose hostname is a shared record's host.
	m := Machines{obj(t, `{"host:lab000000001": {"name": "lab-1", "host": "lab-1.example.com", "user": "lab"}}`), pyjson.NewObject()}
	out = ConnectionsWith(graph, src, nil, ConnectOpts{Machines: m})
	if got := dumps(out.Value("links").(*pyjson.Object).Value("host:lab000000001")); got != `{"machine": "host:lab000000001", "how": "self"}` {
		t.Errorf("lab self %s", got)
	}
	// The address of the viewer's own entry for the laptop is the lab box.
	saved := obj(t, `{"host:abc123abc123": {"host": "LAB-1.example.com", "auth": "key"}}`)
	out = ConnectionsWith(graph, src, saved, ConnectOpts{Machines: m})
	if got := dumps(out.Value("links").(*pyjson.Object).Value("host:abc123abc123")); got != `{"machine": "host:lab000000001", "how": "address"}` {
		t.Errorf("address match %s", got)
	}

	// A saved link wins; an empty one means not linked.
	m = Machines{pyjson.NewObject(), obj(t, `{"host:abc123abc123": "gpu:gpu-lab", "ark": ""}`)}
	out = ConnectionsWith(graph, src, nil, ConnectOpts{Machines: m, ServerAddrs: []string{"198.51.100.23"}})
	links = out.Value("links").(*pyjson.Object)
	if got := dumps(links); got != `{"ark": {"machine": "", "how": "unlinked"}, "gpu:gpu-lab": {"machine": "gpu:gpu-lab", "how": "self"}, "host:abc123abc123": {"machine": "gpu:gpu-lab", "how": "saved"}}` {
		t.Errorf("saved links %s", got)
	}
}

func TestMachinesPrefillPrecedence(t *testing.T) {
	graph, src := machineGraph(t, true)
	// Shared project record over the GPU list; personal over shared; the
	// viewer's older entry for the device itself over both.
	m := Machines{obj(t, `{"gpu:gpu-lab": {"name": "gpu-lab", "port": 2200}}`), obj(t, `{"ark": "gpu:gpu-lab"}`)}
	saved := obj(t, `{"gpu:gpu-lab": {"alias": "ark-gpu-lab", "auth": "key"}, "ark": {"user": "ana"}}`)
	out := ConnectionsWith(graph, src, saved, ConnectOpts{Machines: m})
	devices := out.Value("devices").(*pyjson.Object)
	if got := dumps(devices.Value("ark")); got != `{"alias": "ark-gpu-lab", "host": "198.51.100.23", "user": "ana", "port": 2200, "auth": "key", "source": "mine", "machine": "gpu:gpu-lab"}` {
		t.Errorf("ark %s", got)
	}
	if got := dumps(devices.Value("gpu:gpu-lab")); got != `{"alias": "ark-gpu-lab", "host": "198.51.100.23", "user": "user", "port": 2200, "auth": "key", "source": "mine"}` {
		t.Errorf("gpu %s", got)
	}
	if got := dumps(out.Value("machines").(*pyjson.Object).Value("gpu:gpu-lab")); got != `{"name": "gpu-lab", "host": "198.51.100.23", "user": "user", "port": 2200, "source": "project", "shared": true}` {
		t.Errorf("machine %s", got)
	}
	if got := dumps(out.Value("mine")); got != dumps(saved) {
		t.Errorf("mine %s", got)
	}
	// Members: no server list address, but the project record is shared.
	graph, src = machineGraph(t, false)
	m = Machines{obj(t, `{"gpu:gpu-lab": {"host": "198.51.100.23", "port": 2022, "user": "user"}}`), pyjson.NewObject()}
	out = ConnectionsWith(graph, src, nil, ConnectOpts{Machines: m})
	if got := dumps(out.Value("devices").(*pyjson.Object).Value("gpu:gpu-lab")); got != `{"host": "198.51.100.23", "user": "user", "port": 2022, "source": "project"}` {
		t.Errorf("member gpu %s", got)
	}
	out = ConnectionsWith(graph, src, nil, ConnectOpts{})
	if out.Value("devices").(*pyjson.Object).Has("gpu:gpu-lab") {
		t.Error("member sees the server list address")
	}
}

func machineOrg(t *testing.T) *org.Manager {
	t.Helper()
	ctx := context.Background()
	o, err := org.Open(filepath.Join(t.TempDir(), "org.sqlite3"), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	if err := o.Bootstrap(ctx, "", "", false, nil); err != nil {
		t.Fatal(err)
	}
	if len(org.AvatarColors) == 0 {
		org.AvatarColors = []string{"#4f46e5"} // the server sets them from the static snapshot
	}
	if _, err := o.CreateUser(ctx, org.LocalUsername, pyjson.NewObject("username", "bob", "password", "correct horse battery", "role", "member")); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestSaveConnectionPermissionsAndMigration(t *testing.T) {
	ctx := context.Background()
	o := machineOrg(t)
	dir := t.TempDir()
	admin := org.LocalUsername
	graph, src := machineGraph(t, true)
	opts := ConnectOpts{ServerAddrs: []string{"198.51.100.23"}}
	req := func(user, device string, isAdmin bool, body string) SaveRequest {
		return SaveRequest{ProjectPath: dir, Username: user, DeviceID: device, Body: obj(t, body), Admin: isAdmin,
			Graph: graph, Sources: src, Opts: opts}
	}
	// An older personal entry for the ARK server device (before machines).
	if _, err := SetSSH(ctx, o, admin, "ark", obj(t, `{"host": "198.51.100.23", "user": "old", "port": 2022}`)); err != nil {
		t.Fatal(err)
	}

	// Members cannot change shared fields or links.
	for _, body := range []string{`{"shared": {"host": "evil.example.com"}}`, `{"link": "gpu:gpu-lab"}`} {
		_, err := SaveConnection(ctx, o, req("bob", "ark", false, body))
		if e, ok := err.(*Error); !ok || e.Status != 403 {
			t.Errorf("%s: %v", body, err)
		}
	}
	if _, err := os.Stat(machinesPath(dir)); err == nil {
		t.Fatal("a member wrote the shared file")
	}

	// The admin saves from the ARK server's dialog: shared values go to the
	// linked machine, the personal entry moves from the device to the machine.
	got, err := SaveConnection(ctx, o, req(admin, "ark", true,
		`{"alias": "ark-gpu-lab", "auth": "key", "shared": {"name": "gpu-lab", "host": "198.51.100.23", "port": 2022, "user": "user"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if dumps(got) != `{"device": "ark", "machine": "gpu:gpu-lab", "ssh": {"alias": "ark-gpu-lab", "auth": "key"}, "shared": {"name": "gpu-lab", "host": "198.51.100.23", "user": "user", "port": 2022}}` {
		t.Fatalf("save %s", dumps(got))
	}
	saved, _ := SSHSettings(ctx, o, admin)
	if dumps(saved) != `{"gpu:gpu-lab": {"alias": "ark-gpu-lab", "auth": "key"}}` {
		t.Fatalf("personal %s", dumps(saved))
	}
	m := ReadMachines(dir)
	if dumps(m.Records) != `{"gpu:gpu-lab": {"name": "gpu-lab", "host": "198.51.100.23", "user": "user", "port": 2022}}` {
		t.Fatalf("records %s", dumps(m.Records))
	}

	// A member now gets the address everywhere (GPU tab, graph, ARK server),
	// with their own user; their entry is theirs only.
	mgraph, msrc := machineGraph(t, false)
	if _, err := SaveConnection(ctx, o, SaveRequest{ProjectPath: dir, Username: "bob", DeviceID: "gpu:gpu-lab",
		Body: obj(t, `{"user": "bob", "auth": "password"}`), Graph: mgraph, Sources: msrc, Opts: opts}); err != nil {
		t.Fatal(err)
	}
	bobSaved, _ := SSHSettings(ctx, o, "bob")
	out := ConnectionsWith(mgraph, msrc, bobSaved, ConnectOpts{Machines: ReadMachines(dir), ServerAddrs: opts.ServerAddrs})
	devices := out.Value("devices").(*pyjson.Object)
	if got := dumps(devices.Value("ark")); got != `{"host": "198.51.100.23", "user": "bob", "port": 2022, "auth": "password", "source": "project", "machine": "gpu:gpu-lab"}` {
		t.Errorf("member ark %s", got)
	}
	if adminSaved, _ := SSHSettings(ctx, o, admin); dumps(adminSaved) != `{"gpu:gpu-lab": {"alias": "ark-gpu-lab", "auth": "key"}}` {
		t.Errorf("admin entry changed: %s", dumps(adminSaved))
	}

	// A personal entry without an address needs a machine that has one.
	if _, err := SaveConnection(ctx, o, req("bob", "host:abc123abc123", false, `{"user": "bob"}`)); err == nil {
		t.Error("an entry without any address was saved")
	}
	// Link the laptop to nothing explicitly, then to the GPU server.
	if _, err := SaveConnection(ctx, o, req(admin, "host:abc123abc123", true, `{"link": "gpu:nope"}`)); err == nil {
		t.Error("unknown machine accepted")
	}
	if _, err := SaveConnection(ctx, o, req(admin, "host:abc123abc123", true, `{"link": "gpu:gpu-lab"}`)); err != nil {
		t.Fatal(err)
	}
	if got := dumps(ReadMachines(dir).Links); got != `{"host:abc123abc123": "gpu:gpu-lab"}` {
		t.Errorf("links %s", got)
	}
	// Clearing the shared record falls back to the server list.
	if _, err := SaveConnection(ctx, o, req(admin, "gpu:gpu-lab", true, `{"alias": "ark-gpu-lab", "shared": null}`)); err != nil {
		t.Fatal(err)
	}
	if got := dumps(ReadMachines(dir).Records); got != `{}` {
		t.Errorf("records after clear %s", got)
	}
}

func TestCleanShared(t *testing.T) {
	if got, err := CleanShared(obj(t, `{"name": " gpu-lab ", "host": "198.51.100.23", "port": "2022", "user": "user"}`)); err != nil ||
		dumps(got) != `{"name": "gpu-lab", "host": "198.51.100.23", "user": "user", "port": 2022}` {
		t.Errorf("%s %v", dumps(got), err)
	}
	for _, in := range []string{`{"alias": "a"}`, `{"auth": "key"}`, `{"host": "-oX"}`, `{"name": "a\nb"}`, `{"key": "x"}`} {
		if _, err := CleanShared(obj(t, in)); err == nil {
			t.Errorf("%s accepted", in)
		}
	}
	if got, _ := CleanShared(obj(t, `{"name": ""}`)); got != nil {
		t.Error("empty record kept")
	}
}

// Regression (2026-11-24): an alias made from the ARK server's hostname
// (ark-deskhost7k2pq), saved by an older dialog, kept winning and sent VS
// Code to a Host no ~/.ssh/config had.  It is dropped (the machine's own
// alias is resolved on the client); a save writes the repaired entry.
func TestStaleDeviceAliasRepaired(t *testing.T) {
	graph, src := machineGraph(t, true)
	opts := ConnectOpts{ServerAddrs: []string{"198.51.100.23"}}
	saved := obj(t, `{"ark": {"alias": "ark-deskhost7k2pq", "auth": "key"}, "gpu:gpu-lab": {"alias": "ark-709270ec09fc"},
	  "host:abc123abc123": {"alias": "ark-laptop"}}`)
	out := ConnectionsWith(graph, src, saved, opts)
	devices := out.Value("devices").(*pyjson.Object)
	if got := dumps(devices.Value("ark")); got != `{"host": "198.51.100.23", "user": "user", "port": 2022, "auth": "key", "source": "gpu-config", "machine": "gpu:gpu-lab"}` {
		t.Errorf("ark %s", got)
	}
	if got := dumps(out.Value("mine")); got != `{"ark": {"auth": "key"}}` {
		t.Errorf("mine %s", got)
	}
	if dumps(saved.Value("ark")) != `{"alias": "ark-deskhost7k2pq", "auth": "key"}` {
		t.Error("the caller's entries were changed")
	}
	// The machine's own alias stays.
	ok := obj(t, `{"gpu:gpu-lab": {"alias": "ark-gpu-lab"}, "ark": {"alias": "my-prod"}}`)
	if got := dumps(ConnectionsWith(graph, src, ok, opts).Value("mine")); got != dumps(ok) {
		t.Errorf("kept %s", got)
	}
	if ManagedAlias("gpu-lab") != "ark-gpu-lab" || ManagedAlias(" Lab GPU #1 ") != "ark-lab-gpu-1" || ManagedAlias("ark-x") != "ark-x" || ManagedAlias("--") != "" {
		t.Error("ManagedAlias")
	}
	// A save writes the repaired entry back.
	ctx := context.Background()
	o := machineOrg(t)
	if _, err := SetSSH(ctx, o, org.LocalUsername, "ark", obj(t, `{"alias": "ark-deskhost7k2pq", "host": "198.51.100.23", "port": 2022}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveConnection(ctx, o, SaveRequest{ProjectPath: t.TempDir(), Username: org.LocalUsername, DeviceID: "ark",
		Body: obj(t, `{"auth": "key"}`), Graph: graph, Sources: src, Opts: opts}); err != nil {
		t.Fatal(err)
	}
	if got, _ := SSHSettings(ctx, o, org.LocalUsername); dumps(got) != `{"gpu:gpu-lab": {"auth": "key"}}` {
		t.Errorf("after save %s", dumps(got))
	}
}

// Setup is where a device is tied to its GPU server: a compute resource
// with gpu_server and device_id.  After that the device opens over SSH with
// the server list's address, with nothing more to configure.
func TestSetupComputeLinksDevice(t *testing.T) {
	src := machineSources(t, true)
	layers := src.Value("layers").(*pyjson.Object)
	project := layers.Value("project").(*pyjson.Object)
	compute := obj(t, `{"id": "gpu-lab", "kind": "compute", "name": "gpu-lab",
	  "details": {"kind": "ssh", "gpu_server": "gpu-lab", "use": "ask", "device_id": "709270ec09fc"}}`)
	project.Value("profile").(*pyjson.Object).Set("resources", append(project.Value("profile").(*pyjson.Object).Value("resources").([]any), compute))
	graph, err := BuildGraph(src, "ana")
	if err != nil {
		t.Fatal(err)
	}
	// No server addresses known: the setup link alone.
	out := ConnectionsWith(graph, src, nil, ConnectOpts{})
	if got := dumps(out.Value("links").(*pyjson.Object).Value("ark")); got != `{"machine": "gpu:gpu-lab", "how": "setup"}` {
		t.Errorf("ark %s", got)
	}
	if got := dumps(out.Value("devices").(*pyjson.Object).Value("ark")); got != `{"host": "198.51.100.23", "user": "user", "port": 2022, "source": "gpu-config", "machine": "gpu:gpu-lab"}` {
		t.Errorf("ark connection %s", got)
	}
	// A link saved in the dialog wins; an explicit "not linked" too.
	m := Machines{pyjson.NewObject(), obj(t, `{"ark": ""}`)}
	if got := dumps(ConnectionsWith(graph, src, nil, ConnectOpts{Machines: m}).Value("links").(*pyjson.Object).Value("ark")); got != `{"machine": "", "how": "unlinked"}` {
		t.Errorf("unlinked %s", got)
	}
	// A member's layer may only tie its own hosts.
	src2 := machineSources(t, true)
	user := src2.Value("layers").(*pyjson.Object).Value("users").([]any)[0].(*pyjson.Object)
	prof := user.Value("profile").(*pyjson.Object)
	prof.Set("resources", append(prof.Value("resources").([]any), compute.Copy()))
	graph2, _ := BuildGraph(src2, "ana")
	if got := ConnectionsWith(graph2, src2, nil, ConnectOpts{}).Value("links").(*pyjson.Object); got.Has("ark") {
		t.Errorf("member tied the server: %s", dumps(got))
	}
	own := obj(t, `{"id": "x", "kind": "compute", "details": {"kind": "ssh", "gpu_server": "gpu-lab", "device_id": "abc123abc123"}}`)
	prof.Set("resources", append(prof.Value("resources").([]any), own))
	graph2, _ = BuildGraph(src2, "ana")
	if got := dumps(ConnectionsWith(graph2, src2, nil, ConnectOpts{}).Value("links").(*pyjson.Object).Value("host:abc123abc123")); got != `{"machine": "gpu:gpu-lab", "how": "setup"}` {
		t.Errorf("own host %s", got)
	}
}

// The ARK server's own SSH entry comes from the whole server list (admins)
// in projects that register no GPU server for it.
func TestServerOwnSSHEntry(t *testing.T) {
	src := machineSources(t, true)
	src.Value("gpu").(*pyjson.Object).Set("servers", []any{})
	graph, _ := BuildGraph(src, "ana")
	opts := ConnectOpts{ServerAddrs: []string{"198.51.100.23"},
		ServerSSH: obj(t, `{"name": "gpu-lab", "host": "198.51.100.23", "port": 2022, "user": "user"}`)}
	out := ConnectionsWith(graph, src, nil, opts)
	if got := dumps(out.Value("devices").(*pyjson.Object).Value("ark")); got != `{"host": "198.51.100.23", "user": "user", "port": 2022, "source": "server"}` {
		t.Errorf("ark %s", got)
	}
	if got := dumps(out.Value("machines").(*pyjson.Object).Value("ark")); got != `{"name": "gpu-lab", "host": "198.51.100.23", "port": 2022, "user": "user", "source": "server", "shared": false}` {
		t.Errorf("machine %s", got)
	}
	// Members (no ServerSSH): nothing.
	if ConnectionsWith(graph, src, nil, ConnectOpts{ServerAddrs: opts.ServerAddrs}).Value("devices").(*pyjson.Object).Has("ark") {
		t.Error("member got the server's address")
	}
}
