package setup

import (
	"strings"
)

func inStrings(s string, l []any) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// checkTaskBase is skills/setup.check_task(profile, state, task).
func checkTaskBase(profile *obj, state string, task *obj) []string {
	warnings := []string{}
	if state != "confirmed" && state != "verified" {
		return append(warnings, "environment setup is '"+state+"'; run the setup skill and get human "+
			"confirmation before starting task work")
	}
	repositories := listOr(or(profile.Value("repositories"), []any{}))
	skipped := listOr(or(profile.Value("skipped"), []any{}))
	repository := text(task.Value("repository"), 500)
	if repository != "" && !inStrings("code", skipped) {
		known := false
		readOnly := false
		for _, x := range repositories {
			item := mapOf(x)
			if item == nil {
				continue
			}
			id, p := str(item.Value("id")), str(item.Value("path"))
			names := []string{id, pathName(p), p}
			for _, n := range names {
				if n == repository {
					known = true
				}
			}
			if str(item.Value("access")) == "read-only" {
				for _, n := range names {
					if n == repository {
						readOnly = true
					}
				}
			}
		}
		if !known {
			warnings = append(warnings, "repository '"+repository+"' is not registered in the environment profile")
		} else if readOnly {
			warnings = append(warnings, "repository '"+repository+"' is registered read-only")
		}
	}
	gates := map[any]bool{}
	for _, x := range listOr(or(profile.Value("commands"), []any{})) {
		if item := mapOf(x); item != nil {
			gates[item.Value("gate")] = true
		}
	}
	if inStrings("commands", skipped) {
		return warnings
	}
	for _, g := range list(task.Value("gates")) {
		gateID := g
		if m := mapOf(g); m != nil {
			gateID = m.Value("id")
		}
		s, ok := gateID.(string)
		if ok && (s == "static" || s == "unit" || s == "component") && !gates[s] {
			warnings = append(warnings, "gate '"+s+"' has no recorded command in the environment profile")
		}
	}
	return warnings
}

// CheckTask is setup_layers.check_task(resolve(project, viewer), task):
// per-caller warnings for an agent-team task.
func (e *Env) CheckTask(v *Viewer, task *obj) ([]any, error) {
	mu.Lock()
	defer mu.Unlock()
	vw, err := e.resolve(v)
	if err != nil {
		return nil, err
	}
	flat, _, _ := vw.flatView(nil)
	state := str(vw.project.Value("state"))
	warnings := checkTaskBase(flat, state, task)
	if state != "confirmed" && state != "verified" {
		return toAny(warnings), nil
	}
	if vw.mine != nil {
		if s := str(vw.mine.Value("state")); s == "draft" || s == "proposed" || s == "stale" {
			warnings = append([]string{"your setup layer is '" + s + "'; confirm it in " +
				"Settings -> Setup before task work starts"}, warnings...)
		}
	}
	repository := text(task.Value("repository"), 500)
	notRegistered := false
	for _, w := range warnings {
		notRegistered = notRegistered || strings.Contains(w, "is not registered")
	}
	if repository != "" && notRegistered {
		for _, item := range vw.inaccessible {
			names := []string{str(item.Value("id")), str(item.Value("name")), str(item.Value("key"))}
			for _, x := range listOr(or(item.Value("locations"), []any{})) {
				if loc := mapOf(x); loc != nil {
					names = append(names, pathName(str(loc.Value("path_or_ref"))))
				}
			}
			match := false
			for _, n := range names {
				match = match || n == repository
			}
			if str(item.Value("kind")) != "repository" || !match {
				continue
			}
			kept := []string{}
			for _, w := range warnings {
				if !strings.Contains(w, "is not registered") {
					kept = append(kept, w)
				}
			}
			warnings = kept
			if str(item.Value("reason")) == "no_location_on_host" {
				warnings = append(warnings, "repository '"+repository+"' has no location on this host; "+
					"register one with POST /api/agent/v1/setup/location")
			}
			break
		}
	}
	return toAny(warnings), nil
}

func toAny(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}
