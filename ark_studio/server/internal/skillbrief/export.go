package skillbrief

import (
	"math/big"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// Exported normalizers of interface.py for the agent-team task contract
// (skillkit.PrepareTask).  Each appends to d in Python's order.

func NewDiag() *Diag { return &Diag{Errors: []any{}, Warnings: []any{}} }

func (d *Diag) Err(msg string)  { d.Errors = append(d.Errors, msg) }
func (d *Diag) Warn(msg string) { d.Warnings = append(d.Warnings, msg) }

var TaskStructures = structureOrder

func NormalizeTeam(d *Diag, value any, structure, reviewer string) (*obj, error) {
	return normalizeTeam(d, value, structure, reviewer)
}

func NormalizeContext(d *Diag, value any) (*obj, error) { return normalizeContext(d, value) }

func NormalizeVerification(d *Diag, value any, gates []any, acceptance []*obj, acceptanceException bool) *obj {
	return normalizeVerification(d, value, gates, acceptance, acceptanceException)
}

func NormalizeFailureTaxonomy(d *Diag, value any) []any { return normalizeFailureTaxonomy(d, value) }

func NormalizeHandoff(d *Diag, value any, objective string, deliverables, dependencies any) (*obj, error) {
	return normalizeHandoff(d, value, objective, deliverables, dependencies)
}

// NormalizeModelPolicy is interface.normalize_model_policy(value, team_size=...).
func NormalizeModelPolicy(d *Diag, value any, teamSize any) *obj {
	raw := mapping(value)
	routing := pytext.Lower(text(orDefault(raw.Value("routing"), "fixed"), 40))
	if !in(routing, routingModes) {
		d.err("model_policy.routing must be one of: %s", strings.Join(routingModes, ", "))
		routing = "fixed"
	}
	verifier := text(orDefault(raw.Value("verifier"), "inherit"), 120)
	policy := pyjson.NewObject(
		"routing", routing,
		"planner", text(orDefault(raw.Value("planner"), "inherit"), 120),
		"worker", text(orDefault(raw.Value("worker"), "inherit"), 120),
		"verifier", verifier,
	)
	size := big.NewInt(1)
	switch x := teamSize.(type) {
	case int64:
		size = big.NewInt(x)
	case *big.Int:
		size = x
	}
	if cmp(size, 1) > 0 && pytext.Lower(verifier) == "inherit" {
		d.warn("team tasks should use an independent verifier model or role rather than inheriting the planner's model")
	}
	return policy
}
