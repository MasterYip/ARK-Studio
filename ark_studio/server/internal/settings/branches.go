package settings

import (
	"regexp"

	"arkstudio/go/internal/compat/pyjson"
)

// Branches is settings.branches (Go only, Q-0003): {"prefix": "<namespace>"}
// names the tag namespace of lines of work (store/branches.go; default
// "branch").  nil, null and {} mean the default (nil result).
func Branches(value any) (*pyjson.Object, error) {
	if value == nil {
		return nil, nil
	}
	o, ok := value.(*pyjson.Object)
	if !ok {
		return nil, &Error{"branches settings must be an object"}
	}
	for _, k := range o.Keys() {
		if k != "prefix" {
			return nil, &Error{"unknown branches setting: " + k}
		}
	}
	raw := o.Value("prefix")
	if raw == nil || raw == "" {
		return nil, nil
	}
	prefix, ok := raw.(string)
	if !ok || !branchPrefixRe.MatchString(prefix) || reservedPrefix[prefix] {
		return nil, &Error{"branches.prefix must be a lowercase tag namespace (letters, digits, - or _, at most 24) other than type, status, importance, needs or marker"}
	}
	if prefix == "branch" {
		return nil, nil
	}
	return pyjson.NewObject("prefix", prefix), nil
}

var branchPrefixRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,23}$`)

var reservedPrefix = map[string]bool{"type": true, "status": true, "importance": true, "needs": true, "marker": true}
