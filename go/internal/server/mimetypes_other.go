//go:build !windows

package server

// readRegistryMimes: only Windows has a registry; other hosts use the
// mime.types files.
func readRegistryMimes(func(ctype, ext string)) {}
