package main

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"arkstudio/go/internal/compat/pyargparse"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/setup"
	"arkstudio/go/internal/skillbrief"
	"arkstudio/go/internal/skillkit"
)

// jsonArg is __main__._json_arg: a JSON object from a file path or inline text.
func jsonArg(value string) (*pyjson.Object, error) {
	text := pytext.Strip(value)
	if text == "" {
		return pyjson.NewObject(), nil
	}
	if st, err := os.Stat(text); err == nil && st.Mode().IsRegular() {
		raw, err := os.ReadFile(text)
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(raw) {
			return nil, &cliValueError{"'utf-8' codec can't decode the payload file"}
		}
		text = strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\r", "\n")
		text = strings.TrimPrefix(text, "\ufeff")
	}
	v, err := pyjson.Loads(strings.TrimLeft(text, "\ufeff"))
	if err != nil {
		return nil, &cliValueError{pyjson.DecodeErrorMessage(strings.TrimLeft(text, "\ufeff"))}
	}
	obj, ok := v.(*pyjson.Object)
	if !ok {
		return nil, &cliValueError{"JSON payload must be an object"}
	}
	return obj, nil
}

// getUser is getpass.getuser().
func getUser() string {
	for _, name := range []string{"LOGNAME", "USER", "LNAME", "USERNAME"} {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	if u, err := user.Current(); err == nil {
		name := u.Username
		if i := strings.LastIndexAny(name, `\`); i >= 0 {
			name = name[i+1:]
		}
		return name
	}
	return ""
}

func printJSON(v any) { printBytes(pyjson.DumpsIndent(v, 2)) }

// setupCommand is __main__._run_setup_cli.
func setupCommand(ns pyargparse.Namespace) (int, error) {
	action := ns.Str("setup_action")
	if action == "" {
		action = "state"
	}
	if action == "questions" && ns.Str("project") == "" {
		payload, err := jsonArg(ns.Str("payload"))
		if err != nil {
			return 0, err
		}
		profile, _, _ := setup.NormalizeProfile(payload)
		printJSON(pyjson.NewObject("questions", setup.Questions(profile)))
		return 0, nil
	}
	if ns.Str("project") == "" {
		eprintln("--project <path.ark> is required")
		return 2, nil
	}
	folder := project.Resolve(ns.Str("project"))
	if st, err := os.Stat(filepath.Join(folder, "project.json")); err != nil || !st.Mode().IsRegular() {
		eprintf("not an ARK project: %s\n", folder)
		return 2, nil
	}
	var (
		result *pyjson.Object
		err    error
	)
	switch action {
	case "state":
		result, err = setup.StatePayload(folder)
	case "summary":
		if result, err = setup.StatePayload(folder); err == nil {
			pyprint(pyjson.Str(result.Value("summary_markdown")))
			return 0, nil
		}
	case "questions":
		var state *pyjson.Object
		if state, err = setup.StatePayload(folder); err == nil {
			result = pyjson.NewObject("questions", state.Value("questions"))
		}
	case "discover":
		paths := []any{}
		if l, ok := ns["paths"].([]string); ok {
			for _, p := range l {
				paths = append(paths, p)
			}
		}
		result = setup.Discover(paths, nil, nil, folder)
	case "draft":
		var payload *pyjson.Object
		if payload, err = jsonArg(ns.Str("payload")); err != nil {
			return 0, err
		}
		result, err = setup.SaveDraft(folder, payload, "agent:cli", ns.Bool("propose"))
	case "confirm":
		result, err = setup.Confirm(folder, "human:"+getUser(), ns.Str("digest"), ns.Str("note"))
	case "verify":
		var record *pyjson.Object
		if record, err = setup.Load(folder); err == nil {
			profile, _ := record.Value("profile").(*pyjson.Object)
			result, err = setup.RecordVerification(folder, setup.Verify(profile, nil), "agent:cli")
		}
	default:
		eprintf("unknown setup action: %s\n", action)
		return 2, nil
	}
	if err != nil {
		var se *setup.Error
		if errors.As(err, &se) {
			eprintf("setup error (%s): %s\n", se.Code, se.Message)
			return 1, nil
		}
		return 0, err
	}
	result.Delete("summary_markdown")
	printJSON(result)
	if v, ok := result.Get("ok"); ok && !pyjson.Truthy(v) {
		return 1, nil
	}
	return 0, nil
}

// briefCommand is `skills brief`: interface.validate_brief(_json_arg(payload)).
func briefCommand(ns pyargparse.Namespace) (int, error) {
	payload, err := jsonArg(ns.Str("payload"))
	if err != nil {
		return 0, err
	}
	result, err := skillbrief.ValidateBrief(payload)
	if err != nil {
		return 0, err
	}
	printJSON(result)
	if !pyjson.Truthy(result.Value("ok")) {
		return 1, nil
	}
	return 0, nil
}

// renderCommand is `skills render` (catalog.render_template).
func renderCommand(ns pyargparse.Namespace) (int, error) {
	context, err := jsonArg(ns.Str("context")) // evaluated before render_template runs
	if err != nil {
		return 0, err
	}
	data, err := skillkit.RenderTemplate(ns.Str("skill"), ns.Str("template"), context, nil)
	if err != nil {
		return 0, err
	}
	text := data.Value("text").(string)
	if out := ns.Str("out"); out != "" {
		body := text
		if runtime.GOOS == "windows" {
			body = strings.ReplaceAll(body, "\n", "\r\n")
		}
		if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
			return 0, err
		}
		pyprint(out)
		return 0, nil
	}
	pyprint(text)
	return 0, nil
}

// validateCommand is `skills validate` (catalog.validate_payload).
func validateCommand(ns pyargparse.Namespace) (int, error) {
	payload, err := jsonArg(ns.Str("payload"))
	if err != nil {
		return 0, err
	}
	root := ns.Str("project_root")
	result, err := skillkit.ValidatePayload(ns.Str("skill"), ns.Str("kind"), payload, root, root != "", nil)
	if err != nil {
		return 0, err
	}
	printJSON(result)
	if !pyjson.Truthy(result.Value("ok")) {
		return 1, nil
	}
	return 0, nil
}
