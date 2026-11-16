package pyos

// os.access(name, X_OK) is true for every existing file on Windows.
func executable(string) bool { return true }
