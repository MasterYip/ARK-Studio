package main

import (
	"arkstudio/go/internal/pyargparse"
	"arkstudio/go/internal/sessionwatch"
)

// sessionWatch is the `session-watch` branch: session_watch.run(args) with
// log=print; Ctrl+C returns 0 (KeyboardInterrupt in __main__).
func sessionWatch(ns pyargparse.Namespace) (int, error) {
	sessionwatch.Stderr = stderr
	o := sessionwatch.Options{
		ID: ns.Str("id"), File: ns.Str("file"), Agent: ns.Str("agent"), Server: ns.Str("server"),
		Project: ns.Str("project"), TokenEnv: ns.Str("token_env"), Title: ns.Str("title"),
		Interval: ns.Float("interval"), Once: ns.Bool("once"),
	}
	return sessionwatch.Run(o, nil, "", func(line string) { pyprint(line) }), nil
}
