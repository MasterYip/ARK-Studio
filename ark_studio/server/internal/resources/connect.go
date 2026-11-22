package resources

// SSH connections of the Resources graph's devices (Go only, 2026-11-22):
// how the viewer reaches a device to open a location in VS Code (Remote-SSH)
// or an SSH terminal.  Two sources, never a key or a password:
//
//   gpu-config  the GPU server list (host, port, user); the GPU service gives
//               them to admins only (gpu.ScopedSource), so members see none
//   mine        the viewer's own entry for a device (org settings scope
//               "ssh_hosts"): an ~/.ssh/config alias and/or host, user, port
//
// Only devices of the viewer's graph (what the viewer can see) get one, and
// a saved entry is only ever returned to the person who saved it.

import (
	"context"
	"regexp"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/org"
)

// SSHSettingsScope is the org settings scope of the viewer's saved entries.
const SSHSettingsScope = "ssh_hosts"

// MaxSSHEntries caps the saved entries per person.
const MaxSSHEntries = 200

var (
	// A host name or an IPv4 / IPv6 address; never an option ("-o...").
	sshHostRe = regexp.MustCompile(`\A[A-Za-z0-9_][A-Za-z0-9._:%-]{0,252}\z`)
	sshUserRe = regexp.MustCompile(`\A[A-Za-z0-9_][A-Za-z0-9._-]{0,63}\z`)
	// An ~/.ssh/config Host alias (no patterns).
	sshAliasRe = regexp.MustCompile(`\A[A-Za-z0-9_][A-Za-z0-9._-]{0,99}\z`)
)

// sshKinds are the device kinds one can SSH into.
var sshKinds = map[string]bool{"ark": true, "gpu-server": true, "host": true}

// CleanSSH validates an entry {alias?, host?, user?, port?}: nil (and no
// error) when it is empty, an *Error when a field is invalid.
func CleanSSH(raw *pyjson.Object) (*pyjson.Object, error) {
	if raw == nil {
		return nil, nil
	}
	bad := func(msg string) error { return &Error{400, msg, "RESOURCES_SSH_INVALID"} }
	for _, k := range raw.Keys() {
		switch k {
		case "alias", "host", "user", "port":
		default:
			return nil, bad("an SSH connection has only alias, host, user and port")
		}
	}
	field := func(key string, re *regexp.Regexp, what string) (string, error) {
		v := raw.Value(key)
		if v == nil {
			return "", nil
		}
		s, ok := v.(string)
		if !ok {
			return "", bad(what + " must be text")
		}
		s = strings.TrimSpace(s)
		if s != "" && !re.MatchString(s) {
			return "", bad("invalid " + what)
		}
		return s, nil
	}
	alias, err := field("alias", sshAliasRe, "SSH alias")
	if err != nil {
		return nil, err
	}
	host, err := field("host", sshHostRe, "host")
	if err != nil {
		return nil, err
	}
	user, err := field("user", sshUserRe, "user name")
	if err != nil {
		return nil, err
	}
	var port int64
	switch p := raw.Value("port").(type) {
	case nil:
	case int64:
		port = p
	case float64:
		if p != float64(int64(p)) {
			return nil, bad("port must be a whole number")
		}
		port = int64(p)
	case string:
		if strings.TrimSpace(p) != "" {
			n, ok := atoi(strings.TrimSpace(p))
			if !ok {
				return nil, bad("port must be a number")
			}
			port = n
		}
	default:
		return nil, bad("port must be a number")
	}
	if port != 0 && (port < 1 || port > 65535) {
		return nil, bad("port must be between 1 and 65535")
	}
	if alias == "" && host == "" {
		if user != "" || port != 0 {
			return nil, bad("give an SSH alias or a host")
		}
		return nil, nil
	}
	out := pyjson.NewObject()
	if alias != "" {
		out.Set("alias", alias)
	}
	if host != "" {
		out.Set("host", host)
	}
	if user != "" {
		out.Set("user", user)
	}
	if port != 0 {
		out.Set("port", port)
	}
	return out, nil
}

func atoi(s string) (int64, bool) {
	if s == "" || len(s) > 5 {
		return 0, false
	}
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int64(r-'0')
	}
	return n, true
}

// SSHSettings is the viewer's saved entries {device id: entry}.
func SSHSettings(ctx context.Context, o *org.Manager, username string) (*pyjson.Object, error) {
	if o == nil || username == "" {
		return pyjson.NewObject(), nil
	}
	user, ok, err := o.UserByName(ctx, username)
	if err != nil || !ok {
		return pyjson.NewObject(), err
	}
	row, ok, err := o.SettingsRow(ctx, user.Str("id"), SSHSettingsScope)
	if err != nil {
		return nil, err
	}
	if !ok || !pyjson.Truthy(row.Get("data_json")) {
		return pyjson.NewObject(), nil
	}
	data, _ := pyjson.Loads(pyjson.Str(row.Get("data_json")))
	out := pyjson.NewObject()
	if d, ok := data.(*pyjson.Object); ok {
		if devices, ok := d.Value("devices").(*pyjson.Object); ok {
			for _, id := range devices.Keys() {
				// Re-validate: only clean entries ever leave the store.
				entry, _ := devices.Value(id).(*pyjson.Object)
				if clean, err := CleanSSH(entry); err == nil && clean != nil && deviceIDRe.MatchString(id) {
					out.Set(id, clean)
				}
			}
		}
	}
	return out, nil
}

// SetSSH saves (or, for an empty entry, removes) the viewer's entry for a
// device; it returns {"device": id, "ssh": entry|null}.
func SetSSH(ctx context.Context, o *org.Manager, username, deviceID string, raw *pyjson.Object) (*pyjson.Object, error) {
	if !deviceIDRe.MatchString(deviceID) {
		return nil, &Error{400, "invalid device id", "RESOURCES_INVALID"}
	}
	entry, err := CleanSSH(raw)
	if err != nil {
		return nil, err
	}
	if o == nil || username == "" {
		return nil, &Error{400, "SSH connections need a signed-in account", "RESOURCES_ACCOUNT_REQUIRED"}
	}
	user, ok, err := o.UserByName(ctx, username)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &Error{400, "SSH connections need a signed-in account", "RESOURCES_ACCOUNT_REQUIRED"}
	}
	devices, err := SSHSettings(ctx, o, username)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		devices.Delete(deviceID)
	} else {
		if !devices.Has(deviceID) && devices.Len() >= MaxSSHEntries {
			return nil, &Error{400, "too many saved SSH connections", "RESOURCES_SSH_LIMIT"}
		}
		devices.Set(deviceID, entry)
	}
	if err := o.SaveSettings(ctx, user.Str("id"), SSHSettingsScope, pyjson.NewObject("devices", devices), nil); err != nil {
		return nil, err
	}
	var out any
	if entry != nil {
		out = entry
	}
	return pyjson.NewObject("device", deviceID, "ssh", out), nil
}

// gpuDeviceIDs maps a GPU server's name and host (lower case) to its graph
// device id, as buildGraph does.
func gpuDeviceIDs(sources *pyjson.Object) map[string]string {
	out := map[string]string{}
	gpu := asObj(sources.Value("gpu"))
	for _, item := range iterate(gpu.Value("servers")) {
		cfg, ok := item.(*pyjson.Object)
		if !ok {
			continue
		}
		name := or(cfg.Value("name"), cfg.Value("host"))
		id := "gpu:" + slug(name)
		out[pytext.Lower(pyjson.Str(name))] = id
		if pyjson.Truthy(cfg.Value("host")) {
			out[pytext.Lower(pyjson.Str(cfg.Value("host")))] = id
		}
	}
	return out
}

// HostDevice is the graph device id of a setup host id (the rule of
// buildGraph's hostDevice): the ARK server, a GPU server of the same name,
// else "host:<id>".
func HostDevice(sources *pyjson.Object, hostID, label string) string {
	server := asObj(sources.Value("server"))
	if fp := server.Value("fingerprint"); fp != nil && pyEq(hostID, fp) {
		return "ark"
	}
	if legacy := server.Value("legacy_fingerprint"); legacy != nil && pyEq(hostID, legacy) {
		return "ark"
	}
	gpus := gpuDeviceIDs(sources)
	if id := gpus[pytext.Lower(hostID)]; id != "" {
		return id
	}
	if id := gpus[pytext.Lower(label)]; id != "" && label != "" {
		return id
	}
	return "host:" + hostID
}

// Connections is GET /api/resources/connect: {"devices": {id: {alias?, host?,
// user?, port?, source}}, "hosts": {setup host id: device id}} for the
// devices of `graph` (BuildGraph of the same sources for the same viewer).
func Connections(graph, sources, saved *pyjson.Object) *pyjson.Object {
	visible := map[string]bool{}
	for _, d := range iterate(graph.Value("devices")) {
		dev, ok := d.(*pyjson.Object)
		if !ok {
			continue
		}
		id := pyjson.Str(dev.Value("id"))
		if sshKinds[pyjson.Str(dev.Value("kind"))] {
			visible[id] = true
		}
	}
	devices := pyjson.NewObject()
	gpu := asObj(sources.Value("gpu"))
	for _, item := range iterate(gpu.Value("servers")) {
		cfg, ok := item.(*pyjson.Object)
		if !ok {
			continue
		}
		id := "gpu:" + slug(or(cfg.Value("name"), cfg.Value("host")))
		host, _ := cfg.Value("host").(string)
		if !visible[id] || host == "" || devices.Has(id) {
			continue
		}
		raw := pyjson.NewObject("host", host)
		if u, ok := cfg.Value("user").(string); ok && u != "" {
			raw.Set("user", u)
		}
		if p, ok := cfg.Value("port").(int64); ok && p != 0 {
			raw.Set("port", p)
		}
		// A server list entry that is not a plain host (e.g. an option) is skipped.
		if clean, err := CleanSSH(raw); err == nil && clean != nil {
			devices.Set(id, clean.Set("source", "gpu-config"))
		}
	}
	if saved != nil {
		for _, id := range saved.Keys() {
			entry, _ := saved.Value(id).(*pyjson.Object)
			if !visible[id] || entry == nil {
				continue
			}
			devices.Set(id, entry.Copy().Set("source", "mine"))
		}
	}
	hosts := pyjson.NewObject()
	layers := asObj(sources.Value("layers"))
	records := []any{layers.Value("project")}
	records = append(records, iterate(layers.Value("users"))...)
	for _, r := range records {
		record, ok := r.(*pyjson.Object)
		if !ok {
			continue
		}
		for _, l := range iterate(record.Value("locations")) {
			loc, ok := l.(*pyjson.Object)
			if !ok {
				continue
			}
			hostID := pyjson.Str(or(loc.Value("host_id"), ""))
			if hostID == "" || hosts.Has(hostID) {
				continue
			}
			if id := HostDevice(sources, hostID, pyjson.Str(or(loc.Value("host_label"), ""))); visible[id] {
				hosts.Set(hostID, id)
			}
		}
	}
	return pyjson.NewObject("devices", devices, "hosts", hosts)
}

// VisibleDevice reports whether the graph has a device one can SSH into.
func VisibleDevice(graph *pyjson.Object, deviceID string) bool {
	for _, d := range iterate(graph.Value("devices")) {
		if dev, ok := d.(*pyjson.Object); ok && dev.Value("id") == deviceID {
			return sshKinds[pyjson.Str(dev.Value("kind"))]
		}
	}
	return false
}

// asObj is v as an object, else an empty one (never panics).
func asObj(v any) *pyjson.Object {
	if o, ok := v.(*pyjson.Object); ok && o != nil {
		return o
	}
	return pyjson.NewObject()
}
