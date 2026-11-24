package resources

// Machines (Go only, 2026-11-24): one shared connection record per machine,
// so a server's address is entered once and the device SSH dialog, the GPU
// tab and the graph's "Open in VS Code / SSH" all read it from one place.
//
//   <project>/setup/machines.json
//     {"version": 1,
//      "machines": {"<machine id>": {"name"?, "host"?, "user"?, "port"?}},
//      "links":    {"<device id>": "<machine id>" | ""}}
//
// A machine id is the graph device id of the machine's own entry: a GPU
// server ("gpu:<name>") or a device ("ark", "host:<setup host id>").
//
//   shared    name, host, port and default user: project admins edit them,
//             project members read them.  For a GPU server the server list
//             (gpu_servers.yaml, admins only) is the base and a project
//             record overrides its fields.
//   personal  the viewer's org settings entry (connect.go, scope ssh_hosts):
//             alias, user, auth, and host / port overrides, keyed by the
//             machine id (old entries keyed by a linked device still count
//             and win).  Key files stay on the person's computer; passwords
//             never reach ARK.
//
// A device (the ARK server, a setup host) is linked to one machine: a saved
// link, else matched by its hostname (the machine's name or host), by the
// address of the viewer's own entry, or, for the ARK server, by its public
// host name and addresses.  An empty saved link means "not linked".

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/platform/textfile"
)

// MachinesFile is the shared record file in the project's setup folder.
const MachinesFile = "machines.json"

// MaxMachines caps the shared records (and the links) per project.
const MaxMachines = 200

var machinesMu sync.Mutex

func machinesPath(projectPath string) string {
	return filepath.Join(setupDir(projectPath), MachinesFile)
}

// Machines are a project's shared records and device links.
type Machines struct {
	Records *pyjson.Object // machine id -> {name?, host?, user?, port?}
	Links   *pyjson.Object // device id -> machine id ("" = not linked)
}

// CleanShared validates a shared record {name?, host?, port?, user?}: nil
// when it is empty.
func CleanShared(raw *pyjson.Object) (*pyjson.Object, error) {
	out, err := cleanConn(raw, sharedKeys, "a shared machine record has only name, host, port and user")
	if err != nil || out == nil || out.Len() == 0 {
		return nil, err
	}
	return out, nil
}

// ReadMachines reads the project's records (invalid entries are skipped).
func ReadMachines(projectPath string) Machines {
	out := Machines{pyjson.NewObject(), pyjson.NewObject()}
	if projectPath == "" {
		return out
	}
	data := readJSON(machinesPath(projectPath))
	if data == nil {
		return out
	}
	if recs, ok := data.Value("machines").(*pyjson.Object); ok {
		for _, id := range recs.Keys() {
			r, _ := recs.Value(id).(*pyjson.Object)
			if clean, err := CleanShared(r); err == nil && clean != nil && deviceIDRe.MatchString(id) {
				out.Records.Set(id, clean)
			}
		}
	}
	if links, ok := data.Value("links").(*pyjson.Object); ok {
		for _, id := range links.Keys() {
			v, ok := links.Value(id).(string)
			if ok && deviceIDRe.MatchString(id) && (v == "" || deviceIDRe.MatchString(v)) {
				out.Links.Set(id, v)
			}
		}
	}
	return out
}

// WriteMachines replaces the project's record file.
func WriteMachines(projectPath string, m Machines) error {
	path := machinesPath(projectPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data := pyjson.NewObject("version", int64(1), "machines", m.Records, "links", m.Links)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, textfile.Encode(string(pyjson.DumpsIndent(data, 2))+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ConnectOpts are what Connections needs besides the graph and sources.
type ConnectOpts struct {
	// Machines are the project's shared records (ReadMachines).
	Machines Machines
	// ServerNames are the ARK server's public host names (the request's
	// host, the configured public URL), lower case.
	ServerNames []string
	// ServerAddrs are those names' addresses and the server's own, lower case.
	ServerAddrs []string
	// ServerSSH, for admins, is the server list entry whose host is one of
	// ServerAddrs ({name, host, port?, user?}): the ARK server's own SSH
	// address even when the project registers no GPU server for it.
	ServerSSH *pyjson.Object
}

// connState is the resolved view of one viewer's devices and machines.
type connState struct {
	order   []string          // SSH-able graph devices, graph order
	labels  map[string]string // device id -> label
	kinds   map[string]string // device id -> kind
	catalog *pyjson.Object    // machine id -> {name, host?, user?, port?, source, shared}
	links   map[string][2]string
	saved   *pyjson.Object
	// hostname is the ARK server's host name (lower case).
	hostname string
	// serverIDs are the ARK server's setup host ids (fingerprints).
	serverIDs []string
}

func lowerStr(v any) string { return pytext.Lower(strings.TrimSpace(pyjson.Str(or(v, "")))) }

// resolve builds the catalog and every visible device's link.
func resolve(graph, sources, saved *pyjson.Object, opts ConnectOpts) *connState {
	st := &connState{labels: map[string]string{}, kinds: map[string]string{}, catalog: pyjson.NewObject(), links: map[string][2]string{}, saved: saved,
		hostname: lowerStr(asObj(sources.Value("server")).Value("hostname"))}
	for _, k := range []string{"fingerprint", "legacy_fingerprint"} {
		if fp, ok := asObj(sources.Value("server")).Value(k).(string); ok && fp != "" {
			st.serverIDs = append(st.serverIDs, fp)
		}
	}
	if st.saved == nil {
		st.saved = pyjson.NewObject()
	}
	m := opts.Machines
	if m.Records == nil {
		m.Records = pyjson.NewObject()
	}
	if m.Links == nil {
		m.Links = pyjson.NewObject()
	}
	for _, d := range iterate(graph.Value("devices")) {
		dev, ok := d.(*pyjson.Object)
		if !ok || !sshKinds[pyjson.Str(dev.Value("kind"))] {
			continue
		}
		id := pyjson.Str(dev.Value("id"))
		st.order = append(st.order, id)
		st.labels[id] = pyjson.Str(or(dev.Value("label"), id))
		st.kinds[id] = pyjson.Str(dev.Value("kind"))
	}
	// GPU servers of the (viewer-scoped) server list.
	gpu := asObj(sources.Value("gpu"))
	for _, item := range iterate(gpu.Value("servers")) {
		cfg, ok := item.(*pyjson.Object)
		if !ok {
			continue
		}
		name := or(cfg.Value("name"), cfg.Value("host"))
		id := "gpu:" + slug(name)
		if _, seen := st.labels[id]; !seen || st.catalog.Has(id) {
			continue
		}
		entry := pyjson.NewObject("name", pyjson.Str(name))
		raw := pyjson.NewObject()
		for _, k := range []string{"host", "user", "port"} {
			if v := cfg.Value(k); v != nil && v != "" {
				raw.Set(k, v)
			}
		}
		// A server list value that is not a plain host / user is never handed out.
		if clean, err := CleanShared(raw); err == nil && clean != nil {
			for _, k := range clean.Keys() {
				entry.Set(k, clean.Value(k))
			}
		}
		st.catalog.Set(id, entry.Set("source", "gpu-config").Set("shared", false))
	}
	// Project records of visible devices and of their link targets.
	wanted := map[string]bool{}
	for _, id := range st.order {
		wanted[id] = true
		if v, ok := m.Links.Value(id).(string); ok && v != "" {
			wanted[v] = true
		}
	}
	for _, id := range m.Records.Keys() {
		if !wanted[id] {
			continue
		}
		rec := m.Records.Value(id).(*pyjson.Object)
		entry, _ := st.catalog.Value(id).(*pyjson.Object)
		if entry == nil {
			label := st.labels[id]
			if label == "" {
				label = id
			}
			entry = pyjson.NewObject("name", label)
		}
		for _, k := range rec.Keys() {
			entry.Set(k, rec.Value(k))
		}
		st.catalog.Set(id, entry.Set("source", "project").Set("shared", true))
	}
	server := map[string]bool{}
	for _, s := range append(append([]string{}, opts.ServerNames...), opts.ServerAddrs...) {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			server[s] = true
		}
	}
	setupLinks := st.setupLinks(sources)
	for _, id := range st.order {
		machine, how := st.match(id, m, server)
		// Setup says which GPU server a device is (compute device_id); a link
		// saved in the dialog still wins.
		if mid := setupLinks[id]; mid != "" && how != "saved" && (how != "unlinked" || !m.Links.Has(id)) && mid != id {
			machine, how = mid, "setup"
		}
		if how != "" {
			st.links[id] = [2]string{machine, how}
		}
	}
	// The ARK server's own entry in the server list (admins).
	if _, ok := st.labels["ark"]; ok && opts.ServerSSH != nil && st.links["ark"][1] != "saved" && st.links["ark"][1] != "unlinked" && st.links["ark"][0] == "" {
		if clean, err := CleanShared(opts.ServerSSH); err == nil && clean != nil && clean.Has("host") {
			entry := pyjson.NewObject("name", pyjson.Str(or(clean.Value("name"), st.labels["ark"])))
			for _, k := range []string{"host", "port", "user"} {
				if clean.Has(k) {
					entry.Set(k, clean.Value(k))
				}
			}
			if rec, ok := m.Records.Value("ark").(*pyjson.Object); ok {
				for _, k := range rec.Keys() {
					entry.Set(k, rec.Value(k))
				}
			}
			st.catalog.Set("ark", entry.Set("source", "server").Set("shared", false))
			st.links["ark"] = [2]string{"ark", "server"}
		}
	}
	st.saved = st.repairAliases(st.saved)
	return st
}

// setupLinks maps graph devices to the GPU server machine their setup
// names: a compute resource with gpu_server G and device_id H links the
// device of setup host H to gpu:<G>.  The project layer may name any host;
// a member's layer only hosts of that member's own layer.
func (st *connState) setupLinks(sources *pyjson.Object) map[string]string {
	out := map[string]string{}
	layers := asObj(sources.Value("layers"))
	records := []any{layers.Value("project")}
	records = append(records, iterate(layers.Value("users"))...)
	for i, r := range records {
		record, ok := r.(*pyjson.Object)
		if !ok {
			continue
		}
		own := map[string]bool{}
		for _, h := range iterate(record.Value("hosts")) {
			if ho, ok := h.(*pyjson.Object); ok {
				own[pyjson.Str(ho.Value("id"))] = true
			}
		}
		for _, item := range iterate(asObj(record.Value("profile")).Value("resources")) {
			res, ok := item.(*pyjson.Object)
			if !ok || res.Value("kind") != "compute" {
				continue
			}
			d := asObj(res.Value("details"))
			gs, _ := d.Value("gpu_server").(string)
			host, _ := d.Value("device_id").(string)
			if gs == "" || host == "" || (i > 0 && !own[host]) {
				continue
			}
			device := HostDevice(sources, host, "")
			machine := "gpu:" + slug(gs)
			if _, visible := st.labels[device]; !visible || !st.catalog.Has(machine) {
				continue
			}
			if _, done := out[device]; !done {
				out[device] = machine
			}
		}
	}
	return out
}

// ManagedAlias is resource-open-model.js managedAlias: ark-<name> for an
// ~/.ssh/config Host ("" when nothing usable is left).
func ManagedAlias(name string) string {
	low := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	dash := false
	for _, r := range low {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-.")
	if len(slug) > 90 {
		slug = strings.Trim(slug[:90], "-.")
	}
	if slug == "" {
		return ""
	}
	if !strings.HasPrefix(slug, "ark-") {
		slug = "ark-" + slug
	}
	if !sshAliasRe.MatchString(slug) {
		return ""
	}
	return slug
}

// repairAliases drops saved aliases made from a device's hostname or id
// (ark-<label>, ark-<setup host id>: what older dialogs suggested, which no
// ~/.ssh/config has unless written by hand) when they are not the alias of
// the machine the entry belongs to.  Such an alias sent VS Code to
// ssh-remote+ark-deskhost7k2pq (2026-11-24).  The result is a copy; a save
// (SaveConnection) writes the repaired entries back.
func (st *connState) repairAliases(saved *pyjson.Object) *pyjson.Object {
	stale := map[string]bool{}
	for _, fp := range st.serverIDs {
		if a := ManagedAlias(fp); a != "" {
			stale[a] = true
		}
	}
	for id, kind := range st.kinds {
		if kind != "ark" && kind != "host" {
			continue
		}
		if a := ManagedAlias(st.labels[id]); a != "" {
			stale[a] = true
		}
		if strings.HasPrefix(id, "host:") {
			if a := ManagedAlias(strings.TrimPrefix(id, "host:")); a != "" {
				stale[a] = true
			}
		}
	}
	out := pyjson.NewObject()
	for _, key := range saved.Keys() {
		entry, ok := saved.Value(key).(*pyjson.Object)
		if !ok {
			continue
		}
		alias, _ := entry.Value("alias").(string)
		if alias == "" || !stale[alias] {
			out.Set(key, entry)
			continue
		}
		machine := key
		if l, ok := st.links[key]; ok && l[0] != "" {
			machine = l[0]
		}
		if e, ok := st.catalog.Value(machine).(*pyjson.Object); ok && ManagedAlias(pyjson.Str(e.Value("name"))) == alias {
			out.Set(key, entry)
			continue
		}
		fixed := entry.Copy()
		fixed.Delete("alias")
		if fixed.Len() > 0 {
			out.Set(key, fixed)
		}
	}
	return out
}

// match is a device's machine and how it was found ("" when none).
func (st *connState) match(id string, m Machines, server map[string]bool) (string, string) {
	if v, ok := m.Links.Value(id).(string); ok {
		if v != "" && v != id && st.catalog.Has(v) {
			return v, "saved"
		}
		if v == "" || v == id {
			if st.catalog.Has(id) {
				return id, "self"
			}
			return "", "unlinked"
		}
	}
	if st.catalog.Has(id) {
		return id, "self"
	}
	others := func(fn func(mid string, e *pyjson.Object) bool) string {
		for _, mid := range st.catalog.Keys() {
			if mid == id {
				continue
			}
			if fn(mid, st.catalog.Value(mid).(*pyjson.Object)) {
				return mid
			}
		}
		return ""
	}
	if label := strings.ToLower(st.labels[id]); label != "" {
		if mid := others(func(_ string, e *pyjson.Object) bool {
			return lowerStr(e.Value("name")) == label || (e.Has("host") && lowerStr(e.Value("host")) == label)
		}); mid != "" {
			return mid, "hostname"
		}
	}
	if mine, ok := st.saved.Value(id).(*pyjson.Object); ok && mine.Has("host") {
		host := lowerStr(mine.Value("host"))
		if mid := others(func(_ string, e *pyjson.Object) bool { return e.Has("host") && lowerStr(e.Value("host")) == host }); mid != "" {
			return mid, "address"
		}
	}
	if id == "ark" && len(server) > 0 {
		if mid := others(func(_ string, e *pyjson.Object) bool {
			return e.Has("host") && server[lowerStr(e.Value("host"))]
		}); mid != "" {
			return mid, "server"
		}
	}
	// A setup host named like the ARK server is that machine too.
	if l, ok := st.links["ark"]; ok && id != "ark" && l[0] != "" && st.hostname != "" && strings.ToLower(st.labels[id]) == st.hostname {
		return l[0], "server"
	}
	return "", ""
}

// effective is a device's connection: the machine's shared fields, then the
// viewer's entry for the machine, then the viewer's entry for the device.
func (st *connState) effective(id string) *pyjson.Object {
	machine := st.links[id][0]
	fields := map[string]any{}
	source := ""
	if e, ok := st.catalog.Value(machine).(*pyjson.Object); ok && machine != "" {
		for _, k := range []string{"host", "user", "port"} {
			if e.Has(k) {
				fields[k] = e.Value(k)
			}
		}
		if len(fields) > 0 {
			source = pyjson.Str(e.Value("source"))
		}
	}
	keys := []string{machine}
	if machine != id {
		keys = append(keys, id)
	}
	for _, key := range keys {
		mine, ok := st.saved.Value(key).(*pyjson.Object)
		if !ok || key == "" {
			continue
		}
		for _, k := range mine.Keys() {
			fields[k] = mine.Value(k)
		}
		if mine.Has("alias") || mine.Has("host") {
			source = "mine"
		}
	}
	if fields["alias"] == nil && fields["host"] == nil {
		return nil
	}
	out := pyjson.NewObject()
	for _, k := range sshEntryKeys {
		if v, ok := fields[k]; ok {
			out.Set(k, v)
		}
	}
	out.Set("source", source)
	if machine != "" && machine != id {
		out.Set("machine", machine)
	}
	return out
}

// ConnectionsWith is GET /api/resources/connect: Connections plus
//
//	machines  {id: {name, host?, user?, port?, source, shared}}: what a
//	          device can be linked to (shared values only)
//	links     {device id: {machine, how}}: how = saved, self, hostname,
//	          address, server or unlinked
//	mine      the viewer's own entries {machine or device id: entry}
//	suggest   {"ark": {host}}: the server's public host name, when the
//	          ARK server has no address yet
func ConnectionsWith(graph, sources, saved *pyjson.Object, opts ConnectOpts) *pyjson.Object {
	st := resolve(graph, sources, saved, opts)
	devices := pyjson.NewObject()
	for _, id := range st.order {
		if e := st.effective(id); e != nil {
			devices.Set(id, e)
		}
	}
	links := pyjson.NewObject()
	for _, id := range st.order {
		if l, ok := st.links[id]; ok {
			links.Set(id, pyjson.NewObject("machine", l[0], "how", l[1]))
		}
	}
	mine := pyjson.NewObject()
	for _, key := range st.saved.Keys() {
		if _, ok := st.labels[key]; ok || st.catalog.Has(key) {
			mine.Set(key, st.saved.Value(key))
		}
	}
	suggest := pyjson.NewObject()
	if _, ok := st.labels["ark"]; ok && !devices.Has("ark") {
		for _, name := range opts.ServerNames {
			if name != "" && net.ParseIP(name) == nil && !loopbackName(name) {
				suggest.Set("ark", pyjson.NewObject("host", name))
				break
			}
		}
	}
	out := pyjson.NewObject("devices", devices, "hosts", hostDevices(sources, st.labels))
	return out.Set("machines", st.catalog).Set("links", links).Set("mine", mine).Set("suggest", suggest)
}

func loopbackName(name string) bool {
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}

// hostDevices maps setup host ids to their visible graph device.
func hostDevices(sources *pyjson.Object, visible map[string]string) *pyjson.Object {
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
			if id := HostDevice(sources, hostID, pyjson.Str(or(loc.Value("host_label"), ""))); visible[id] != "" {
				hosts.Set(hostID, id)
			}
		}
	}
	return hosts
}

// SaveRequest is POST /api/resources/connect/<device id>: the viewer's own
// fields (alias, host, user, port, auth) plus, for admins, "shared" {name,
// host, port, user} and "link" (a machine id; "" for the device's own record).
type SaveRequest struct {
	ProjectPath string
	Username    string
	DeviceID    string
	Body        *pyjson.Object
	// Admin may change shared records and links.
	Admin bool
	// Graph, Sources, Opts: as for ConnectionsWith (the viewer's view).
	Graph, Sources *pyjson.Object
	Opts           ConnectOpts
}

// SaveConnection applies a SaveRequest; it returns {"device", "machine",
// "ssh": personal entry|null, "shared": record|null}.
func SaveConnection(ctx context.Context, o *org.Manager, req SaveRequest) (*pyjson.Object, error) {
	if !deviceIDRe.MatchString(req.DeviceID) {
		return nil, &Error{400, "invalid device id", "RESOURCES_INVALID"}
	}
	body := req.Body
	if body == nil {
		body = pyjson.NewObject()
	}
	personalRaw := body.Copy()
	sharedRaw, hasShared := body.Get("shared")
	linkRaw, hasLink := body.Get("link")
	personalRaw.Delete("shared")
	personalRaw.Delete("link")
	personal, err := CleanPersonal(personalRaw)
	if err != nil {
		return nil, err
	}
	var shared *pyjson.Object
	if hasShared && sharedRaw != nil {
		so, ok := sharedRaw.(*pyjson.Object)
		if !ok {
			return nil, sshBad("shared must be an object")
		}
		if shared, err = CleanShared(so); err != nil {
			return nil, err
		}
	}
	if (hasShared || hasLink) && !req.Admin {
		return nil, &Error{403, "only project admins change the shared connection of a machine", "RESOURCES_ADMIN_REQUIRED"}
	}
	machinesMu.Lock()
	defer machinesMu.Unlock()
	saved := pyjson.NewObject()
	if req.Username != "" && o != nil {
		if saved, err = SSHSettings(ctx, o, req.Username); err != nil {
			return nil, err
		}
	}
	m := ReadMachines(req.ProjectPath)
	opts := req.Opts
	opts.Machines = m
	st := resolve(req.Graph, req.Sources, saved, opts)
	if _, visible := st.labels[req.DeviceID]; !visible && !saved.Has(req.DeviceID) && !m.Records.Has(req.DeviceID) {
		return nil, &Error{404, "device not found", "RESOURCES_DEVICE_NOT_FOUND"}
	}
	changed := false
	if hasLink {
		link, ok := linkRaw.(string)
		if !ok || (link != "" && !deviceIDRe.MatchString(link)) {
			return nil, sshBad("link must be a machine id")
		}
		if link == req.DeviceID {
			link = ""
		}
		if link != "" && !st.catalog.Has(link) {
			return nil, &Error{400, "unknown machine", "RESOURCES_UNKNOWN_MACHINE"}
		}
		if !m.Links.Has(req.DeviceID) && m.Links.Len() >= MaxMachines {
			return nil, &Error{400, "too many linked devices", "RESOURCES_SSH_LIMIT"}
		}
		m.Links.Set(req.DeviceID, link)
		changed = true
		opts.Machines = m
		st = resolve(req.Graph, req.Sources, saved, opts)
	}
	target := st.links[req.DeviceID][0]
	if target == "" {
		target = req.DeviceID
	}
	if hasShared {
		if shared == nil {
			m.Records.Delete(target)
		} else {
			if !m.Records.Has(target) && m.Records.Len() >= MaxMachines {
				return nil, &Error{400, "too many shared machine records", "RESOURCES_SSH_LIMIT"}
			}
			m.Records.Set(target, shared)
		}
		changed = true
	}
	// The result must still have an address when the viewer keeps an entry.
	if personal != nil && !personal.Has("alias") && !personal.Has("host") {
		host := shared != nil && shared.Has("host")
		if !hasShared {
			if e, ok := st.catalog.Value(target).(*pyjson.Object); ok {
				host = e.Has("host")
			}
		}
		if !host {
			return nil, sshBad("give an SSH alias or a host")
		}
	}
	if personal != nil && (req.Username == "" || o == nil) {
		return nil, &Error{400, "SSH connections need a signed-in account", "RESOURCES_ACCOUNT_REQUIRED"}
	}
	if changed && req.ProjectPath != "" {
		if err := WriteMachines(req.ProjectPath, m); err != nil {
			return nil, err
		}
	}
	if req.Username != "" && o != nil {
		// The entry lives under the machine; an older one under the device goes.
		before := saved.Len()
		hadDevice, hadTarget := saved.Has(req.DeviceID), saved.Has(target)
		saved.Delete(req.DeviceID)
		if personal == nil {
			saved.Delete(target)
		} else {
			if !saved.Has(target) && saved.Len() >= MaxSSHEntries {
				return nil, &Error{400, "too many saved SSH connections", "RESOURCES_SSH_LIMIT"}
			}
			saved.Set(target, personal)
		}
		if personal != nil || hadDevice || hadTarget || before != saved.Len() {
			user, ok, err := o.UserByName(ctx, req.Username)
			if err != nil {
				return nil, err
			}
			if ok {
				if err := o.SaveSettings(ctx, user.Str("id"), SSHSettingsScope, pyjson.NewObject("devices", saved), nil); err != nil {
					return nil, err
				}
			}
		}
	}
	var p, s any
	if personal != nil {
		p = personal
	}
	if rec, ok := m.Records.Value(target).(*pyjson.Object); ok {
		s = rec
	}
	return pyjson.NewObject("device", req.DeviceID, "machine", target, "ssh", p, "shared", s), nil
}

// ------------------------------------------------------- server addresses

var (
	addrMu    sync.Mutex
	addrCache = map[string]addrEntry{}
)

type addrEntry struct {
	addrs []string
	at    time.Time
}

// LookupAddrs resolves host names (cached for 10 minutes, at most 1.5 s per
// name; failures resolve to nothing) and adds this machine's interface
// addresses.  Lower case, IP literals kept as they are.
func LookupAddrs(ctx context.Context, names []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s = strings.ToLower(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || loopbackName(name) {
			continue
		}
		if net.ParseIP(name) != nil {
			add(name)
			continue
		}
		addrMu.Lock()
		e, ok := addrCache[name]
		addrMu.Unlock()
		if !ok || time.Since(e.at) > 10*time.Minute {
			c, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			found, _ := net.DefaultResolver.LookupHost(c, name)
			cancel()
			e = addrEntry{found, time.Now()}
			addrMu.Lock()
			if len(addrCache) > 64 {
				addrCache = map[string]addrEntry{}
			}
			addrCache[name] = e
			addrMu.Unlock()
		}
		for _, a := range e.addrs {
			add(a)
		}
	}
	if ifaces, err := net.InterfaceAddrs(); err == nil {
		for _, a := range ifaces {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
				add(ipn.IP.String())
			}
		}
	}
	return out
}
