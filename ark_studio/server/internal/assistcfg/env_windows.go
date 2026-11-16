package assistcfg

import "golang.org/x/sys/windows/registry"

// registryEnv is the winreg fallback of _env_value: the User (then
// HKLM\Environment) environment key, for a server started from a shell that
// did not inherit it.
func registryEnv(name string) string {
	for _, hive := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		key, err := registry.OpenKey(hive, "Environment", registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		value, _, err := key.GetStringValue(name)
		key.Close()
		if err == nil {
			if v := strip(value); v != "" {
				return v
			}
		}
	}
	return ""
}
