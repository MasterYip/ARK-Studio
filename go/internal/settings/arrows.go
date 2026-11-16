package settings

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/store"
)

// Arrowheads: ARROW_* constants and the helpers of arkstudio/settings.py.
var (
	arrowHeads      = []string{"none", "triangle", "open", "stealth", "diamond", "diamond_filled", "circle", "bar"}
	arrowPlacements = []string{"target", "source", "both", "none"}
	arrowStyleKeys  = []string{"head", "placement", "size"}
	arrowSizeRange  = [2]float64{0.5, 2.5}
)

func arrowDefaultStyle() *pyjson.Object {
	return pyjson.NewObject("head", "triangle", "placement", "target", "size", 1.0)
}

func arrowDefaultRelations() *pyjson.Object {
	out := pyjson.NewObject()
	for _, rel := range store.SymmetricRelations {
		out.Set(rel, pyjson.NewObject("placement", "none"))
	}
	return out
}

func arrowDefaults() *pyjson.Object {
	return pyjson.NewObject("style", arrowDefaultStyle(), "relations", arrowDefaultRelations(), "mind_tree", false)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// pyRound3 is round(x, 3) for a finite float.
func pyRound3(x float64) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 3, 64), 64)
	return f
}

// arrowStyle is _arrow_style(value, partial=..., strict=..., where=...).
// allowReverse (per-relation entries, main 480f4ad) also accepts a boolean
// "reverse" (display only).
func arrowStyle(value any, partial, strict bool, where string, allowReverse ...bool) (*pyjson.Object, error) {
	reverseOK := len(allowReverse) > 0 && allowReverse[0]
	v, ok := value.(*pyjson.Object)
	if !ok {
		if strict {
			return nil, &Error{where + " must be an object"}
		}
		if partial {
			return pyjson.NewObject(), nil
		}
		return arrowDefaultStyle(), nil
	}
	if strict {
		var unknown []string
		for _, k := range v.Keys() {
			if !contains(arrowStyleKeys, k) && !(reverseOK && k == "reverse") {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return nil, &Error{"unknown " + where + " field: " + unknown[0]}
		}
	}
	out := pyjson.NewObject()
	if !partial {
		out = arrowDefaultStyle()
	}
	for _, c := range []struct {
		key     string
		choices []string
	}{{"head", arrowHeads}, {"placement", arrowPlacements}} {
		raw, has := v.Get(c.key)
		if !has {
			continue
		}
		if s, isStr := raw.(string); isStr && contains(c.choices, s) {
			out.Set(c.key, s)
		} else if strict {
			return nil, &Error{where + " " + c.key + " must be one of: " + strings.Join(c.choices, ", ")}
		}
	}
	if raw, has := v.Get("reverse"); has && reverseOK {
		if b, isBool := raw.(bool); isBool {
			out.Set("reverse", b)
		} else if strict {
			return nil, &Error{where + " reverse must be true or false"}
		}
	}
	if raw, has := v.Get("size"); has {
		number, valid := 0.0, false
		switch raw.(type) {
		case bool, nil, *pyjson.Object, []any:
		default:
			number, valid = pyFloat(raw)
		}
		low, high := arrowSizeRange[0], arrowSizeRange[1]
		if valid && !math.IsInf(number, 0) && !math.IsNaN(number) && low <= number && number <= high {
			out.Set("size", pyRound3(number))
		} else if strict {
			return nil, &Error{where + " size must be between " + fmtG(low) + " and " + fmtG(high)}
		}
	}
	return out, nil
}

// arrowRelations is _arrow_relations(value, strict=...).
func arrowRelations(value any, strict bool) (*pyjson.Object, error) {
	v, ok := value.(*pyjson.Object)
	if !ok {
		if strict {
			return nil, &Error{"arrow relations must be an object"}
		}
		return arrowDefaultRelations(), nil
	}
	out := pyjson.NewObject()
	for _, rel := range v.Keys() {
		if !contains(store.Relations, rel) {
			if strict {
				return nil, &Error{"unknown relation: " + rel}
			}
			continue
		}
		style, err := arrowStyle(v.Value(rel), true, strict, "arrow style for "+rel, true)
		if err != nil {
			return nil, err
		}
		if style.Len() > 0 {
			out.Set(rel, style)
		}
	}
	return out, nil
}

// NormalizeArrows is normalize_arrows(value).
func NormalizeArrows(value any) *pyjson.Object {
	source, _ := value.(*pyjson.Object)
	style, _ := arrowStyle(source.Value("style"), false, false, "arrow style")
	var relsIn any = arrowDefaultRelations()
	if r, has := source.Get("relations"); has {
		relsIn = r
	}
	rels, _ := arrowRelations(relsIn, false)
	return pyjson.NewObject("style", style, "relations", rels, "mind_tree", source.Value("mind_tree") == true)
}

// UpdateArrows is update_arrows(base, patch).
func UpdateArrows(base any, patch any) (*pyjson.Object, error) {
	merged := NormalizeArrows(base)
	p, ok := patch.(*pyjson.Object)
	if !ok {
		return nil, &Error{"arrow settings must be an object"}
	}
	var unknown []string
	for _, k := range p.Keys() {
		if k != "style" && k != "relations" && k != "mind_tree" {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, &Error{"unknown arrow setting: " + unknown[0]}
	}
	if raw, has := p.Get("style"); has {
		style, err := arrowStyle(raw, true, true, "arrow style")
		if err != nil {
			return nil, err
		}
		next := merged.Value("style").(*pyjson.Object).Copy()
		for _, k := range style.Keys() {
			next.Set(k, style.Value(k))
		}
		merged.Set("style", next)
	}
	if raw, has := p.Get("relations"); has {
		rels, err := arrowRelations(raw, true)
		if err != nil {
			return nil, err
		}
		merged.Set("relations", rels)
	}
	if raw, has := p.Get("mind_tree"); has {
		b, isBool := raw.(bool)
		if !isBool {
			return nil, &Error{"mind_tree must be true or false"}
		}
		merged.Set("mind_tree", b)
	}
	return merged, nil
}

// personalArrows is personal_arrows(value).
func personalArrows(value any) (*pyjson.Object, error) {
	v, ok := value.(*pyjson.Object)
	if !ok {
		return nil, &Error{"arrow settings must be an object"}
	}
	if !v.Has("style") {
		return nil, &Error{"personal arrow settings need: style"}
	}
	style, err := arrowStyle(v.Value("style"), true, true, "arrow style")
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, k := range arrowStyleKeys {
		if !style.Has(k) {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, &Error{"personal arrow style needs: " + strings.Join(missing, ", ")}
	}
	return UpdateArrows(arrowDefaults(), v)
}
