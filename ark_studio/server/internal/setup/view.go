package setup

import (
	"sort"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// view is resolve()'s result.
type view struct {
	project      *obj
	mine         *obj // nil for a viewer without a user
	host         *obj
	accessible   []*obj
	inaccessible []*obj
	policies     *obj
	full         []*obj
}

func needsLocation(resource *obj) bool {
	kind := str(resource.Value("kind"))
	if _, ok := locationField[kind]; ok {
		return true
	}
	return kind == "compute" && get(mapOf(resource.Value("details")), "kind") == "local"
}

func meta(resource *obj, key, layer, reason string) *obj {
	owner := or(resource.Value("owner"), "")
	return pyjson.NewObject("key", key, "id", resource.Value("id"), "kind", resource.Value("kind"),
		"name", resource.Value("name"), "purpose", or(resource.Value("purpose"), ""), "owner", owner,
		"layer", layer, "reason", reason, "ask", owner)
}

// resolve is resolve(project_path, viewer).
func (e *Env) resolve(v *Viewer) (*view, error) {
	project, err := e.loadProject()
	if err != nil {
		return nil, err
	}
	policies := mapOf(get(profileOf(project), "policies"))
	if policies.Len() == 0 {
		policies = pyjson.NewObject()
		for _, k := range PolicyKeys {
			policies.Set(k, PolicyDefaults[k])
		}
	}
	layers, err := e.allUserLayers()
	if err != nil {
		return nil, err
	}
	var mine *obj
	for _, rec := range layers {
		if v.isUser(rec.Value("owner")) {
			mine = rec
			break
		}
	}
	if mine == nil {
		mine = empty("user", v.User)
	} else if mine, err = e.loadUser(v.User, policies); err != nil {
		return nil, err
	}
	hostLabel := hostLabelFor(mine, v.HostID)

	type source struct {
		layer  string
		record *obj
	}
	sources := []source{{"project", project}}
	for _, rec := range layers {
		sources = append(sources, source{"user", rec})
	}
	out := &view{project: project, host: pyjson.NewObject("id", v.HostID, "label", hostLabel)}
	for _, src := range sources {
		for _, x := range listOr(get(profileOf(src.record), "resources")) {
			resource := x.(*obj)
			key := resourceKey(src.layer, str(src.record.Value("owner")), str(resource.Value("id")))
			locations := []*obj{}
			for _, pool := range sources {
				for _, l := range listOr(pool.record.Value("locations")) {
					loc := l.(*obj)
					if loc.Value("resource") == key {
						locations = append(locations, loc.Copy())
					}
				}
			}
			sort.SliceStable(locations, func(i, j int) bool {
				ai, aj := str(locations[i].Value("added_at")), str(locations[j].Value("added_at"))
				if ai != aj {
					return ai < aj
				}
				return !v.isUser(locations[i].Value("added_by")) && v.isUser(locations[j].Value("added_by"))
			})
			var here *obj
			for i := len(locations) - 1; i >= 0; i-- {
				if locations[i].Value("host_id") == v.HostID {
					here = locations[i]
					break
				}
			}
			if here == nil {
				for _, loc := range locations {
					p := str(loc.Value("path_or_ref"))
					if !IsLocalPath(p) && p != "local" {
						here = loc
						break
					}
				}
			}
			usable := here != nil || !needsLocation(resource)
			locs := make([]any, len(locations))
			for i, l := range locations {
				locs[i] = l
			}
			var location any
			if here != nil {
				location = here
			}
			updated, ok := src.record.Get("updated_at")
			if !ok {
				updated = ""
			}
			item := meta(resource, key, src.layer, "registered").Set("details", resource.Value("details")).
				Set("locations", locs).Set("location", location).Set("usable_here", usable).
				Set("access_verified", here != nil && pyjson.Truthy(here.Value("verified_at"))).
				Set("updated_at", updated).Set("layer_state", src.record.Value("state"))
			out.full = append(out.full, item)
			if usable {
				out.accessible = append(out.accessible, item)
			} else {
				out.inaccessible = append(out.inaccessible, item.Copy().Set("reason", "no_location_on_host"))
			}
		}
	}
	userPolicies := pyjson.NewObject()
	if v.User != "" {
		userPolicies = orMap(get(profileOf(mine), "policies"))
	}
	effective := pyjson.NewObject()
	for _, k := range PolicyKeys {
		a, ok := policies.Get(k)
		if !ok {
			a = "ask"
		}
		b, ok := userPolicies.Get(k)
		if !ok {
			b = "allow"
		}
		if rankOf(b) > rankOf(a) {
			a = b
		}
		effective.Set(k, a)
	}
	if v.User != "" {
		out.mine = mine
	}
	out.policies = pyjson.NewObject("effective", effective, "project", policies, "user", userPolicies)
	return out, nil
}

func rankOf(v any) int {
	if s, ok := v.(string); ok {
		if r, ok := policyRank[s]; ok {
			return r
		}
	}
	return 1
}

// flatView is flat_view(view, items): a 1.0 profile plus flat id -> item.
func (vw *view) flatView(items []*obj) (*obj, map[string]*obj, []string) {
	if items == nil {
		items = vw.accessible
	}
	project := profileOf(vw.project)
	raw := pyjson.NewObject()
	for _, k := range projectFields {
		raw.Set(k, project.Value(k))
	}
	raw.Set("policies", vw.policies.Value("effective").(*obj).Copy())
	counts := map[string]int{}
	for _, item := range items {
		counts[str(item.Value("id"))]++
	}
	mapping := map[string]*obj{}
	order := []string{}
	for _, item := range items {
		flatID := str(item.Value("id"))
		if counts[flatID] > 1 {
			owner := str(or(item.Value("owner"), "project"))
			flatID = prefix(nonID.ReplaceAllString(lower(owner+"-"+str(item.Value("id"))), "-"), 48)
		}
		entry := mapOf(item.Value("details")).Copy().Set("id", flatID)
		kind := str(item.Value("kind"))
		if f := locationField[kind]; f != "" {
			var p any = ""
			if loc := mapOf(item.Value("location")); loc != nil {
				if v, ok := loc.Get("path_or_ref"); ok {
					p = v
				}
			}
			entry.Set(f, p)
		}
		field := KindFields[kind]
		lst := listOr(raw.Value(field))
		raw.Set(field, append(append([]any{}, lst...), entry))
		if _, seen := mapping[flatID]; !seen {
			order = append(order, flatID)
		}
		mapping[flatID] = item
	}
	profile, _, _ := NormalizeProfile(raw)
	profile.Set("policies_defaulted", or(project.Value("policies_defaulted"), []any{}))
	return profile, mapping, order
}

func (vw *view) readiness(flat *obj) *obj {
	projectState := str(vw.project.Value("state"))
	userState := "unset"
	if vw.mine != nil {
		if s := vw.mine.Value("state"); pyjson.Truthy(s) {
			userState = str(s)
		}
	}
	ready := Readiness(flat, projectState)
	blocked := []*obj{}
	for _, x := range ready.Value("blocked").([]any) {
		blocked = append(blocked, x.(*obj))
	}
	if vw.mine != nil && (userState == "draft" || userState == "proposed" || userState == "stale") {
		blocked = append([]*obj{pyjson.NewObject("capability", "tasks", "reason", "your setup layer is '"+userState+"'; "+
			"confirm it in Settings -> Setup before task work starts")}, blocked...)
		ready.Set("ready", false)
	}
	missing := []*obj{}
	for _, item := range vw.inaccessible {
		if item.Value("reason") == "no_location_on_host" {
			missing = append(missing, item)
		}
	}
	for _, entry := range blocked {
		if entry.Value("capability") == "implement" && entry.Value("reason") == "no primary repository registered" {
			for _, m := range missing {
				if m.Value("kind") == "repository" {
					entry.Set("reason", "repository '"+str(m.Value("id"))+"' has no location on this host")
					break
				}
			}
		}
	}
	blocked = dedupeBlocked(blocked)
	ids := map[string]bool{}
	ba := make([]any, len(blocked))
	for i, b := range blocked {
		ids[b.Value("capability").(string)] = true
		ba[i] = b
	}
	ready.Set("blocked", ba)
	allowed := []any{}
	for _, c := range capabilities {
		if !ids[c] {
			allowed = append(allowed, c)
		}
	}
	ready.Set("allowed", allowed)
	var mineOwner any
	if vw.mine != nil {
		mineOwner = vw.mine.Value("owner")
	}
	needs := []any{}
	for _, item := range missing {
		if item.Value("layer") == "project" || (mineOwner != nil && item.Value("owner") == mineOwner) {
			needs = append(needs, item.Value("key"))
		}
	}
	inacc := 0
	for _, item := range vw.inaccessible {
		if item.Value("reason") != "no_location_on_host" {
			inacc++
		}
	}
	ready.Set("project_state", projectState).Set("user_state", userState).Set("host", vw.host.Copy()).
		Set("needs_location", needs).Set("inaccessible", int64(inacc)).Set("accessible", int64(len(vw.accessible)))
	return ready
}

func ownLayerPublic(record *obj) any {
	if record == nil {
		return nil
	}
	out := pyjson.NewObject()
	for _, k := range []string{"layer", "owner", "state", "digest", "confirmation", "verification", "updated_at",
		"updated_by", "needs_reconfirmation", "stale_reason", "valid"} {
		out.Set(k, record.Value(k))
	}
	return out.Set("profile", record.Value("profile")).Set("locations", or(record.Value("locations"), []any{})).
		Set("hosts", or(record.Value("hosts"), []any{}))
}

func projectLayerPublic(record *obj) *obj {
	out := pyjson.NewObject()
	for _, k := range []string{"layer", "state", "digest", "confirmation", "verification", "updated_at", "updated_by",
		"needs_reconfirmation", "stale_reason", "valid", "migration"} {
		out.Set(k, record.Value(k))
	}
	return out.Set("profile", profileOf(record).Copy()).Set("locations", or(record.Value("locations"), []any{}))
}

func renderResources(items []*obj) string {
	rows := [][]any{}
	for _, item := range items {
		locs := listOr(item.Value("locations"))
		var where any = "—"
		if len(locs) > 0 {
			parts := []string{}
			for _, l := range locs {
				loc := l.(*obj)
				parts = append(parts, str(loc.Value("host_label"))+": "+str(loc.Value("path_or_ref")))
			}
			where = strings.Join(parts, ", ")
		}
		rows = append(rows, []any{or(item.Value("key"), item.Value("id")), item.Value("kind"), item.Value("name"),
			item.Value("owner"), where})
	}
	return table(rows, []string{"key", "kind", "name", "owner", "locations"})
}

func (vw *view) projectSummary() string {
	shown := []*obj{}
	for _, item := range vw.full {
		if item.Value("layer") == "project" {
			shown = append(shown, item)
		}
	}
	flat, _, _ := vw.flatView(shown)
	flat.Set("policies", vw.policies.Value("project").(*obj).Copy())
	record := vw.project.Copy().Set("profile", flat)
	return pytextRStrip(RenderSummary(record)) + "\n\n## All registered resources and locations\n\n" +
		renderResources(vw.full) + "\n"
}

func (vw *view) userSummary() string {
	mine := vw.mine
	if mine == nil {
		mine = pyjson.NewObject()
	}
	owner := orStr(mine.Value("owner"))
	keyPrefix := resourceKey("user", owner, "")
	items := []*obj{}
	for _, item := range vw.full {
		if strings.HasPrefix(str(item.Value("key")), keyPrefix) {
			items = append(items, item)
		}
	}
	parts := []string{"# My setup layer: " + owner, "",
		"- State: **" + str(mine.Value("state")) + "**  ·  digest `" + orStr(mine.Value("digest")) + "`",
		"- Updated: " + dash(mine.Value("updated_at")) + " by " + dash(mine.Value("updated_by"))}
	if conf := mapOf(mine.Value("confirmation")); conf.Len() > 0 {
		parts = append(parts, "- Confirmed: "+str(conf.Value("at"))+" by "+str(conf.Value("by")))
	}
	parts = append(parts, "", "## My resources", "", renderResources(items), "")
	policies := orMap(get(orMap(mine.Value("profile")), "policies"))
	rows := [][]any{}
	project := vw.policies.Value("project").(*obj)
	for _, k := range policies.Keys() {
		p, ok := project.Get(k)
		if !ok {
			p = ""
		}
		rows = append(rows, []any{k, policies.Value(k), p})
	}
	parts = append(parts, "## My policy tightenings", "", table(rows, []string{"action", "mine", "project"}), "")
	parts = append(parts, "## My hosts", "", table(rowsOf(mine.Value("hosts"), "id", "label"), []string{"id", "label"}), "")
	return pytextRStrip(strings.Join(parts, "\n")) + "\n"
}

func objs(l []*obj) []any {
	out := make([]any, len(l))
	for i, o := range l {
		out[i] = o
	}
	return out
}

// Payload is payload(project_path, viewer, default_owner=..., known_user=...).
func (e *Env) Payload(v *Viewer) (*obj, error) {
	mu.Lock()
	defer mu.Unlock()
	return e.PayloadLocked(v)
}

// PayloadLocked is Payload for a caller that holds Lock().
func (e *Env) PayloadLocked(v *Viewer) (*obj, error) {
	vw, err := e.resolve(v)
	if err != nil {
		return nil, err
	}
	flat, _, _ := vw.flatView(nil)
	ready := vw.readiness(flat)
	project := vw.project
	public := projectLayerPublic(project)
	pl := pyjson.NewObject()
	for _, k := range []string{"state", "digest", "confirmation", "updated_at", "updated_by", "needs_reconfirmation", "valid"} {
		pl.Set(k, public.Value(k))
	}
	pl.Set("can_edit", v.CanWrite).Set("can_confirm", v.Admin && !v.Legacy)
	var user any
	if vw.mine != nil {
		u := pyjson.NewObject()
		for _, k := range []string{"owner", "state", "digest", "confirmation", "updated_at", "updated_by", "needs_reconfirmation", "valid"} {
			u.Set(k, vw.mine.Value(k))
		}
		can := v.CanWrite && v.User != "" && !v.Legacy
		user = u.Set("can_edit", can).Set("can_confirm", can)
	}
	userSummary := ""
	if vw.mine != nil {
		userSummary = vw.userSummary()
	}
	return pyjson.NewObject(
		"format", LayerFormat, "setup_version", LayerVersion,
		"state", project.Value("state"), "digest", project.Value("digest"),
		"confirmation", project.Value("confirmation"), "verification", project.Value("verification"),
		"profile", flat, "readiness", ready, "questions", Questions(flat),
		"summary_markdown", vw.projectSummary(),
		"viewer", v.public(str(vw.host.Value("label"))), "host", vw.host,
		"resources", objs(vw.full), "accessible", objs(vw.accessible),
		"known_but_inaccessible", objs(vw.inaccessible), "policies", vw.policies,
		"layers", pyjson.NewObject("project", pl, "user", user),
		"project_layer", public, "mine", ownLayerPublic(vw.mine), "user_summary_markdown", userSummary), nil
}

// VerifyView is verify_view(project_path, viewer, actor=..., gpu_servers=...).
func (e *Env) VerifyView(v *Viewer, actor string, gpuServers *obj) (*obj, error) {
	vw, err := (&Env{Project: e.Project, DefaultOwner: "local"}).resolve(v)
	if err != nil {
		return nil, err
	}
	flat, mapping, order := vw.flatView(nil)
	fp := MachineFingerprint()
	var result *obj
	if v.HostID != fp {
		result = pyjson.NewObject("ok", false, "checks", []any{pyjson.NewObject("id", "host", "status", "skip", "mandatory", false,
			"detail", "locations on host '"+str(vw.host.Value("label"))+"' cannot be checked from the ARK server; verify them on that host")},
			"machine", v.HostID, "verified_at", utcNow())
	} else {
		result = Verify(flat, gpuServers)
	}
	passed := map[string]bool{}
	for _, x := range listOr(result.Value("checks")) {
		c := x.(*obj)
		id := str(c.Value("id"))
		if c.Value("status") == "pass" && strings.Count(id, ":") == 1 {
			passed[strings.SplitN(id, ":", 2)[1]] = true
		}
	}
	stamped := map[string]bool{}
	for _, flatID := range order {
		loc := mapOf(mapping[flatID].Value("location"))
		if passed[flatID] && loc != nil && loc.Value("host_id") == v.HostID {
			stamped[locKey2(loc)] = true
		}
	}
	now := orStr(result.Value("verified_at"))
	if now == "" {
		now = utcNow()
	}
	type target struct{ layer, owner string }
	targets := []target{}
	if v.Admin && vw.project.Value("state") != "unset" {
		targets = append(targets, target{"project", ""})
	}
	if v.User != "" && vw.mine != nil && vw.mine.Value("state") != "unset" {
		targets = append(targets, target{"user", v.User})
	}
	ok := pyjson.Truthy(result.Value("ok"))
	for _, t := range targets {
		record, err := e.loadLayer(t.layer, t.owner)
		if err != nil {
			return nil, err
		}
		for _, x := range listOr(record.Value("locations")) {
			loc := x.(*obj)
			if stamped[locKey2(loc)] && loc.Value("host_id") == v.HostID {
				loc.Set("verified_at", now).Set("verified_by", actor)
			}
		}
		if inAny(record.Value("state"), ReadyStates) && v.HostID == fp {
			state := "confirmed"
			if ok {
				state = "verified"
			}
			record.Set("state", state).Set("verification", pyjson.NewObject("ok", ok, "at", now, "by", actor, "host", v.HostID))
		}
		if err := e.write(e.layerTarget(t.layer, t.owner), record, pyjson.NewObject("at", now, "actor", actor,
			"layer", layerName(t.layer, t.owner), "event", "verify", "ok", ok, "host", v.HostID)); err != nil {
			return nil, err
		}
	}
	if !v.Admin && ok && v.HostID == fp {
		record, err := e.loadProject()
		if err != nil {
			return nil, err
		}
		if record.Value("state") == "confirmed" {
			record.Set("state", "verified").Set("verification", pyjson.NewObject("ok", true, "at", now, "by", actor, "host", v.HostID))
			if err := e.write(e.projectFile(), record, pyjson.NewObject("at", now, "actor", actor, "layer", "project",
				"event", "verify", "ok", true, "host", v.HostID)); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func locKey2(loc *obj) string {
	return pyjson.Repr([]any{loc.Value("resource"), loc.Value("path_or_ref")})
}

func pytextRStrip(s string) string { return pytext.RStrip(s) }
