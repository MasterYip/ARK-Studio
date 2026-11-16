//go:build windows

package server

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// readRegistryMimes is _mimetypes_read_windows_registry: every
// HKEY_CLASSES_ROOT\.<ext> key with a REG_SZ "Content Type" value.
func readRegistryMimes(add func(ctype, ext string)) {
	root, err := registry.OpenKey(registry.CLASSES_ROOT, "", registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return
	}
	defer root.Close()
	names, err := root.ReadSubKeyNames(-1)
	if err != nil {
		return
	}
	for _, name := range names {
		if !strings.HasPrefix(name, ".") || strings.ContainsRune(name, 0) {
			continue
		}
		key, err := registry.OpenKey(registry.CLASSES_ROOT, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		value, kind, err := key.GetStringValue("Content Type")
		key.Close()
		if err != nil || kind != registry.SZ {
			continue
		}
		add(value, name)
	}
}
