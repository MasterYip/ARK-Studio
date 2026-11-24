package server

// Standby mode (Go only, docs/deploy/standby.md): ARK_READ_ONLY=1 serves a
// replica that the primary refreshes every few minutes.  Every API write is
// refused with 503 STANDBY_READ_ONLY (signing in and out still work), the
// background workers that act on the outside world stay off (main.go), and
// the web app shows a banner.  ARK_STANDBY_STATUS names the JSON file the
// replication writes; /api/health reports its sync time.

import (
	"html"
	"net/http"
	"os"
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
)

// ReadOnlyCode is the error code of a refused standby write.
const ReadOnlyCode = "STANDBY_READ_ONLY"

const readOnlyMessage = "standby (read-only): this server is a copy of the primary; changes are turned off"

// readOnlyAllowed are the writes a standby still accepts: signing in and out.
var readOnlyAllowed = set("/login", "/logout", "/api/auth/logout")

// refuseStandbyWrite answers a write on a read-only server and reports
// whether it did.
func (c *reqCtx) refuseStandbyWrite() bool {
	if !c.app.Cfg.ReadOnly {
		return false
	}
	switch c.r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	if readOnlyAllowed[c.rawPath] {
		return false
	}
	c.w.Header().Set("Retry-After", "300")
	c.fail(503, readOnlyMessage, ReadOnlyCode)
	return true
}

// standbyStatus is the public part of the replication status file: when the
// last copy arrived and from where.  Nil when the file is missing or invalid.
func standbyStatus(path string) *pyjson.Object {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 1<<20 {
		return nil
	}
	v, err := pyjson.Loads(string(raw))
	obj, ok := v.(*pyjson.Object)
	if err != nil || !ok {
		return nil
	}
	out := pyjson.NewObject()
	for _, key := range []string{"last_sync", "primary", "databases"} {
		switch value := obj.Value(key).(type) {
		case string:
			out.Set(key, value)
		case int64:
			out.Set(key, value)
		}
	}
	return out
}

// statusStamp changes whenever the replication rewrites its status file, so
// the cached index page (and its banner) follows the last sync.
func statusStamp(path string) string {
	if path == "" {
		return ""
	}
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fi.ModTime().UTC().Format(time.RFC3339Nano)
}

// standbyMeta marks the page of a standby server for the web app
// (web/js/standby.js draws the banner): <meta name="ark-standby"> with the
// time of the last sync.
func standbyMeta(statusPath string) string {
	last := ""
	if st := standbyStatus(statusPath); st != nil {
		if at, ok := st.Value("last_sync").(string); ok {
			if t, err := time.Parse(time.RFC3339, at); err == nil {
				last = t.UTC().Format(time.RFC3339)
			}
		}
	}
	return `<meta name="ark-standby" content="read-only" data-last-sync="` + html.EscapeString(last) + `">`
}

// withStandbyMeta inserts the standby marker before </head>.
func withStandbyMeta(text, statusPath string) string {
	at := strings.Index(text, "</head>")
	if at < 0 {
		return text
	}
	return text[:at] + standbyMeta(statusPath) + "\n" + text[at:]
}
