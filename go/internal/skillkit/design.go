package skillkit

import (
	"math"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
)

// design.py: theme validation and rendering.

var (
	hexRe       = regexp.MustCompile(`\A#(?:[0-9A-Fa-f]{3}|[0-9A-Fa-f]{6}|[0-9A-Fa-f]{8})\n?\z`)
	roleNameRe  = regexp.MustCompile(`\A[a-z][a-z0-9_]*\n?\z`)
	variantName = regexp.MustCompile(`\A[a-z][a-z0-9_-]*\n?\z`)
)

var Variants = []string{"paper", "web", "video"}

var (
	geometry      = []string{"rectangle", "rounded_rectangle", "capsule", "circle", "line", "trapezoid", "cylinder", "layered_cylinder", "clipped_header", "rounded_top_rectangle"}
	strokeStyles  = []string{"solid", "dashed", "dotted", "dashdot"}
	caps          = []string{"butt", "round", "projecting"}
	joins         = []string{"miter", "round", "bevel"}
	emphasis      = []string{"quiet", "normal", "strong"}
	density       = []string{"compact", "balanced", "open"}
	alignment     = []string{"optical", "geometric"}
	readingOrder  = []string{"left_to_right", "top_to_bottom", "radial"}
	reducedMotion = []string{"remove", "crossfade", "shorten"}
	casing        = []string{"none", "uppercase", "lowercase", "title"}
	fontStyles    = []string{"normal", "italic"}
)

var requiredColorRoles = []string{"surface", "surface_alt", "text_primary", "text_secondary", "data_primary",
	"data_secondary", "baseline", "uncertainty", "success", "warning", "failure", "focus"}

var gateShared = []string{
	"Theme validates and artifacts name its version.",
	"No unexplained local colors, opacity, font sizes, radii, or motion curves.",
	"Hierarchy survives grayscale and meaning does not rely only on hue.",
	"Text, equations, labels, and annotations remain readable at delivery size.",
}

var gatePaper = []string{
	"Data transformation and uncertainty are auditable.",
	"Axes, scales, legends, units, and panel labels are unambiguous.",
	"SVG/PDF is unclipped, fonts render correctly, and final-width print works.",
}

// QualityGates is design.QUALITY_GATES (a fresh copy per call).
func QualityGates() *obj {
	return pyjson.NewObject(
		"shared", strList(gateShared),
		"paper", strList(gatePaper),
		"website", []any{
			"Keyboard focus, headings, alternative text, contrast, and reduced motion pass.",
			"Mobile order is preserved and media does not cause layout shift.",
		},
		"video", []any{
			"Safe area, captions, pacing, muted comprehension, and end card pass.",
			"Resolution, frame rate, duration, and encoded playback match the target.",
		},
	)
}

func dtext(v any, name string) (string, error) {
	t := pytext.Strip(str(or(v, "")))
	if t == "" {
		return "", errf(name + " is required")
	}
	return t, nil
}

func parseColor(v any, name string) (string, error) {
	t := pytext.Upper(pytext.Strip(str(or(v, ""))))
	if !hexRe.MatchString(t) {
		return "", errf(name + " must use #RGB, #RRGGBB, or #RRGGBBAA")
	}
	return t, nil
}

func luminance(hex string) (float64, error) {
	c, err := parseColor(hex, "color")
	if err != nil {
		return 0, err
	}
	v := strings.TrimLeft(c, "#")
	if len(v) == 3 {
		v = string([]byte{v[0], v[0], v[1], v[1], v[2], v[2]})
	}
	lin := [3]float64{}
	for i, idx := range []int{0, 2, 4} {
		n, _ := strconv.ParseInt(v[idx:idx+2], 16, 64)
		ch := float64(n) / 255.0
		if ch <= 0.04045 {
			lin[i] = ch / 12.92
		} else {
			lin[i] = math.Pow((ch+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*lin[0] + 0.7152*lin[1] + 0.0722*lin[2], nil
}

func contrastRatio(fg, bg string) (float64, error) {
	a, err := luminance(fg)
	if err != nil {
		return 0, err
	}
	b, err := luminance(bg)
	if err != nil {
		return 0, err
	}
	return (math.Max(a, b) + 0.05) / (math.Min(a, b) + 0.05), nil
}

func dmapping(v any, name string, required bool) (*obj, error) {
	if v == nil && !required {
		return pyjson.NewObject(), nil
	}
	o, ok := v.(*obj)
	if !ok {
		return nil, errf(name + " must be an object")
	}
	return copyObj(o), nil
}

func gfmt(x float64) string { return strconv.FormatFloat(x, 'g', -1, 64) }

type bound struct {
	set bool
	v   float64
}

var none = bound{}

func b(v float64) bound { return bound{true, v} }

func dnumber(v any, name string, lo, hi bound) error {
	if _, ok := v.(bool); ok {
		return errf(name + " must be a number")
	}
	f, ok, err := pyFloat(v)
	if err != nil {
		return err
	}
	if !ok {
		return errf(name + " must be a number")
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return errf(name + " must be finite")
	}
	if lo.set && f < lo.v {
		return errf(name + " must be >= " + gfmt(lo.v))
	}
	if hi.set && f > hi.v {
		return errf(name + " must be <= " + gfmt(hi.v))
	}
	return nil
}

func dinteger(v any, name string, minimum *int64) error {
	var n *big.Int
	switch x := v.(type) {
	case int64:
		n = big.NewInt(x)
	case *big.Int:
		n = x
	default:
		return errf(name + " must be an integer")
	}
	if minimum != nil && n.Cmp(big.NewInt(*minimum)) < 0 {
		return errf(name + " must be >= " + strconv.FormatInt(*minimum, 10))
	}
	return nil
}

func imin(v int64) *int64 { return &v }

func denum(v any, name string, allowed []string) error {
	t := pytext.Lower(pytext.Strip(str(or(v, ""))))
	if !inList(t, allowed) {
		s := append([]string{}, allowed...)
		sort.Strings(s)
		return errf(name + " must be one of: " + strings.Join(s, ", "))
	}
	return nil
}

func dstringList(v any, name string, required bool) error {
	if v == nil {
		if required {
			return errf(name + " must be a list of strings")
		}
		return nil
	}
	items, ok := v.([]any)
	if !ok {
		return errf(name + " must be a list of strings")
	}
	n := 0
	for _, it := range items {
		if pytext.Strip(str(it)) != "" {
			n++
		}
	}
	if required && n == 0 {
		return errf(name + " must contain at least one entry")
	}
	return nil
}

// DeepMerge is design.deep_merge(base, override).
func DeepMerge(base, override *obj) *obj {
	result := copyObj(base)
	for _, k := range override.Keys() {
		v := override.Value(k)
		vo, ok1 := v.(*obj)
		ro, ok2 := result.Value(k).(*obj)
		if ok1 && ok2 {
			result.Set(k, DeepMerge(ro, vo))
		} else {
			result.Set(k, v)
		}
	}
	return result
}

func hashable(v any) error {
	switch v.(type) {
	case []any:
		return uncaught("TypeError", "unhashable type: 'list'")
	case *obj:
		return uncaught("TypeError", "unhashable type: 'dict'")
	}
	return nil
}

func resolveVariant(theme *obj, variant any) (*obj, error) {
	document := copyObj(theme)
	variants := document.Delete("variants")
	if variants == nil && !theme.Has("variants") {
		variants = pyjson.NewObject()
	}
	if !truthy(variants) {
		variants = pyjson.NewObject()
	}
	if !truthy(variant) {
		document.Set("variants", variants)
		return document, nil
	}
	vm, ok := variants.(*obj)
	if !ok {
		return nil, errf("variants must be an object")
	}
	if err := hashable(variant); err != nil {
		return nil, err
	}
	var override any
	if s, ok := variant.(string); ok {
		override = vm.Value(s)
	}
	if override == nil {
		document.Set("variants", variants)
		return document, nil
	}
	oo, ok := override.(*obj)
	if !ok {
		return nil, errf("variant " + pyjson.Repr(variant) + " must be an object")
	}
	resolved := DeepMerge(document, oo)
	resolved.Set("variants", variants)
	return resolved, nil
}

// ValidateTheme is design.validate_theme(theme, variant=variant).
func ValidateTheme(theme any, variant any) (*obj, error) {
	errs, warns := []any{}, []any{}
	variantOut := or(variant, "base")
	t, ok := theme.(*obj)
	if !ok {
		return pyjson.NewObject("ok", false, "errors", []any{"theme must be an object"}, "warnings", []any{},
			"theme", pyjson.NewObject(), "variant", variantOut, "quality_gates", QualityGates()), nil
	}
	document, err := resolveVariant(t, variant)
	if err == nil {
		err = validateDocument(document, &errs, &warns)
	}
	if err != nil {
		if _, ok := err.(*Error); !ok {
			return nil, err
		}
		errs = append(errs, err.Error())
		document = copyObj(t)
	}
	if truthy(variant) {
		if s, ok := variant.(string); !ok || !inList(s, Variants) {
			warns = append(warns, "unknown variant "+pyjson.Repr(variant)+"; validated the base theme instead")
		}
	}
	return pyjson.NewObject("ok", len(errs) == 0, "errors", errs, "warnings", warns, "theme", document,
		"variant", variantOut, "quality_gates", QualityGates()), nil
}

// each iterates a mapping's items in order.
func each(o *obj, fn func(k string, v any) error) error {
	for _, k := range o.Keys() {
		if err := fn(k, o.Value(k)); err != nil {
			return err
		}
	}
	return nil
}

// enumerate is enumerate(value) for a JSON value.
func enumerate(v any, fn func(i int, item any) error) error {
	list, err := iterate(v)
	if err != nil {
		return err
	}
	for i, it := range list {
		if err := fn(i, it); err != nil {
			return err
		}
	}
	return nil
}

func validateDocument(doc *obj, errs, warns *[]any) error {
	addErr := func(s string) { *errs = append(*errs, s) }
	allowed := []string{"meta", "color", "typography", "shape", "spacing", "layout", "chart", "web", "motion", "video", "variants"}
	unknown := []string{}
	for _, k := range doc.Keys() {
		if !inList(k, allowed) {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		addErr("unknown theme system: " + k)
	}
	for _, k := range allowed[:10] {
		if !doc.Has(k) {
			addErr("missing theme system: " + k)
		}
	}
	if len(*errs) > 0 {
		return nil
	}

	meta, err := dmapping(doc.Value("meta"), "meta", true)
	if err != nil {
		return err
	}
	if _, err := dtext(meta.Value("name"), "meta.name"); err != nil {
		return err
	}
	if err := dinteger(meta.Value("version"), "meta.version", imin(1)); err != nil {
		return err
	}
	if _, err := dtext(meta.Value("description"), "meta.description"); err != nil {
		return err
	}
	if err := dstringList(meta.Value("intent"), "meta.intent", true); err != nil {
		return err
	}
	if err := dstringList(meta.Value("avoid"), "meta.avoid", false); err != nil {
		return err
	}

	color, err := dmapping(doc.Value("color"), "color", true)
	if err != nil {
		return err
	}
	roles, err := dmapping(color.Value("roles"), "color.roles", true)
	if err != nil {
		return err
	}
	for _, r := range requiredColorRoles {
		if !roles.Has(r) {
			addErr("missing semantic color role: " + r)
		}
	}
	err = each(roles, func(role string, tok any) error {
		if !roleNameRe.MatchString(role) {
			addErr("color role " + pyjson.Repr(role) + " must be semantic (lowercase snake_case)")
			return nil
		}
		token, err := dmapping(tok, "color.roles."+role, true)
		if err != nil {
			return err
		}
		if _, err := parseColor(token.Value("value"), "color.roles."+role+".value"); err != nil {
			return err
		}
		if err := dnumber(get(token, "opacity", 1.0), "color.roles."+role+".opacity", b(0), b(1)); err != nil {
			return err
		}
		_, err = dtext(token.Value("usage"), "color.roles."+role+".usage")
		return err
	})
	if err != nil {
		return err
	}
	for _, palette := range []string{"categorical", "sequential", "diverging"} {
		err := enumerate(or(get(color, palette, []any{}), []any{}), func(i int, v any) error {
			_, err := parseColor(v, "color."+palette+"["+strconv.Itoa(i)+"]")
			return err
		})
		if err != nil {
			return err
		}
	}
	op, err := dmapping(get(color, "opacity", pyjson.NewObject()), "color.opacity", true)
	if err != nil {
		return err
	}
	if err := each(op, func(k string, v any) error {
		return dnumber(v, "color.opacity."+k, b(0), b(1))
	}); err != nil {
		return err
	}

	typo, err := dmapping(doc.Value("typography"), "typography", true)
	if err != nil {
		return err
	}
	for _, family := range []string{"sans", "serif", "mono"} {
		stack, err := dmapping(typo.Value(family), "typography."+family, true)
		if err != nil {
			return err
		}
		if err := dstringList(stack.Value("family"), "typography."+family+".family", true); err != nil {
			return err
		}
		if err := dinteger(stack.Value("weight"), "typography."+family+".weight", nil); err != nil {
			return err
		}
		w, _ := asNum(stack.Value("weight"))
		if w.cmpInt(100) < 0 || w.cmpInt(900) > 0 {
			addErr("typography." + family + ".weight must be between 100 and 900")
		}
		if err := denum(get(stack, "style", "normal"), "typography."+family+".style", fontStyles); err != nil {
			return err
		}
	}
	if _, err := dtext(typo.Value("math"), "typography.math"); err != nil {
		return err
	}
	for _, sec := range []string{"roles_pt", "line_height"} {
		m, err := dmapping(typo.Value(sec), "typography."+sec, true)
		if err != nil {
			return err
		}
		if err := each(m, func(k string, v any) error {
			return dnumber(v, "typography."+sec+"."+k, b(0.1), none)
		}); err != nil {
			return err
		}
	}
	cas, err := dmapping(typo.Value("casing"), "typography.casing", true)
	if err != nil {
		return err
	}
	if err := each(cas, func(k string, v any) error {
		return denum(v, "typography.casing."+k, casing)
	}); err != nil {
		return err
	}

	shape, err := dmapping(doc.Value("shape"), "shape", true)
	if err != nil {
		return err
	}
	strokes, err := dmapping(shape.Value("strokes"), "shape.strokes", true)
	if err != nil {
		return err
	}
	if err := each(strokes, func(name string, v any) error {
		p := "shape.strokes." + name
		s, err := dmapping(v, p, true)
		if err != nil {
			return err
		}
		c, err := dtext(s.Value("color"), p+".color")
		if err != nil {
			return err
		}
		if !roles.Has(c) && c != "none" {
			addErr(p + ".color references unknown color role " + pyjson.Repr(c))
		}
		if err := dnumber(s.Value("width_pt"), p+".width_pt", b(0), none); err != nil {
			return err
		}
		if err := denum(get(s, "style", "solid"), p+".style", strokeStyles); err != nil {
			return err
		}
		if err := denum(get(s, "cap", "round"), p+".cap", caps); err != nil {
			return err
		}
		return denum(get(s, "join", "round"), p+".join", joins)
	}); err != nil {
		return err
	}
	shadows, err := dmapping(shape.Value("shadows"), "shape.shadows", true)
	if err != nil {
		return err
	}
	if err := each(shadows, func(name string, v any) error {
		p := "shape.shadows." + name
		s, err := dmapping(v, p, true)
		if err != nil {
			return err
		}
		c, err := dtext(s.Value("color"), p+".color")
		if err != nil {
			return err
		}
		if !roles.Has(c) && c != "none" {
			addErr(p + ".color references unknown color role " + pyjson.Repr(c))
		}
		if err := dnumber(s.Value("opacity"), p+".opacity", b(0), b(1)); err != nil {
			return err
		}
		return dnumber(s.Value("blur"), p+".blur", b(0), none)
	}); err != nil {
		return err
	}
	vocab, err := dmapping(shape.Value("vocabulary"), "shape.vocabulary", true)
	if err != nil {
		return err
	}
	if err := each(vocab, func(name string, v any) error {
		p := "shape.vocabulary." + name
		it, err := dmapping(v, p, true)
		if err != nil {
			return err
		}
		if err := denum(it.Value("geometry"), p+".geometry", geometry); err != nil {
			return err
		}
		fill, err := dtext(it.Value("fill"), p+".fill")
		if err != nil {
			return err
		}
		st, err := dmapping(it.Value("stroke"), p+".stroke", true)
		if err != nil {
			return err
		}
		sc, err := dtext(st.Value("color"), p+".stroke.color")
		if err != nil {
			return err
		}
		if !roles.Has(fill) && fill != "none" {
			addErr(p + ".fill references unknown color role " + pyjson.Repr(fill))
		}
		if !roles.Has(sc) && sc != "none" {
			addErr(p + ".stroke.color references unknown role " + pyjson.Repr(sc))
		}
		if err := dnumber(get(it, "radius", int64(0)), p+".radius", b(0), none); err != nil {
			return err
		}
		padding := get(it, "padding", []any{int64(0), int64(0)})
		if pl, ok := padding.([]any); !ok || len(pl) < 2 || len(pl) > 4 {
			addErr(p + ".padding must have 2-4 values")
		}
		return denum(get(it, "emphasis", "normal"), p+".emphasis", emphasis)
	}); err != nil {
		return err
	}
	spacing, err := dmapping(doc.Value("spacing"), "spacing", true)
	if err != nil {
		return err
	}
	if err := dnumber(spacing.Value("base"), "spacing.base", b(0.01), none); err != nil {
		return err
	}
	if err := enumerate(or(spacing.Value("scale"), []any{}), func(i int, v any) error {
		return dnumber(v, "spacing.scale["+strconv.Itoa(i)+"]", b(0), none)
	}); err != nil {
		return err
	}
	if err := denum(spacing.Value("density"), "spacing.density", density); err != nil {
		return err
	}

	layout, err := dmapping(doc.Value("layout"), "layout", true)
	if err != nil {
		return err
	}
	for _, sec := range []string{"figure_width_in", "aspect_ratios"} {
		m, err := dmapping(layout.Value(sec), "layout."+sec, true)
		if err != nil {
			return err
		}
		if err := each(m, func(k string, v any) error {
			return dnumber(v, "layout."+sec+"."+k, b(0.01), none)
		}); err != nil {
			return err
		}
	}
	if err := dinteger(layout.Value("grid_columns"), "layout.grid_columns", imin(1)); err != nil {
		return err
	}
	if err := dnumber(layout.Value("gutter"), "layout.gutter", b(0), none); err != nil {
		return err
	}
	if err := dnumber(layout.Value("outer_margin"), "layout.outer_margin", b(0), none); err != nil {
		return err
	}
	if err := denum(layout.Value("alignment"), "layout.alignment", alignment); err != nil {
		return err
	}
	if err := denum(layout.Value("reading_order"), "layout.reading_order", readingOrder); err != nil {
		return err
	}

	chart, err := dmapping(doc.Value("chart"), "chart", true)
	if err != nil {
		return err
	}
	for _, k := range []string{"axes", "lines", "markers", "grid", "legend", "uncertainty"} {
		if _, err := dmapping(chart.Value(k), "chart."+k, true); err != nil {
			return err
		}
	}
	if err := dstringList(chart.Value("rules"), "chart.rules", false); err != nil {
		return err
	}

	web, err := dmapping(doc.Value("web"), "web", true)
	if err != nil {
		return err
	}
	if err := dinteger(web.Value("content_width_px"), "web.content_width_px", imin(1)); err != nil {
		return err
	}
	bp, err := dmapping(web.Value("breakpoints_px"), "web.breakpoints_px", true)
	if err != nil {
		return err
	}
	if err := each(bp, func(k string, v any) error {
		return dinteger(v, "web.breakpoints_px."+k, imin(1))
	}); err != nil {
		return err
	}
	rs, err := dmapping(web.Value("radius_scale_px"), "web.radius_scale_px", true)
	if err != nil {
		return err
	}
	if err := each(rs, func(k string, v any) error {
		return dnumber(v, "web.radius_scale_px."+k, b(0), none)
	}); err != nil {
		return err
	}
	if _, err := dmapping(web.Value("focus_ring"), "web.focus_ring", true); err != nil {
		return err
	}
	tm, err := dmapping(web.Value("transitions_ms"), "web.transitions_ms", true)
	if err != nil {
		return err
	}
	if err := each(tm, func(k string, v any) error {
		return dinteger(v, "web.transitions_ms."+k, imin(0))
	}); err != nil {
		return err
	}

	motion, err := dmapping(doc.Value("motion"), "motion", true)
	if err != nil {
		return err
	}
	if err := dstringList(motion.Value("principles"), "motion.principles", false); err != nil {
		return err
	}
	curves, err := dmapping(motion.Value("curves"), "motion.curves", true)
	if err != nil {
		return err
	}
	if err := each(curves, func(name string, v any) error {
		p := "motion.curves." + name
		c, err := dmapping(v, p, true)
		if err != nil {
			return err
		}
		if err := dinteger(c.Value("duration_ms"), p+".duration_ms", imin(0)); err != nil {
			return err
		}
		if _, err := dtext(c.Value("easing"), p+".easing"); err != nil {
			return err
		}
		if err := dnumber(get(c, "distance_px", int64(0)), p+".distance_px", b(0), none); err != nil {
			return err
		}
		return dnumber(get(c, "opacity_from", int64(1)), p+".opacity_from", b(0), b(1))
	}); err != nil {
		return err
	}
	if err := denum(motion.Value("reduced_motion"), "motion.reduced_motion", reducedMotion); err != nil {
		return err
	}

	video, err := dmapping(doc.Value("video"), "video", true)
	if err != nil {
		return err
	}
	for _, k := range []string{"width", "height", "fps"} {
		if err := dinteger(video.Value(k), "video."+k, imin(1)); err != nil {
			return err
		}
	}
	if err := dnumber(video.Value("safe_area_percent"), "video.safe_area_percent", b(0), b(25)); err != nil {
		return err
	}
	if err := dnumber(video.Value("title_duration_s"), "video.title_duration_s", b(0.01), none); err != nil {
		return err
	}
	if _, err := dtext(video.Value("transition"), "video.transition"); err != nil {
		return err
	}
	if _, err := dmapping(video.Value("caption"), "video.caption", true); err != nil {
		return err
	}

	// Cross-system invariant (DesignError inside is swallowed).
	roleValue := func(name string) string {
		if o, ok := roles.Value(name).(*obj); ok {
			return str(get(o, "value", ""))
		}
		return ""
	}
	surface, primary, secondary := roleValue("surface"), roleValue("text_primary"), roleValue("text_secondary")
	cross := func() error {
		if surface != "" && primary != "" {
			r, err := contrastRatio(primary, surface)
			if err != nil {
				return err
			}
			if r < 4.5 {
				addErr("text_primary/surface contrast is below WCAG AA 4.5:1")
			}
		}
		if surface != "" && secondary != "" {
			r, err := contrastRatio(secondary, surface)
			if err != nil {
				return err
			}
			if r < 3.0 {
				*warns = append(*warns, "text_secondary/surface contrast is below 3.0:1")
			}
		}
		for _, name := range []string{"text_primary", "text_secondary"} {
			if tok, ok := roles.Value(name).(*obj); ok {
				f, ok, err := pyFloat(get(tok, "opacity", int64(1)))
				if err != nil {
					return err
				}
				if !ok {
					return errf("could not convert to float")
				}
				if f < 0.9 {
					*warns = append(*warns, name+" uses opacity below 0.9; use a readable text color instead")
				}
			}
		}
		cat := []string{}
		if err := enumerate(or(color.Value("categorical"), []any{}), func(_ int, v any) error {
			c, err := parseColor(v, "categorical")
			cat = append(cat, c)
			return err
		}); err != nil {
			return err
		}
		if len(cat) >= 2 {
			lums := map[float64]bool{}
			for _, c := range cat {
				l, _ := luminance(c)
				r, _ := strconv.ParseFloat(strconv.FormatFloat(l, 'f', 3, 64), 64)
				lums[r] = true
			}
			if len(lums) == 1 {
				*warns = append(*warns, "categorical palette collapses to one luminance in grayscale")
			}
		}
		return nil
	}
	if err := cross(); err != nil {
		if _, ok := err.(*Error); !ok {
			return err
		}
	}

	if variants, ok := doc.Get("variants"); ok && variants != nil {
		vm, isMap := variants.(*obj)
		if !isMap {
			addErr("variants must be an object")
			return uncaught("AttributeError", "'"+typeName(variants)+"' object has no attribute 'items'")
		}
		for _, name := range vm.Keys() {
			if !variantName.MatchString(name) {
				addErr("variant name " + pyjson.Repr(name) + " must be lowercase snake_case")
			}
			if !inList(name, Variants) {
				*warns = append(*warns, "variant "+pyjson.Repr(name)+" is not one of paper/web/video")
			}
			if !isMapping(vm.Value(name)) {
				addErr("variant " + pyjson.Repr(name) + " must be an object")
			}
		}
	}
	return nil
}

// ExampleTheme is design.example_theme().
func ExampleTheme() *obj {
	v, _ := pyjson.Loads(assets["design/theme.json"])
	o, _ := v.(*obj)
	return o
}

func RenderDesignBrief(context *obj) (string, error) {
	merged := withDefaults(pyjson.NewObject(
		"project_name", "ARK project", "audience", "researchers and maintainers", "venue", "internal report",
		"mediums", []any{"paper figure", "project website"},
		"qualities", []any{"precise", "calm", "technical"},
		"anti_goals", []any{"decorative gradients", "low-contrast gray text"},
		"theme_name", "academic-clean", "theme_version", "1", "artifact", "assets/figure.svg",
	), context)
	return substitute(assets["design/design_brief.md"], merged)
}

func RenderStyleReview(context *obj) (string, error) {
	merged := withDefaults(pyjson.NewObject(
		"artifact", "assets/figure.svg", "theme_name", "academic-clean", "theme_version", "1", "mode", "paper",
		"shared_checks", strList(gateShared), "mode_checks", strList(gatePaper),
		"verification", "_Run verify_artifact() before ticking these gates._",
	), context)
	return substitute(assets["design/style_review.md"], merged)
}
