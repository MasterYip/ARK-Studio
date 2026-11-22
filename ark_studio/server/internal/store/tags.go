package store

import (
	"regexp"
	"sort"
	"strings"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// Ports of arkstudio/tags.py (write side).

const (
	needsNS           = "needs"
	defaultType       = "note"
	defaultStatus     = "open"
	defaultImportance = "p2"
)

var needsPendingValues = []string{"review", "decision"}

func attrErr(v any, name string) error { return pyerr.AttrError(v, name) }

// tag regexes: TAG_RE and NS_RE with Python's Unicode \s, \w and \d.
var (
	tagRe = regexp.MustCompile(`^[^` + pytext.SpaceClass + `\x00-\x1f]{1,72}$`)
	nsRe  = regexp.MustCompile(`^[\p{L}\p{Nl}\p{No}][\p{L}\p{N}_-]{0,23}$`)
)

func reprStr(s string) string { return pyjson.Repr(s) }

// splitTag is tags.split_tag.
func splitTag(raw any) (string, string, error) {
	text := ""
	if pyjson.Truthy(raw) {
		text = pyjson.Str(raw)
	}
	text = pytext.Strip(text)
	if text == "" {
		return "", "", storeErr("empty tag")
	}
	var ns, value string
	if i := strings.IndexByte(text, ':'); i >= 0 {
		ns, value = pytext.Lower(pytext.Strip(text[:i])), pytext.Strip(text[i+1:])
	} else {
		ns, value = "tag", text
	}
	if !nsRe.MatchString(ns) {
		return "", "", storeErr("bad tag namespace: %s", reprStr(ns))
	}
	if !tagRe.MatchString(value) {
		return "", "", storeErr("bad tag value: %s", reprStr(value))
	}
	return ns, value, nil
}

func knownType(t string) bool { _, ok := TypePrefixes[t]; return ok }

// fieldValue is the explicit type/status/importance argument of
// normalize_tags: None stays unset, a str is stripped and lowered, anything
// else raises AttributeError on .strip().
func fieldValue(v any) (string, bool, error) {
	if v == nil {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", false, attrErr(v, "strip")
	}
	return pytext.Lower(pytext.Strip(s)), true, nil
}

// normalizeTags is tags.normalize_tags.  typeName/status/importance are the
// raw explicit values (nil = None).
func normalizeTags(tags []any, typeName, status, importance any) ([]string, error) {
	collected := map[string]bool{}
	var types, statuses, imps []string
	for _, raw := range tags {
		ns, value, err := splitTag(raw)
		if err != nil {
			return nil, err
		}
		switch ns {
		case "type":
			value = pytext.Lower(value)
			if !knownType(value) {
				return nil, storeErr("unknown type: %s", reprStr(value))
			}
			types = append(types, value)
		case "status":
			value = pytext.Lower(value)
			if !containsString(KnownStatuses, value) {
				return nil, storeErr("unknown status: %s", reprStr(value))
			}
			statuses = append(statuses, value)
		case "importance":
			value = pytext.Lower(value)
			if !containsString(KnownImportance, value) {
				return nil, storeErr("unknown importance: %s", reprStr(value))
			}
			imps = append(imps, value)
		default:
			collected[ns+":"+value] = true
		}
	}
	if v, ok, err := fieldValue(typeName); err != nil {
		return nil, err
	} else if ok {
		if !knownType(v) {
			return nil, storeErr("unknown type: %s", reprStr(v))
		}
		types = append(types, v)
	}
	if v, ok, err := fieldValue(status); err != nil {
		return nil, err
	} else if ok {
		if !containsString(KnownStatuses, v) {
			return nil, storeErr("unknown status: %s", reprStr(v))
		}
		statuses = append(statuses, v)
	}
	if v, ok, err := fieldValue(importance); err != nil {
		return nil, err
	} else if ok {
		if !containsString(KnownImportance, v) {
			return nil, storeErr("unknown importance: %s", reprStr(v))
		}
		imps = append(imps, v)
	}
	one := func(values []string, def, label string) (string, error) {
		set := map[string]bool{}
		var unique []string
		for _, v := range values {
			if !set[v] {
				set[v] = true
				unique = append(unique, v)
			}
		}
		sort.Strings(unique)
		if len(unique) > 1 {
			return "", storeErr("a unit has exactly one %s; got %s", label, strings.Join(unique, ", "))
		}
		if len(unique) == 1 {
			return unique[0], nil
		}
		return def, nil
	}
	ft, err := one(types, defaultType, "type")
	if err != nil {
		return nil, err
	}
	fs, err := one(statuses, defaultStatus, "status")
	if err != nil {
		return nil, err
	}
	fi, err := one(imps, defaultImportance, "importance")
	if err != nil {
		return nil, err
	}
	free := make([]string, 0, len(collected))
	for t := range collected {
		free = append(free, t)
	}
	sort.Strings(free)
	ordered := append([]string{"type:" + ft, "status:" + fs, "importance:" + fi}, free...)
	seen := map[string]bool{}
	out := ordered[:0]
	for _, t := range ordered {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// needsPending is tags.needs_pending.
func needsPending(typeName, status string, archived bool) string {
	if archived {
		return ""
	}
	if status == "review" {
		return "review"
	}
	if IsOpenDecision(typeName, status) {
		return "decision"
	}
	return ""
}

// needsTransition is tags.needs_transition ("" = None).
func needsTransition(current, typeName, status string, archived bool) string {
	if p := needsPending(typeName, status, archived); p != "" {
		return p
	}
	if !containsString(needsPendingValues, current) {
		return current
	}
	if archived || status == "archived" {
		return "dismissed"
	}
	if status == "done" {
		if current == "review" {
			return "accepted"
		}
		return "decided"
	}
	if current == "review" {
		return "sent-back"
	}
	return "dismissed"
}

// withoutNeeds is store._without_needs.
func withoutNeeds(tags []any) []any {
	out := make([]any, 0, len(tags))
	for _, tag := range tags {
		text := ""
		if pyjson.Truthy(tag) {
			text = pyjson.Str(tag)
		}
		text = pytext.Strip(text)
		if i := strings.IndexByte(text, ':'); i >= 0 && pytext.Lower(pytext.Strip(text[:i])) == needsNS {
			continue
		}
		out = append(out, tag)
	}
	return out
}

// needsOf is store._needs_of.
func needsOf(tags []string) string {
	for _, t := range tags {
		if TagNamespace(t) == needsNS {
			return TagValue(t)
		}
	}
	return ""
}

// typePrefix is tags.type_prefix.
func typePrefix(t string) string {
	if p, ok := TypePrefixes[t]; ok {
		return p
	}
	return "U"
}
