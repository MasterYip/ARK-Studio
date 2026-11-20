package settings

import (
	"sort"

	"arkstudio/go/internal/compat/pyjson"
)

// prefs.home_filter = {project_id: {"limit", "types", "statuses", "days"}}:
// which nodes one person's Home graph shows in a project.  "limit" keeps the
// most recently updated nodes (0 = all of them), "types" / "statuses" keep
// only those values (empty = any), "days" only nodes updated in that many
// days (0 = any time).  The overview applies it on the server, so a large
// project's Home fetches only what it draws.

// HomeFilterLimits are the limits the UI offers (0 = all).
var HomeFilterLimits = []int64{200, 500, 1000, 2000, 0}

// HomeFilterDays are the time windows the UI offers (0 = any time).
var HomeFilterDays = []int64{0, 7, 30, 90}

// DefaultHomeLimit is the limit when a person has not chosen one: Home stays
// fast on large projects (docs/guide/home-overview.md, "Home shows").
const DefaultHomeLimit int64 = 500

const (
	maxHomeFilterProjects = 50
	maxHomeFilterValues   = 40
	maxHomeFilterLimit    = 1_000_000
	maxHomeFilterDays     = 3650
	maxHomeFilterValueLen = 64
)

// HomeFilter is one project's Home graph filter.
type HomeFilter struct {
	Limit    int64
	Types    []string
	Statuses []string
	Days     int64
}

// DefaultHomeFilter is the filter before a person changes anything.
func DefaultHomeFilter() HomeFilter { return HomeFilter{Limit: DefaultHomeLimit} }

// Object is the stored / reported form.
func (f HomeFilter) Object() *pyjson.Object {
	return pyjson.NewObject("limit", f.Limit, "types", stringsAny(f.Types), "statuses", stringsAny(f.Statuses), "days", f.Days)
}

func stringsAny(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

// HomeFilterError is an invalid prefs.home_filter value.
type HomeFilterError struct{ Message string }

func (e HomeFilterError) Error() string { return e.Message }

func homeFilterErr(msg string) error {
	return HomeFilterError{"invalid Home filter preferences: " + msg}
}

func homeInt(v any, lo, hi int64) (int64, bool) {
	n, ok := gdNumberValue(v)
	if !ok || n != float64(int64(n)) {
		return 0, false
	}
	return int64(n), int64(n) >= lo && int64(n) <= hi
}

func homeValues(v any) ([]string, bool) {
	if v == nil {
		return nil, true
	}
	list, ok := v.([]any)
	if !ok || len(list) > maxHomeFilterValues {
		return nil, false
	}
	seen := map[string]bool{}
	var out []string
	for _, item := range list {
		s, ok := item.(string)
		if !ok || s == "" || len([]rune(s)) > maxHomeFilterValueLen || !validProjectID(s) {
			return nil, false
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, true
}

// ParseHomeFilter cleans one project's entry; strict refuses any invalid
// part, lenient falls back to the default for it.
func ParseHomeFilter(value any, strict bool) (HomeFilter, error) {
	f := DefaultHomeFilter()
	v, ok := value.(*pyjson.Object)
	if !ok || (strict && !onlyKeys(v, "limit", "types", "statuses", "days")) {
		if strict {
			return f, homeFilterErr("a project entry has limit, types, statuses and days")
		}
		return f, nil
	}
	if raw, has := v.Get("limit"); has {
		if n, ok := homeInt(raw, 0, maxHomeFilterLimit); ok {
			f.Limit = n
		} else if strict {
			return f, homeFilterErr("limit must be a whole number from 0 (all) up")
		}
	}
	if raw, has := v.Get("days"); has {
		if n, ok := homeInt(raw, 0, maxHomeFilterDays); ok {
			f.Days = n
		} else if strict {
			return f, homeFilterErr("days must be a whole number from 0 (any time) to 3650")
		}
	}
	for _, key := range []string{"types", "statuses"} {
		list, ok := homeValues(v.Value(key))
		if !ok {
			if strict {
				return f, homeFilterErr(key + " must be a list of at most 40 names")
			}
			continue
		}
		if key == "types" {
			f.Types = list
		} else {
			f.Statuses = list
		}
	}
	return f, nil
}

// HomeFilterPrefs cleans a stored prefs.home_filter (lenient read path).
func HomeFilterPrefs(value any) *pyjson.Object {
	out := pyjson.NewObject()
	v, ok := value.(*pyjson.Object)
	if !ok {
		return out
	}
	for _, pid := range v.Keys() {
		if !validProjectID(pid) {
			continue
		}
		if _, ok := v.Value(pid).(*pyjson.Object); !ok {
			continue
		}
		f, _ := ParseHomeFilter(v.Value(pid), false)
		out.Set(pid, f.Object())
	}
	for out.Len() > maxHomeFilterProjects {
		out.Delete(out.Keys()[0])
	}
	return out
}

// HomeFilterFor is the filter a person chose for a project (the default
// when none).
func HomeFilterFor(prefs *pyjson.Object, projectID string) HomeFilter {
	if prefs == nil {
		return DefaultHomeFilter()
	}
	all, ok := prefs.Value("home_filter").(*pyjson.Object)
	if !ok {
		return DefaultHomeFilter()
	}
	entry, ok := all.Value(projectID).(*pyjson.Object)
	if !ok {
		return DefaultHomeFilter()
	}
	f, _ := ParseHomeFilter(entry, false)
	return f
}

// UpdateHomeFilter applies a PATCH /api/me prefs.home_filter patch
// {project_id: entry | null}: an entry replaces that project's filter (and
// makes it the most recent), null restores the default.  The oldest
// projects are dropped beyond the limit.
func UpdateHomeFilter(base, patch any) (*pyjson.Object, error) {
	p, ok := patch.(*pyjson.Object)
	if !ok {
		return nil, homeFilterErr("expected an object keyed by project id")
	}
	if p.Len() > maxHomeFilterProjects {
		return nil, homeFilterErr("too many projects in one update")
	}
	merged := HomeFilterPrefs(base)
	for _, pid := range p.Keys() {
		if !validProjectID(pid) {
			return nil, homeFilterErr("invalid project id")
		}
		merged.Delete(pid)
		raw := p.Value(pid)
		if raw == nil {
			continue
		}
		f, err := ParseHomeFilter(raw, true)
		if err != nil {
			return nil, err
		}
		merged.Set(pid, f.Object())
	}
	for merged.Len() > maxHomeFilterProjects {
		merged.Delete(merged.Keys()[0])
	}
	return merged, nil
}
