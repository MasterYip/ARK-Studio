package providers

import (
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyre"
	"arkstudio/go/internal/compat/pytext"
)

// The two line patterns of providers.py, on pyre for Python's Unicode \s
// and its "$ also matches before a final newline".
var (
	tomlTable  = pyre.MustCompile(`^\[\s*([^\[\]]+?)\s*\]\s*(#.*)?$`, 0)
	tomlString = pyre.MustCompile(`^([A-Za-z0-9_-]+)\s*=\s*(?:\"([^\"\\]*)\"|'([^']*)')\s*(#.*)?$`, 0)
)

// reMatch is pattern.match(s): the group texts (nil for a group that did
// not participate), or nil when there is no match.
func reMatch(re *pyre.Regexp, s string, groups int) []*string {
	rs := []rune(s)
	budget := 1 << 40
	m, ok, err := re.SearchFrom(rs, 0, -1, &budget)
	if err != nil || !ok || m.Start != 0 {
		return nil
	}
	out := make([]*string, groups+1)
	for g := 0; g <= groups; g++ {
		if a, b, ok := m.Group(g); ok {
			text := string(rs[a:b])
			out[g] = &text
		}
	}
	return out
}

// tomlModelKeys is _toml_model_keys: the top-level and per-profile "model"
// and "model_reasoning_effort" strings of a Codex config.toml.
func tomlModelKeys(text string) (*pyjson.Object, *pyjson.Object) {
	top := pyjson.NewObject()
	profiles := pyjson.NewObject()
	table := ""
	for _, raw := range pytext.SplitLines(text, false) {
		line := pytext.Strip(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if header := reMatch(tomlTable, line, 2); header != nil {
			table = pytext.Strip(*header[1])
			continue
		}
		m := reMatch(tomlString, line, 4)
		if m == nil {
			continue
		}
		key := *m[1]
		value := ""
		if m[2] != nil {
			value = *m[2]
		} else if m[3] != nil {
			value = *m[3]
		}
		if key != "model" && key != "model_reasoning_effort" {
			continue
		}
		if table == "" {
			top.Set(key, value)
		} else if strings.HasPrefix(table, "profiles.") {
			_, name, _ := strings.Cut(table, ".")
			name = strings.Trim(pytext.Strip(name), "\"'")
			p, ok := profiles.Value(name).(*pyjson.Object)
			if !ok {
				p = pyjson.NewObject()
				profiles.Set(name, p)
			}
			p.Set(key, value)
		}
	}
	return top, profiles
}

// tomlStrings returns every string-valued key of a TOML text grouped by
// table ("" for the top level). Table names keep their dotted form with
// quotes removed, e.g. "model_providers.azure".
func tomlStrings(text string) map[string]map[string]string {
	out := map[string]map[string]string{}
	table := ""
	for _, raw := range pytext.SplitLines(text, false) {
		line := pytext.Strip(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if header := reMatch(tomlTable, line, 2); header != nil {
			table = strings.NewReplacer("\"", "", "'", "").Replace(pytext.Strip(*header[1]))
			continue
		}
		m := reMatch(tomlString, line, 4)
		if m == nil {
			continue
		}
		value := ""
		if m[2] != nil {
			value = *m[2]
		} else if m[3] != nil {
			value = *m[3]
		}
		if out[table] == nil {
			out[table] = map[string]string{}
		}
		out[table][*m[1]] = value
	}
	return out
}
