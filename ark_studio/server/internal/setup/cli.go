package setup

import "arkstudio/go/internal/compat/pyjson"

// The 1.0 single-profile API of skills/setup.py used by
// `python -m arkstudio skills setup ...` (load, state_payload, save_draft,
// confirm, record_verification, example_profile).

// CLIViewer is _cli_viewer(): the terminal caller, project-wide and trusted as admin.
func CLIViewer() *Viewer {
	return NewViewer(Viewer{Admin: true, CanWrite: true, TokenKind: "cli"})
}

func legacyEnv(project string) *Env { return &Env{Project: project, DefaultOwner: "local"} }

// Load is setup.load: the project record's 1.0 keys plus the flat profile.
func Load(project string) (*obj, error) {
	mu.Lock()
	defer mu.Unlock()
	vw, err := legacyEnv(project).resolve(LegacyViewer(""))
	if err != nil {
		return nil, err
	}
	flat, _, _ := vw.flatView(nil)
	out := pyjson.NewObject()
	for _, k := range []string{"format", "setup_version", "state", "digest", "confirmation",
		"verification", "updated_at", "updated_by", "stale_reason"} {
		if v, ok := vw.project.Get(k); ok {
			out.Set(k, v)
		}
	}
	out.Set("profile", flat)
	return out, nil
}

// StatePayload is setup.state_payload (legacy server viewer).
func StatePayload(project string) (*obj, error) {
	return legacyEnv(project).Payload(LegacyViewer(""))
}

// SaveDraft is setup.save_draft.
func SaveDraft(project string, profile *obj, actor string, propose bool) (*obj, error) {
	if profile == nil {
		profile = pyjson.NewObject()
	}
	result, err := legacyEnv(project).SaveLayer(profile, LegacyViewer(""), "project", actor, propose, "")
	if err != nil {
		return nil, err
	}
	payload, err := StatePayload(project)
	if err != nil {
		return nil, err
	}
	payload.Set("ok", result.Value("ok"))
	payload.Set("errors", result.Value("errors"))
	payload.Set("warnings", result.Value("warnings"))
	return payload, nil
}

// Confirm is setup.confirm (human actors only).
func Confirm(project, actor, digest, note string) (*obj, error) {
	if !IsHumanActor(actor) {
		return nil, setupErr("only a human can confirm the environment profile; agents may "+
			"draft and propose", 403, "SETUP_HUMAN_REQUIRED")
	}
	if _, err := legacyEnv(project).ConfirmLayer(CLIViewer(), "project", actor, digest, note); err != nil {
		return nil, err
	}
	return StatePayload(project)
}

// RecordVerification is setup.record_verification.
func RecordVerification(project string, result *obj, actor string) (*obj, error) {
	if err := recordProjectVerification(project, result, actor); err != nil {
		return nil, err
	}
	payload, err := StatePayload(project)
	if err != nil {
		return nil, err
	}
	payload.Set("verify", result.Copy())
	return payload, nil
}

// recordProjectVerification is setup_layers.record_project_verification.
func recordProjectVerification(project string, result *obj, actor string) error {
	mu.Lock()
	defer mu.Unlock()
	e := legacyEnv(project)
	record, err := e.loadProject()
	if err != nil {
		return err
	}
	if !inAny(record.Value("state"), ReadyStates) {
		return nil
	}
	now := utcNow()
	ok := pyjson.Truthy(result.Value("ok"))
	if ok {
		record.Set("state", "verified")
	} else {
		record.Set("state", "confirmed")
	}
	host := MachineFingerprint()
	if m := result.Value("machine"); pyjson.Truthy(m) {
		host = str(m)
	}
	record.Set("verification", pyjson.NewObject("ok", ok, "at", now, "by", actor, "host", host))
	return e.write(e.projectFile(), record, pyjson.NewObject("at", now, "actor", actor, "layer", "project",
		"event", "verify", "ok", ok, "digest", record.Value("digest")))
}
