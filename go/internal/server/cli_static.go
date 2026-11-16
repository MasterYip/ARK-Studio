package server

// StaticPayload returns one of the Python-generated static JSON payloads
// (static_gen.go), for the CLI's `skills list|show|interface`.
func StaticPayload(key string) (string, bool) {
	s, ok := staticJSON[key]
	return s, ok
}
