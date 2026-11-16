package procx

import (
	"os/exec"
	"testing"
)

func TestSetArgvCmdline(t *testing.T) {
	cmd := exec.Command("x")
	SetArgvCmdline(cmd, []string{"a b", `c"d`})
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CmdLine != `"a b" c\"d` {
		t.Fatalf("CmdLine = %+v", cmd.SysProcAttr)
	}
}
