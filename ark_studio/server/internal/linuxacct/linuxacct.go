// Package linuxacct ports arkstudio/linux_account.py: optional POSIX account
// selection for task-agent processes (validation, the sudo wrapper and the
// preflight check).
package linuxacct

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// SudoHint is linux_account.SUDO_HINT.
const SudoHint = "Configure passwordless sudo for the target account, e.g. " +
	"`ark ALL=(alice) NOPASSWD: SETENV: /usr/bin/env` in /etc/sudoers.d/ark, " +
	"and let that account write the session workspace " +
	"(`setfacl -R -m u:alice:rwX <workspace>` or a shared group)."

var (
	accountRe      = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	systemAccounts = map[string]bool{}
)

func init() {
	for _, a := range strings.Fields("root daemon bin sys sync games man lp mail news uucp proxy www-data backup list irc gnats " +
		"nobody systemd-network systemd-resolve systemd-timesync messagebus sshd polkitd rtkit colord gdm saned") {
		systemAccounts[a] = true
	}
}

// Error is LinuxAccountError.
type Error struct {
	Message string
	Status  int
	Code    string
}

func (e *Error) Error() string { return e.Message }

func lerr(msg string, status int, code string) *Error { return &Error{msg, status, code} }

// Platform is sys.platform.
func Platform() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	}
	return runtime.GOOS
}

func isLinux() bool { return runtime.GOOS == "linux" }

func uidMin() int64 {
	raw := os.Getenv("ARK_LINUX_UID_MIN")
	if raw == "" {
		return 1000
	}
	n, err := pytext.Int(raw)
	if err != nil {
		return 1000
	}
	return max(0, n)
}

// SudoBinary is linux_account.sudo_binary.
func SudoBinary() string {
	if o := pytext.Strip(os.Getenv("ARK_SUDO_BINARY")); o != "" {
		return o
	}
	p, err := exec.LookPath("sudo")
	if err != nil {
		return ""
	}
	return p
}

// Info is the validated account.
type Info struct {
	Account, Home, Sudo string
	UID                 int64
}

// Validate is validate_linux_account.
func Validate(account string) (*Info, error) {
	text := pytext.Strip(account)
	if text == "" {
		return nil, lerr("Linux account is required.", 400, "LINUX_ACCOUNT_REQUIRED")
	}
	if len(text) > 32 || !accountRe.MatchString(text) {
		return nil, lerr("Linux account must be a plain lowercase account name "+
			"(letters, digits, _ or -, up to 32 characters).", 400, "LINUX_ACCOUNT_INVALID")
	}
	if systemAccounts[text] {
		return nil, lerr(fmt.Sprintf("'%s' is a system account and cannot be used for agent isolation.", text), 400, "LINUX_ACCOUNT_SYSTEM")
	}
	if !isLinux() {
		return nil, lerr("Linux account selection is only available on a Linux host.", 400, "LINUX_ACCOUNT_UNAVAILABLE")
	}
	u, err := user.Lookup(text)
	if err != nil {
		return nil, lerr(fmt.Sprintf("Linux account '%s' does not exist on this host.", text), 400, "LINUX_ACCOUNT_NOT_FOUND")
	}
	uid, err := strconv.ParseInt(u.Uid, 10, 64)
	if err != nil {
		return nil, lerr(fmt.Sprintf("Linux account '%s' has no usable uid.", text), 400, "LINUX_ACCOUNT_INVALID")
	}
	if uid == 0 {
		return nil, lerr("root cannot be used for agent isolation.", 400, "LINUX_ACCOUNT_ROOT")
	}
	if limit := uidMin(); uid < limit {
		return nil, lerr(fmt.Sprintf("'%s' is a system account (uid %d < %d); choose a regular login account.", text, uid, limit),
			400, "LINUX_ACCOUNT_SYSTEM")
	}
	current := pytext.Strip(os.Getenv("USER"))
	if current == "" {
		current = pytext.Strip(os.Getenv("LOGNAME"))
	}
	if current != "" && text == current {
		return nil, lerr(fmt.Sprintf("'%s' is the account running ARK; choose a different account "+
			"so provider credentials are isolated.", text), 400, "LINUX_ACCOUNT_SERVER_ACCOUNT")
	}
	home := pytext.Strip(u.HomeDir)
	if home == "" || !strings.HasPrefix(home, "/") {
		return nil, lerr(fmt.Sprintf("Linux account '%s' has no absolute home directory.", text), 400, "LINUX_ACCOUNT_INVALID")
	}
	home = strings.TrimRight(home, "/")
	if home == "" {
		home = "/"
	}
	return &Info{Account: text, UID: uid, Home: home}, nil
}

// Wrap is wrap_command.
func Wrap(argv []string, account, home string) ([]string, error) {
	target := pytext.Strip(account)
	if target == "" {
		return nil, lerr("Linux account is required.", 400, "LINUX_ACCOUNT_REQUIRED")
	}
	if len(argv) == 0 {
		return nil, lerr("agent command is empty.", 400, "LINUX_COMMAND_EMPTY")
	}
	if home == "" {
		home = "/home/" + target
	}
	home = strings.TrimRight(home, "/")
	if home == "" {
		home = "/"
	}
	sudo := SudoBinary()
	if sudo == "" {
		sudo = "sudo"
	}
	out := []string{sudo, "-n", "-E", "-u", target, "--", "/usr/bin/env", "HOME=" + home, "USER=" + target,
		"LOGNAME=" + target, "XDG_CONFIG_HOME=" + home + "/.config"}
	return append(out, argv...), nil
}

// Preflight is linux_account.preflight.
func Preflight(account, workspace string) (*Info, error) {
	info, err := Validate(account)
	if err != nil {
		return nil, err
	}
	sudo := SudoBinary()
	if sudo == "" {
		return nil, lerr("sudo is not installed or not on PATH on this host. "+SudoHint, 409, "LINUX_SUDO_UNAVAILABLE")
	}
	run := func(argv []string) (int, string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		err := cmd.Run()
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), stderr.String(), nil
		}
		return 0, stderr.String(), err
	}
	base, _ := Wrap([]string{"/bin/true"}, info.Account, info.Home)
	code, stderr, err := run(base)
	if err != nil {
		return nil, lerr(fmt.Sprintf("could not execute sudo for '%s': %v. ", info.Account, err)+SudoHint, 409, "LINUX_SUDO_FAILED")
	}
	if code != 0 {
		detail := pytext.Prefix(pytext.Strip(stderr), 400)
		msg := fmt.Sprintf("passwordless sudo as '%s' failed", info.Account)
		if detail != "" {
			msg += ": " + detail
		} else {
			msg += "."
		}
		return nil, lerr(msg+" "+SudoHint, 409, "LINUX_SUDO_DENIED")
	}
	if workspace != "" {
		argv, _ := Wrap([]string{"/bin/test", "-w", workspace}, info.Account, info.Home)
		code, _, err := run(argv)
		if err != nil {
			return nil, lerr(fmt.Sprintf("could not verify workspace permissions: %v. ", err)+SudoHint, 409, "LINUX_WORKSPACE_CHECK_FAILED")
		}
		if code != 0 {
			return nil, lerr(fmt.Sprintf("Linux account '%s' cannot write the session workspace (%s). ", info.Account, workspace)+SudoHint,
				409, "LINUX_WORKSPACE_NOT_WRITABLE")
		}
	}
	info.Sudo = sudo
	return info, nil
}

// Available is linux_account.available().
func Available() *pyjson.Object {
	sudo := SudoBinary()
	return pyjson.NewObject("platform", Platform(), "available", isLinux() && sudo != "", "linux", isLinux(),
		"sudo", sudo, "uid_min", uidMin(), "hint", SudoHint)
}
