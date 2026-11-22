package sessions

import (
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func tool(id, name string, input *pyjson.Object, task *pyjson.Object) *pyjson.Object {
	it := pyjson.NewObject("id", id, "type", "tool", "name", name, "status", "completed", "input", input)
	if task != nil {
		it.Set("task", task)
	}
	return it
}

func TestBusyCountsRunningWorkButNotTheWatcher(t *testing.T) {
	items := []*pyjson.Object{
		tool("a", "Task", pyjson.NewObject("description", "Survey"), pyjson.NewObject("id", "t1", "type", "agent", "status", "running")),
		tool("b", "Task", pyjson.NewObject("description", "Done"), pyjson.NewObject("id", "t2", "type", "agent", "status", "completed")),
		tool("c", "Bash", pyjson.NewObject("command", "npm run build", "run_in_background", true),
			pyjson.NewObject("id", "sh1", "type", "command", "status", "running")),
		tool("d", "Bash", pyjson.NewObject("command", "powershell -File ark-watch.ps1", "run_in_background", true),
			pyjson.NewObject("id", "sh2", "type", "command", "status", "running")),
		tool("e", "Bash", pyjson.NewObject("command", "sleep 600", "run_in_background", true),
			pyjson.NewObject("id", "sh3", "type", "command", "status", "running")),
		tool("f", "KillShell", pyjson.NewObject("shell_id", "sh3"), nil),
		pyjson.NewObject("id", "k1", "type", "task", "task", pyjson.NewObject("id", "t9", "type", "command", "status", "failed")),
	}
	b := busyFromItems(items)
	if b.Subagents != 1 || b.Background != 1 {
		t.Fatalf("busy = %+v, want 1 sub-agent and 1 background task", b)
	}
	if busyFromItems(items[3:4]).Any() {
		t.Fatal("the ARK watcher alone is not background work")
	}
	if !IsWatcher("", "Watch ARK updates (ark-watch.sh)") || IsWatcher("go test ./...") {
		t.Fatal("watcher detection")
	}
}
