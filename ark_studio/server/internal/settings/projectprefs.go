package settings

import (
	"unicode"

	"arkstudio/go/internal/compat/pyjson"
)

// Go-only (T-0034): the project picker's personal pins and hidden projects,
// kept on the profile as prefs.projects = {"pinned": [id…], "hidden": [id…]}
// (tests/python/contract/go_only.py FIXES "profile-project-prefs"; Python
// refuses the unknown preference).  Pinned ids keep their order (the
// picker's pin order); an id is never both pinned and hidden (hidden wins).
// "only_mine": true keeps the picker's "Created by me" filter on; it is
// stored only when true.

const (
	maxProjectPrefIDs = 200
	maxProjectIDRunes = 200
)

// ProjectPrefsError is an invalid prefs.projects value.
type ProjectPrefsError struct{}

func (ProjectPrefsError) Error() string { return "invalid project list preferences" }

func validProjectID(s string) bool {
	n := 0
	for _, r := range s {
		n++
		if unicode.IsControl(r) || n > maxProjectIDRunes {
			return false
		}
	}
	return n > 0
}

// projectIDList is a deduplicated id list; strict refuses anything invalid,
// lenient (reading stored prefs) drops it.
func projectIDList(value any, strict bool) ([]any, bool) {
	if value == nil {
		return []any{}, true
	}
	items, ok := value.([]any)
	if !ok || (strict && len(items) > maxProjectPrefIDs) {
		return nil, false
	}
	out := []any{}
	seen := map[string]bool{}
	for _, item := range items {
		s, ok := item.(string)
		if !ok || !validProjectID(s) {
			if strict {
				return nil, false
			}
			continue
		}
		if seen[s] || len(out) >= maxProjectPrefIDs {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, true
}

// ProjectPrefs validates (strict, PATCH /api/me) or cleans (lenient, stored
// prefs) a prefs.projects value.  The result is {"pinned", "hidden"} (and
// "only_mine": true when set); it is empty (Len 0) when both lists are empty
// and the filter is off.
func ProjectPrefs(value any, strict bool) (*pyjson.Object, error) {
	obj, ok := value.(*pyjson.Object)
	if !ok || (strict && !onlyKeys(obj, "pinned", "hidden", "only_mine")) {
		if strict {
			return nil, ProjectPrefsError{}
		}
		return pyjson.NewObject(), nil
	}
	pinned, okPinned := projectIDList(obj.Value("pinned"), strict)
	hidden, okHidden := projectIDList(obj.Value("hidden"), strict)
	if !okPinned || !okHidden {
		if strict {
			return nil, ProjectPrefsError{}
		}
		return pyjson.NewObject(), nil
	}
	isHidden := map[string]bool{}
	for _, id := range hidden {
		isHidden[id.(string)] = true
	}
	kept := []any{}
	for _, id := range pinned {
		if !isHidden[id.(string)] {
			kept = append(kept, id)
		}
	}
	onlyMine := false
	switch v := obj.Value("only_mine").(type) {
	case nil:
	case bool:
		onlyMine = v
	default:
		if strict {
			return nil, ProjectPrefsError{}
		}
	}
	if len(kept) == 0 && len(hidden) == 0 && !onlyMine {
		return pyjson.NewObject(), nil
	}
	out := pyjson.NewObject("pinned", kept, "hidden", hidden)
	if onlyMine {
		out.Set("only_mine", true)
	}
	return out, nil
}
