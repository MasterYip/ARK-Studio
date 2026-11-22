package store

// Move plans (Go only; T-0068): a split or merge proposed by an agent (or a
// person), approved by a person, then applied.  A plan is a JSON file in the
// source project, <source>/moves/plans/<id>.json:
//
//	proposed -> approved -> applied -> undone
//	         \-> rejected   \-> (apply failed: stays approved, error kept)
//
// An agent's plan comes with a decision unit in the source project (status
// open, so it waits in Needs you) that holds the summary and a link to the
// approval view; approving or rejecting closes it, which the agent sees in
// its update feed as "decided".  Only a person approves; the routes check
// that (routes_projectmove.go).  The plan keeps the request, so apply plans
// again and refuses when what moves changed since (plan_hash).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/clock"
)

// MovePlan is one plan file.
type MovePlan struct {
	ID         string
	State      string // proposed, approved, rejected, applied, undone
	Mode       string // split, merge, import (a copy from Request.SourceID into this project)
	Request    MoveRequest
	NewName    string
	NewDesc    string
	Hash       string
	Summary    *pyjson.Object
	Title      string
	Note       string
	Lang       string
	Decision   string // the decision unit's uid ("" for a person's own plan)
	CreatedBy  string
	CreatedAt  string
	ApprovedBy string
	ApprovedAt string
	Comment    string // the approver's or rejecter's note
	MoveID     string
	AppliedBy  string
	Error      string
	UpdatedAt  string
	Created    []string // import: the uids of the copies
}

var planIDRe = regexp.MustCompile(`^pl_[0-9a-f]{12}$`)

func planPath(projectPath, id string) string {
	return filepath.Join(projectPath, "moves", "plans", id+".json")
}

func (p *MovePlan) object() *pyjson.Object {
	units := []any{}
	for _, u := range p.Request.Units {
		units = append(units, u)
	}
	exclude := []any{}
	for _, u := range p.Request.Exclude {
		exclude = append(exclude, u)
	}
	return pyjson.NewObject("id", p.ID, "state", p.State, "mode", p.Mode,
		"request", pyjson.NewObject("units", units, "all", p.Request.All, "with_children", p.Request.WithChildren, "cross_links", p.Request.CrossLinks,
			"exclude", exclude, "source", p.Request.SourceID, "source_name", p.Request.SourceName,
			"target", p.Request.TargetID, "target_name", p.Request.TargetName),
		"new_project", pyjson.NewObject("name", p.NewName, "description", p.NewDesc),
		"plan_hash", p.Hash, "summary", p.Summary, "title", p.Title, "note", p.Note, "lang", p.Lang,
		"decision", p.Decision, "created_by", p.CreatedBy, "created_at", p.CreatedAt,
		"approved_by", p.ApprovedBy, "approved_at", p.ApprovedAt, "comment", p.Comment,
		"move_id", p.MoveID, "applied_by", p.AppliedBy, "error", p.Error, "updated_at", p.UpdatedAt,
		"created", stringsAny(p.Created))
}

// Public is the plan as the API returns it.
func (p *MovePlan) Public() *pyjson.Object { return p.object() }

func strList(v any) []string {
	list, _ := v.([]any)
	out := []string{}
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func str(o *pyjson.Object, key string) string {
	s, _ := o.Value(key).(string)
	return s
}

// SaveMovePlan writes the plan file.
func SaveMovePlan(projectPath string, p *MovePlan) error {
	p.UpdatedAt = clock.UTCNow()
	path := planPath(projectPath, p.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, pyjson.DumpsIndent(p.object(), 2), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadMovePlan reads a plan of the project at projectPath.
func LoadMovePlan(projectPath, id string) (*MovePlan, error) {
	if !planIDRe.MatchString(id) {
		return nil, storeErr("unknown move plan: %s", id)
	}
	raw, err := os.ReadFile(planPath(projectPath, id))
	if err != nil {
		return nil, storeErr("unknown move plan: %s", id)
	}
	o, ok := pyjson.LoadsObject(string(raw))
	if !ok {
		return nil, storeErr("unreadable move plan: %s", id)
	}
	r, _ := o.Value("request").(*pyjson.Object)
	if r == nil {
		r = pyjson.NewObject()
	}
	np, _ := o.Value("new_project").(*pyjson.Object)
	if np == nil {
		np = pyjson.NewObject()
	}
	summary, _ := o.Value("summary").(*pyjson.Object)
	p := &MovePlan{ID: str(o, "id"), State: str(o, "state"), Mode: str(o, "mode"),
		Request: MoveRequest{Units: strList(r.Value("units")), All: r.Value("all") == true, CrossLinks: str(r, "cross_links"),
			Exclude: strList(r.Value("exclude")), SourceID: str(r, "source"), SourceName: str(r, "source_name"),
			TargetID: str(r, "target"), TargetName: str(r, "target_name")},
		NewName: str(np, "name"), NewDesc: str(np, "description"), Hash: str(o, "plan_hash"), Summary: summary,
		Title: str(o, "title"), Note: str(o, "note"), Lang: str(o, "lang"), Decision: str(o, "decision"),
		CreatedBy: str(o, "created_by"), CreatedAt: str(o, "created_at"), ApprovedBy: str(o, "approved_by"),
		ApprovedAt: str(o, "approved_at"), Comment: str(o, "comment"), MoveID: str(o, "move_id"),
		AppliedBy: str(o, "applied_by"), Error: str(o, "error"), UpdatedAt: str(o, "updated_at")}
	p.Request.WithChildren = r.Value("with_children") == true || p.Mode == "split"
	p.Request.NewProject = p.Mode == "split"
	p.Created = strList(o.Value("created"))
	return p, nil
}

// ListMovePlans lists the project's plans, newest first.
func ListMovePlans(projectPath string) []*MovePlan {
	entries, _ := os.ReadDir(filepath.Join(projectPath, "moves", "plans"))
	var out []*MovePlan
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".json")
		if p, err := LoadMovePlan(projectPath, id); err == nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func newPlanID() string { return "pl_" + strings.TrimPrefix(newMoveID(), "mv_") }

// ProposeMove is a new plan: the decision unit first (when withDecision;
// a merge leaves it behind), then the dry run, then the unit's text.
func (s *Store) ProposeMove(ctx context.Context, dst *Store, p *MovePlan, withDecision bool) error {
	p.ID, p.State, p.CreatedAt = newPlanID(), "proposed", clock.UTCNow()
	if p.Lang != "zh" {
		p.Lang = "en"
	}
	if p.Request.All {
		p.Mode = "merge"
	} else {
		p.Mode = "split"
		p.Request.WithChildren, p.Request.NewProject = true, true
	}
	if withDecision {
		title := p.Title
		if title == "" {
			title = planText(p.Lang, "title_"+p.Mode, p.NewName, p.Request.TargetName)
		}
		u, err := s.CreateUnit(ctx, pyjson.NewObject("type", "decision", "title", title, "status", "open",
			"importance", "p1", "body", p.Note), p.CreatedBy, Who{})
		if err != nil {
			return err
		}
		p.Decision = pyjson.Str(u.Value("uid"))
		if p.Request.All {
			p.Request.Exclude = append(p.Request.Exclude, p.Decision)
		}
	}
	plan, err := s.PlanMove(ctx, dst, p.Request)
	if err != nil {
		if p.Decision != "" {
			s.ArchiveUnit(ctx, p.Decision, p.CreatedBy) //nolint:errcheck // the plan failed: its question goes too
		}
		return err
	}
	p.Hash, p.Summary = pyjson.Str(plan.Value("plan_hash")), plan
	p.Request.CrossLinks = str(plan, "cross_link_mode")
	if p.Decision != "" {
		if _, err := s.UpdateUnit(ctx, p.Decision, pyjson.NewObject("body", p.decisionBody()), p.CreatedBy, Who{}); err != nil {
			return err
		}
	}
	return SaveMovePlan(s.Path, p)
}

// decisionBody is the text of the plan's decision unit.
func (p *MovePlan) decisionBody() string {
	sm := p.Summary
	n := func(key string) any { return sm.Value(key) }
	var b strings.Builder
	if p.Note != "" {
		b.WriteString(p.Note + "\n\n")
	}
	if p.Mode == "import" {
		return p.importDecisionBody()
	}
	b.WriteString("## " + planText(p.Lang, "plan") + "\n\n")
	if p.Mode == "split" {
		b.WriteString("- " + planText(p.Lang, "split_to", p.NewName) + "\n")
	} else {
		b.WriteString("- " + planText(p.Lang, "merge_to", p.Request.TargetName) + "\n")
	}
	roots := []string{}
	if p.Mode == "split" {
		// A cluster can have dozens of branch roots: name the first few.
		for i, u := range p.Request.Units {
			if i == 6 {
				roots = append(roots, "…")
				break
			}
			roots = append(roots, "[["+u+"]]")
		}
	}
	b.WriteString("- " + planText(p.Lang, "units", n("unit_count"), strings.Join(roots, " ")) + "\n")
	b.WriteString("- " + planText(p.Lang, "renumbered", n("renumbered")) + "\n")
	b.WriteString("- " + planText(p.Lang, "links", n("links"), n("media"), n("bytes"), n("comments")) + "\n")
	cross, _ := n("cross_links").([]any)
	mode := planText(p.Lang, "cross_"+p.Request.CrossLinks)
	b.WriteString("- " + planText(p.Lang, "cross", int64(len(cross)), mode) + "\n")
	for i, c := range cross {
		if i == 20 {
			b.WriteString("  - …\n")
			break
		}
		o := c.(*pyjson.Object)
		if o.Value("direction") == "out" {
			fmt.Fprintf(&b, "  - [[%s]] %s [[%s]] %s\n", o.Value("uid"), o.Value("relation"), o.Value("other_uid"), o.Value("other_title"))
		} else {
			fmt.Fprintf(&b, "  - [[%s]] %s %s [[%s]]\n", o.Value("other_uid"), o.Value("other_title"), o.Value("relation"), o.Value("uid"))
		}
	}
	b.WriteString("\n[" + planText(p.Lang, "open") + "](?move_plan=" + p.ID + ")\n\n" + planText(p.Lang, "how") + "\n")
	return b.String()
}

var planTexts = map[string][2]string{
	"title_split":  {"Approve: split %[1]s off into a new project", "批准：拆分出新项目「%[1]s」"},
	"title_import": {"Approve: copy %[3]v nodes from %[2]s into this project", "批准：从「%[2]s」复制 %[3]v 个节点到本项目"},
	"import_from":  {"Copy from %s into this project; the source stays as it is", "从 %s 复制到本项目；来源项目不变"},
	"import_units": {"%v nodes are copied (%v already here are skipped)", "复制 %v 个节点（%v 个之前已导入，跳过）"},
	"import_ids":   {"IDs: %v change (one clash here renumbers the whole batch)", "编号：%v 个变化（本项目有重复时整批重编号）"},
	"import_more":  {"%v links between them and %v attachments (%v bytes) come along; each copy ends with a line naming its source", "它们之间的 %v 条链接和 %v 个附件（%v 字节）一起复制；每个副本末尾注明来源"},
	"title_merge":  {"Approve: merge this project into %[2]s", "批准：把本项目合并到「%[2]s」"},
	"plan":         {"Move plan", "移动计划"},
	"split_to":     {"Split into a new project: %s", "拆分到新项目：%s"},
	"merge_to":     {"Merge every node of this project into %s; this project then becomes read-only", "把本项目全部节点合并到 %s；之后本项目变为只读"},
	"units":        {"%v nodes move %s", "移动 %v 个节点 %s"},
	"renumbered":   {"IDs: %v change (one clash in the target renumbers the whole batch); the old IDs still lead to the nodes", "编号：%v 个变化（目标里有重复时整批重编号）；旧编号仍能找到这些节点"},
	"links":        {"%v links between them, %v attachments (%v bytes), %v comments move along", "它们之间的 %v 条链接、%v 个附件（%v 字节）、%v 条评论一起移动"},
	"cross":        {"%v links to nodes that stay: %s", "与留下节点的链接 %v 条：%s"},
	"cross_keep":   {"kept as references to the other project", "保留为指向原项目的引用"},
	"cross_drop":   {"removed (an undo restores them)", "删除（撤销时恢复）"},
	"open":         {"Review and approve the plan", "查看并批准计划"},
	"how_import":   {"Nothing is copied until a person with write access here and read access to the source approves.", "在本项目有写权限、来源项目有读权限的人批准之前，不会复制任何内容。"},
	"how":          {"Nothing moves until a person with admin access to both projects approves.", "在两个项目都有管理权限的人批准之前，不会移动任何内容。"},
	"approved":     {"Approved by %s.", "%s 已批准。"},
	"rejected":     {"Rejected by %s.", "%s 已拒绝。"},
}

func planText(lang, key string, args ...any) string {
	t := planTexts[key]
	f := t[0]
	if lang == "zh" {
		f = t[1]
	}
	if len(args) == 0 {
		return f
	}
	return fmt.Sprintf(f, args...)
}

// DecidePlan records a person's approval or rejection on the plan and its
// decision unit.
func (s *Store) DecidePlan(ctx context.Context, p *MovePlan, approve bool, who, comment string) error {
	if p.State != "proposed" {
		return storeErr("move plan %s is %s", p.ID, p.State)
	}
	p.ApprovedBy, p.ApprovedAt, p.Comment = who, clock.UTCNow(), comment
	p.State = "rejected"
	key := "rejected"
	if approve {
		p.State, key = "approved", "approved"
	}
	if p.Decision != "" {
		line := planText(p.Lang, key, strings.TrimPrefix(who, "human:"))
		if comment != "" {
			line += " " + comment
		}
		err := s.updateDecision(ctx, p.Decision, line, who)
		if err != nil {
			return err
		}
	}
	return SaveMovePlan(s.Path, p)
}

func (s *Store) updateDecision(ctx context.Context, uid, line, who string) error {
	u, err := s.GetUnit(ctx, uid)
	if err != nil {
		return nil // the decision unit is gone: the plan file is the record
	}
	body := strings.TrimRight(pyjson.Str(u.Value("body")), "\n") + "\n\n**" + line + "**\n"
	_, err = s.UpdateUnit(ctx, uid, pyjson.NewObject("body", body, "status", "done"), who, Who{})
	return err
}

func (p *MovePlan) importDecisionBody() string {
	sm := p.Summary
	n := func(key string) any { return sm.Value(key) }
	var b strings.Builder
	if p.Note != "" {
		b.WriteString(p.Note + "\n\n")
	}
	b.WriteString("## " + planText(p.Lang, "plan") + "\n\n")
	b.WriteString("- " + planText(p.Lang, "import_from", p.Request.SourceName) + "\n")
	b.WriteString("- " + planText(p.Lang, "import_units", n("unit_count"), n("skipped")) + "\n")
	b.WriteString("- " + planText(p.Lang, "import_ids", n("renumbered")) + "\n")
	b.WriteString("- " + planText(p.Lang, "import_more", n("links"), n("media"), n("bytes")) + "\n")
	units, _ := n("units").([]any)
	for i, u := range units {
		if i == 20 {
			b.WriteString("  - …\n")
			break
		}
		o := u.(*pyjson.Object)
		fmt.Fprintf(&b, "  - [[%s/%s]] %s\n", p.Request.SourceID, o.Value("uid"), o.Value("title"))
	}
	b.WriteString("\n[" + planText(p.Lang, "open") + "](?move_plan=" + p.ID + ")\n\n" + planText(p.Lang, "how_import") + "\n")
	return b.String()
}

// importDigest is an import plan's plan_hash: what would be copied and how
// (source uids, new uids, skips, links, attachments).
func importDigest(sm *pyjson.Object) string {
	h := sha256.New()
	units, _ := sm.Value("units").([]any)
	for _, u := range units {
		o := u.(*pyjson.Object)
		fmt.Fprintf(h, "%v=%v;%v\n", o.Value("uid"), o.Value("new_uid"), o.Value("skipped"))
	}
	fmt.Fprintf(h, "%v|%v|%v|%v", sm.Value("links"), sm.Value("media"), sm.Value("bytes"), sm.Value("source"))
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// ProposeImport is a new import plan on s (the target): the decision unit
// (when withDecision), then the preview of copying from src.
func (s *Store) ProposeImport(ctx context.Context, src *Store, p *MovePlan, withDecision bool) error {
	p.ID, p.State, p.CreatedAt, p.Mode = newPlanID(), "proposed", clock.UTCNow(), "import"
	if p.Lang != "zh" {
		p.Lang = "en"
	}
	req := p.importRequest()
	sm, err := s.ImportFrom(ctx, src, req, true, p.CreatedBy)
	if err != nil {
		return err
	}
	p.Summary, p.Hash = sm, importDigest(sm)
	if withDecision {
		title := p.Title
		if title == "" {
			title = planText(p.Lang, "title_import", "", p.Request.SourceName, sm.Value("unit_count"))
		}
		u, err := s.CreateUnit(ctx, pyjson.NewObject("type", "decision", "title", title, "status", "open",
			"importance", "p1", "body", p.decisionBody()), p.CreatedBy, Who{})
		if err != nil {
			return err
		}
		p.Decision = pyjson.Str(u.Value("uid"))
	}
	return SaveMovePlan(s.Path, p)
}

func (p *MovePlan) importRequest() ImportRequest {
	return ImportRequest{SourceID: p.Request.SourceID, SourceName: p.Request.SourceName, Units: p.Request.Units,
		WithChildren: p.Request.WithChildren, Lang: p.Lang}
}

// ApplyImport carries out an approved import plan on s (the target).
func (s *Store) ApplyImport(ctx context.Context, src *Store, p *MovePlan, actor string) (*pyjson.Object, error) {
	req := p.importRequest()
	check, err := s.ImportFrom(ctx, src, req, true, actor)
	if err != nil {
		return nil, err
	}
	if importDigest(check) != p.Hash {
		return nil, &MoveStaleError{}
	}
	out, err := s.ImportFrom(ctx, src, req, false, actor)
	if err != nil {
		return nil, err
	}
	p.Created = nil
	units, _ := out.Value("units").([]any)
	for _, u := range units {
		o := u.(*pyjson.Object)
		if o.Value("skipped") != true {
			p.Created = append(p.Created, pyjson.Str(o.Value("new_uid")))
		}
	}
	out.Delete("created")
	return out, nil
}

// UndoImport moves an applied import's copies to the recycle bin.  Copies
// edited since the import refuse unless force.
func (s *Store) UndoImport(ctx context.Context, p *MovePlan, force bool, actor string) (*pyjson.Object, error) {
	if !force {
		var edited []string
		for _, uid := range p.Created {
			u, err := s.GetUnit(ctx, uid)
			if err != nil {
				continue
			}
			meta, _ := u.Value("meta").(*pyjson.Object)
			from, _ := meta.Value("imported_from").(*pyjson.Object)
			if from != nil && pyjson.Str(u.Value("updated_at")) > pyjson.Str(from.Value("at")) {
				edited = append(edited, uid)
			}
		}
		if len(edited) > 0 {
			return nil, storeErr("copies changed since the import: %s", strings.Join(edited, ", "))
		}
	}
	n := int64(0)
	for _, uid := range p.Created {
		if _, err := s.ArchiveUnit(ctx, uid, actor); err == nil {
			n++
		}
	}
	return pyjson.NewObject("units", n), nil
}
