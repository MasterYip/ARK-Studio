//go:build windows

package hostopen

import "golang.org/x/sys/windows"

// shellOpen is os.startfile(target): ShellExecuteW with the "open" verb.
func shellOpen(target string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
