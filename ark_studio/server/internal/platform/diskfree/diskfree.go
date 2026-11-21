// Package diskfree reports the space left on the volume holding a folder.
package diskfree

// Available is the number of bytes this process may still write on the
// volume holding path (false when unknown).
func Available(path string) (int64, bool) { return available(path) }
