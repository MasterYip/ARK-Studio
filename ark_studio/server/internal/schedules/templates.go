package schedules

// Template is a built-in starting point; the person edits name, prompt and
// schedule before saving.
type Template struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	NameEN string `json:"name_en"`
	Prompt string `json:"prompt"`
	Spec   Spec   `json:"schedule"`
	Mode   string `json:"mode"`
}

// Templates are the built-in tasks.
var Templates = []Template{
	{
		ID: "branch-tags", Name: "维护研究线标签", NameEN: "Maintain line tags", Mode: "suggest",
		Spec: Spec{Kind: "daily", At: "07:00", TZOffset: 480},
		Prompt: "维护本项目的研究线标签（branch_status 返回的 prefix，例如 branch:<名称>）。\n" +
			"1. 调用 suggest_line_tags 读取没有研究线标签的节点的建议。\n" +
			"2. 只对 inherit 里的建议动手：用 get_unit 确认节点和它的父节点属于同一条研究线，再用 add_tags 加上建议的标签。拿不准的跳过。\n" +
			"3. new_lines（新研究线）不要直接建：在总结里列出来（根节点、建议的名称、节点数），由人决定。\n" +
			"4. 不删除任何标签。最后总结：加了哪些标签、跳过了哪些、建议的新研究线。",
	},
	{
		ID: "orphans", Name: "整理孤立节点", NameEN: "Tidy orphan nodes", Mode: "suggest",
		Spec: Spec{Kind: "weekly", At: "08:00", Weekday: 1, TZOffset: 480},
		Prompt: "整理没有上级的节点。\n" +
			"1. 调用 orphan_units 列出既没有 part_of 也没有 derived_from 上级的节点（专题除外）。\n" +
			"2. 对每个节点用 get_unit 和 search_units 找最合适的上级（任务、问题或专题），有把握时用 link_units 建 part_of 关联。\n" +
			"3. 找不到合适上级的不要硬连，在总结里列出来。\n" +
			"4. 不删除、不归档任何节点。最后总结：连了哪些、哪些留给人处理。",
	},
	{
		ID: "stalled-lines", Name: "停滞研究线提醒", NameEN: "Stalled lines summary", Mode: "apply",
		Spec: Spec{Kind: "daily", At: "09:00", TZOffset: 480},
		Prompt: "汇总停滞的研究线。\n" +
			"1. 调用 branch_status，找出 status 为 stalled 的研究线（有待处理事项，但 3 天没有活动）。\n" +
			"2. 如果没有停滞的研究线，不要创建任何节点，只回复“没有停滞的研究线”。\n" +
			"3. 如果有，创建一个决策节点（create_unit，type decision，status open），标题“停滞的研究线：<日期>”，正文按研究线列出：研究线名、待处理事项（用 get_unit 读标题）、最近活动时间，并给出选项：继续推进 / 暂停 / 关闭这条研究线。\n" +
			"4. 不修改其他节点。",
	},
}

// TemplateByID returns a template.
func TemplateByID(id string) (Template, bool) {
	for _, t := range Templates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}
