package resources

import (
	"context"
	"path/filepath"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/org"
)

func obj(t *testing.T, text string) *pyjson.Object {
	t.Helper()
	o, ok := pyjson.LoadsObject(text)
	if !ok {
		t.Fatalf("not an object: %s", text)
	}
	return o
}

func TestCleanSSH(t *testing.T) {
	good := map[string]string{
		`{}`:                         `null`,
		`{"alias": "", "host": " "}`: `null`,
		`{"alias": "gpu-a"}`:         `{"alias": "gpu-a"}`,
		`{"host": "203.0.113.7", "user": "user", "port": 2222}`: `{"host": "203.0.113.7", "user": "user", "port": 2222}`,
		`{"host": "box.example.com", "port": "2222"}`:           `{"host": "box.example.com", "port": 2222}`,
		`{"host": "fe80::1%eth0", "user": "a.b_c-d"}`:           `{"host": "fe80::1%eth0", "user": "a.b_c-d"}`,
		`{"alias": "a", "host": "h", "port": 22.0}`:             `{"alias": "a", "host": "h", "port": 22}`,
		// How the viewer logs in: a key (in their own ~/.ssh) or a password they type.
		`{"host": "h", "auth": "key"}`:         `{"host": "h", "auth": "key"}`,
		`{"alias": "a", "auth": " password "}`: `{"alias": "a", "auth": "password"}`,
		`{"host": "h", "auth": ""}`:            `{"host": "h"}`,
	}
	for in, want := range good {
		got, err := CleanSSH(obj(t, in))
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		have := "null"
		if got != nil {
			have = string(pyjson.Dumps(got))
		}
		if have != want {
			t.Errorf("%s: %s, want %s", in, have, want)
		}
	}
	for _, in := range []string{
		`{"host": "-oProxyCommand=calc"}`, // an ssh option, not a host
		`{"host": "a b"}`,
		`{"host": "h;rm -rf ~"}`,
		`{"host": "h\nx"}`,
		`{"host": "h", "user": "-l"}`,
		`{"host": "h", "user": "a@b"}`,
		`{"host": "h", "user": "$(id)"}`,
		`{"alias": "a*"}`,
		`{"alias": "a b"}`,
		`{"host": "h", "port": 0.5}`,
		`{"host": "h", "port": 70000}`,
		`{"host": "h", "port": "22x"}`,
		`{"host": "h", "port": true}`,
		`{"user": "ana"}`, // no alias, no host
		`{"host": "h", "password": "x"}`,
		`{"host": "h", "identity_file": "~/.ssh/id"}`,
		`{"host": 5}`,
		`{"host": "h", "auth": "token"}`,
		`{"host": "h", "auth": true}`,
		`{"auth": "key"}`, // a mode without a host
	} {
		if _, err := CleanSSH(obj(t, in)); err == nil {
			t.Errorf("%s accepted", in)
		} else if e, ok := err.(*Error); !ok || e.Code != "RESOURCES_SSH_INVALID" {
			t.Errorf("%s: %v", in, err)
		}
	}
}

func connectSources(t *testing.T, admin bool) *pyjson.Object {
	gpuServer := `{"name": "gpu-a", "tags": [], "enabled": true}`
	if admin {
		gpuServer = `{"name": "gpu-a", "host": "10.0.0.5", "port": 2222, "user": "ana", "tags": [], "enabled": true}`
	}
	return obj(t, `{"now": 1800000000.0,
	  "server": {"hostname": "srv", "platform": "linux", "fingerprint": "fp0000000001", "disk": null, "load1": null},
	  "project": {"id": "p1", "name": "P", "path": "/srv/p", "db": "/srv/p/db"},
	  "sessions": null, "phones": [], "prefs": {}, "users": {},
	  "gpu": {"enabled": true, "servers": [`+gpuServer+`], "last_probe": null, "jobs": []},
	  "layers": {
	    "project": {"layer": "project", "profile": {"resources": [{"id": "repo", "kind": "repository", "name": "repo"}]},
	      "locations": [{"resource": "project/repo", "host_id": "fp0000000001", "path_or_ref": "/srv/repo"},
	                    {"resource": "project/repo", "host_id": "gpuhost00001", "host_label": "gpu-a", "path_or_ref": "/data/repo"}]},
	    "users": [{"layer": "user", "owner": "ana", "hosts": [{"id": "abc123abc123", "label": "laptop"}],
	      "profile": {"resources": [{"id": "x", "kind": "repository", "name": "x"}]},
	      "locations": [{"resource": "u:ana/x", "host_id": "abc123abc123", "host_label": "laptop", "path_or_ref": "/home/ana/x"}]}]}}`)
}

func TestConnections(t *testing.T) {
	saved := obj(t, `{"host:abc123abc123": {"alias": "laptop"}, "gpu:gpu-a": {"alias": "my-gpu"},
	  "host:gone": {"alias": "x"}}`)
	for _, admin := range []bool{true, false} {
		src := connectSources(t, admin)
		graph, err := BuildGraph(src, "ana")
		if err != nil {
			t.Fatal(err)
		}
		// Without saved entries: the GPU config's address, for admins only.
		got := string(pyjson.Dumps(Connections(graph, src, nil)))
		want := `{"devices": {}, "hosts": {"fp0000000001": "ark", "gpuhost00001": "gpu:gpu-a", "abc123abc123": "host:abc123abc123"}}`
		if admin {
			want = `{"devices": {"gpu:gpu-a": {"host": "10.0.0.5", "user": "ana", "port": 2222, "source": "gpu-config"}}, "hosts": {"fp0000000001": "ark", "gpuhost00001": "gpu:gpu-a", "abc123abc123": "host:abc123abc123"}}`
		}
		if got != want {
			t.Errorf("admin=%v:\n got %s\nwant %s", admin, got, want)
		}
		// The viewer's own entries win field by field over the shared values;
		// devices not in the graph get none.
		got = string(pyjson.Dumps(Connections(graph, src, saved).Value("devices")))
		want = `{"gpu:gpu-a": {"alias": "my-gpu", "host": "10.0.0.5", "user": "ana", "port": 2222, "source": "mine"}, "host:abc123abc123": {"alias": "laptop", "source": "mine"}}`
		if !admin {
			want = `{"gpu:gpu-a": {"alias": "my-gpu", "source": "mine"}, "host:abc123abc123": {"alias": "laptop", "source": "mine"}}`
		}
		if got != want {
			t.Errorf("admin=%v saved:\n got %s\nwant %s", admin, got, want)
		}
		if !VisibleDevice(graph, "ark") || !VisibleDevice(graph, "gpu:gpu-a") || VisibleDevice(graph, "host:gone") {
			t.Error("VisibleDevice")
		}
	}
	// A server list host that is not a plain host name is never handed out.
	src := connectSources(t, true)
	server := src.Value("gpu").(*pyjson.Object).Value("servers").([]any)[0].(*pyjson.Object)
	server.Set("host", "-oProxyCommand=x")
	graph, _ := BuildGraph(src, "ana")
	if d := Connections(graph, src, nil).Value("devices").(*pyjson.Object); d.Len() != 0 {
		t.Errorf("option host handed out: %s", pyjson.Dumps(d))
	}
}

func TestSetSSH(t *testing.T) {
	ctx := context.Background()
	o, err := org.Open(filepath.Join(t.TempDir(), "org.sqlite3"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if err := o.Bootstrap(ctx, "", "", false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := SetSSH(ctx, o, "nobody", "host:x", obj(t, `{"alias": "a"}`)); err == nil ||
		err.(*Error).Code != "RESOURCES_ACCOUNT_REQUIRED" {
		t.Fatalf("unknown account: %v", err)
	}
	user := org.LocalUsername
	if _, err := SetSSH(ctx, o, user, "bad/id", obj(t, `{"alias": "a"}`)); err == nil {
		t.Error("bad device id accepted")
	}
	if _, err := SetSSH(ctx, o, user, "host:x", obj(t, `{"host": "-oX"}`)); err == nil {
		t.Error("bad host accepted")
	}
	got, err := SetSSH(ctx, o, user, "host:x", obj(t, `{"host": "box", "user": "me", "port": 2222}`))
	if err != nil || string(pyjson.Dumps(got)) != `{"device": "host:x", "ssh": {"host": "box", "user": "me", "port": 2222}}` {
		t.Fatalf("%s %v", pyjson.Dumps(got), err)
	}
	saved, _ := SSHSettings(ctx, o, user)
	if string(pyjson.Dumps(saved)) != `{"host:x": {"host": "box", "user": "me", "port": 2222}}` {
		t.Fatalf("saved %s", pyjson.Dumps(saved))
	}
	if others, _ := SSHSettings(ctx, o, "someone-else"); others.Len() != 0 {
		t.Error("another person sees the entry")
	}
	got, err = SetSSH(ctx, o, user, "host:x", pyjson.NewObject())
	if err != nil || string(pyjson.Dumps(got)) != `{"device": "host:x", "ssh": null}` {
		t.Fatalf("clear: %s %v", pyjson.Dumps(got), err)
	}
	if saved, _ = SSHSettings(ctx, o, user); saved.Len() != 0 {
		t.Fatalf("cleared %s", pyjson.Dumps(saved))
	}
}
