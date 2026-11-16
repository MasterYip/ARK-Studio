package setup

import "arkstudio/go/internal/compat/pyjson"

// exampleProfileJSON is the literal of setup.example_profile (key order kept).
const exampleProfileJSON = `{"objective": "Evaluate whether method X improves Y on benchmark Z within 2 weeks.", "non_goals": ["no changes to the evaluation harness"], "repositories": [{"id": "code", "path": "~/work/method-x", "role": "primary", "default_branch": "main", "instructions": ["AGENTS.md"]}, {"id": "paper", "path": "~/work/method-x-paper", "role": "paper", "default_branch": "main"}], "task_spaces": [{"id": "legacy-team", "kind": "agent-team-folder", "location": "~/work/method-x/agent_team", "relation": "import-history"}], "compute": [{"id": "local", "kind": "local", "use": "any"}, {"id": "gpu-a", "kind": "ssh", "gpu_server": "gpu-a", "gpus": "8x H200", "use": "ask", "scratch": "/data/scratch/method-x"}], "environments": [{"id": "py", "kind": "conda", "repository": "code", "activate": "conda activate method-x", "python": "3.11"}], "storage": [{"id": "artifacts", "kind": "artifacts", "location": "~/artifacts/method-x", "retention": "keep checkpoints of accepted runs"}], "commands": [{"id": "lint", "run": "ruff check .", "repository": "code", "environment": "py", "gate": "static"}, {"id": "unit", "run": "python -m pytest -q tests", "repository": "code", "environment": "py", "gate": "unit"}, {"id": "smoke", "run": "python train.py --steps 20", "repository": "code", "environment": "py", "gate": "component"}], "secrets": [{"id": "wandb", "ref": "env:WANDB_API_KEY", "purpose": "experiment logging"}], "agents": {"coordinator": "dsh", "providers": ["dsh", "codex"], "concurrency": 4, "default_reviewer": "user"}, "policies": {"commit": "allow", "push": "ask", "gpu_dispatch": "ask"}, "cadence": {"status_minutes": 10, "task_trigger": "the user requests a task or substantial multi-file work"}, "task_ids": {"prefixes": ["EXP", "DOC", "FIG"]}}`

// ExampleProfile is setup.example_profile().
func ExampleProfile() *obj {
	raw, _ := pyjson.LoadsObject(exampleProfileJSON)
	profile, _, _ := NormalizeProfile(raw)
	return profile
}

// ValidateProfile is setup.validate_profile.
func ValidateProfile(profile *obj) *obj {
	normalized, errs, warns := NormalizeProfile(profile)
	errors, warnings := []any{}, []any{}
	for _, e := range errs {
		errors = append(errors, e)
	}
	for _, w := range warns {
		warnings = append(warnings, w)
	}
	return pyjson.NewObject("ok", len(errs) == 0, "errors", errors, "warnings", warnings,
		"profile", normalized, "digest", Digest(normalized), "questions", Questions(normalized),
		"readiness", Readiness(normalized, "draft"))
}
