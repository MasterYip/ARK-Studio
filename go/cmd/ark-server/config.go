package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"arkstudio/go/internal/auth"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"

	_ "modernc.org/sqlite"
)

// Port of arkstudio/server_config.py: load() and persisted_auth().

var pathFields = map[string]bool{"root": true, "sessions_root": true, "auth_password_file": true}
var envPaths = map[string]string{"org_db": "ARK_ORG_DB", "private_dir": "ARK_PRIVATE_DIR",
	"gpu_servers_config": "ARK_GPU_SERVERS_CONFIG", "gpu_queue_db": "ARK_GPU_QUEUE_DB", "blackbox_dir": "ARK_BLACKBOX_DIR"}
var stringFields = map[string]bool{"root": true, "sessions_root": true, "auth_password_file": true, "host": true,
	"public_url": true, "auth_user": true, "auth_password_hash": true, "public_ip": true}
var boolFields = map[string]bool{"no_demo": true, "expose": true, "detect_public_ip": true, "trust_proxy": true, "require_auth": true}
var intFields = map[string]bool{"port": true, "auth_session_ttl": true}

const cannotRead = "Cannot read server JSON configuration"

// loadConfig is server_config.load(path) -> (cli defaults, env exports).
func loadConfig(path string) (map[string]any, map[string]string, error) {
	source, err := filepath.Abs(expandUser(path))
	if err != nil {
		return nil, nil, &auth.ConfigError{Message: cannotRead}
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		return nil, nil, &auth.ConfigError{Message: cannotRead}
	}
	text := strings.TrimPrefix(string(raw), string(rune(0xfeff))) // utf-8-sig
	v, err := pyjson.Loads(strings.ReplaceAll(text, "\r\n", "\n"))
	if err != nil {
		return nil, nil, &auth.ConfigError{Message: cannotRead}
	}
	values, ok := v.(*pyjson.Object)
	if !ok {
		return nil, nil, &auth.ConfigError{Message: "Server configuration must be a JSON object"}
	}
	var unknown []string
	for _, k := range values.Keys() {
		if !stringFields[k] && !boolFields[k] && !intFields[k] && envPaths[k] == "" && k != "media_root" {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, nil, &auth.ConfigError{Message: "Unknown server setting: " + unknown[0]}
	}
	cli := map[string]any{"require_auth": true}
	env := map[string]string{}
	resolved := func(value string) string {
		candidate := expandUser(value)
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(filepath.Dir(source), candidate)
		}
		abs, _ := filepath.Abs(candidate)
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			return real
		}
		return abs
	}
	for _, key := range values.Keys() {
		value := values.Value(key)
		switch {
		case stringFields[key] || envPaths[key] != "":
			s, ok := value.(string)
			if !ok || strings.Contains(s, "\x00") {
				return nil, nil, &auth.ConfigError{Message: "Server setting must be a string: " + key}
			}
			if pathFields[key] || envPaths[key] != "" {
				if pytext.Strip(s) == "" {
					return nil, nil, &auth.ConfigError{Message: "Server path must not be empty: " + key}
				}
				s = resolved(s)
			}
			value = s
		case boolFields[key]:
			if _, ok := value.(bool); !ok {
				return nil, nil, &auth.ConfigError{Message: "Server setting must be a boolean: " + key}
			}
		case intFields[key]:
			n, ok := value.(int64)
			if !ok || n < 0 {
				return nil, nil, &auth.ConfigError{Message: "Server setting must be a nonnegative integer: " + key}
			}
			if key == "port" && n > 65535 {
				return nil, nil, &auth.ConfigError{Message: "Server port must be at most 65535"}
			}
		case key == "media_root":
			list, ok := value.([]any)
			if !ok {
				return nil, nil, &auth.ConfigError{Message: "media_root must be a list of paths"}
			}
			var out []string
			for _, item := range list {
				s, ok := item.(string)
				if !ok || pytext.Strip(s) == "" {
					return nil, nil, &auth.ConfigError{Message: "media_root must be a list of paths"}
				}
				out = append(out, resolved(s))
			}
			value = out
		}
		if name := envPaths[key]; name != "" {
			env[name] = value.(string)
		} else {
			cli[key] = value
		}
	}
	return cli, env, nil
}

func expandUser(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimLeft(p[1:], `/\`))
		}
	}
	return p
}

// persistedAuth is server_config.persisted_auth: an organization with
// passwords must never silently start as an anonymous local server.
func persistedAuth(database, username string) (*auth.Manager, bool, error) {
	if _, err := os.Stat(database); err != nil {
		return nil, false, nil
	}
	fail := &auth.ConfigError{Message: "Cannot read existing organization authentication; refusing anonymous startup"}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(database)+"?mode=ro&_pragma=busy_timeout(30000)")
	if err != nil {
		return nil, false, fail
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='users'").Scan(&n); err != nil {
		return nil, false, fail
	}
	if n == 0 {
		return nil, false, nil
	}
	var one int
	err = db.QueryRow("SELECT 1 FROM users WHERE password_hash IS NOT NULL AND password_hash<>'' LIMIT 1").Scan(&one)
	if err == sql.ErrNoRows {
		return nil, false, nil
	} else if err != nil {
		return nil, false, fail
	}
	rows, err := db.Query("SELECT u.username,u.password_hash FROM users u JOIN memberships m ON m.user_id=u.id " +
		"WHERE m.role='owner' AND u.disabled=0 AND u.password_hash IS NOT NULL " +
		"AND u.password_hash<>'' ORDER BY u.created_at,u.username")
	if err != nil {
		return nil, false, fail
	}
	defer rows.Close()
	for rows.Next() {
		var name, hash string
		if err := rows.Scan(&name, &hash); err != nil {
			return nil, false, fail
		}
		if username == "" || strings.EqualFold(name, username) {
			m, err := auth.NewManager(name, hash, auth.SessionTTLDefault)
			return m, true, err
		}
	}
	return nil, false, &auth.ConfigError{Message: "Organization has accounts but no matching active owner; configure authentication explicitly"}
}

// buildAuth is __main__._build_auth.
func buildAuth(o *options) (*auth.Manager, error) {
	useCLI := o.authPassword != "" || o.authPasswordFile != "" || o.authPasswordHash != ""
	password, passwordFile, passwordHash := o.authPassword, o.authPasswordFile, o.authPasswordHash
	if !useCLI {
		password, passwordFile, passwordHash = os.Getenv("ARK_AUTH_PASSWORD"), os.Getenv("ARK_AUTH_PASSWORD_FILE"), os.Getenv("ARK_AUTH_PASSWORD_HASH")
	}
	username := pytext.Strip(o.authUser)
	if username == "" {
		username = pytext.Strip(os.Getenv("ARK_AUTH_USER"))
	}
	ttl := o.authSessionTTL
	if ttl == 0 {
		ttl = envInt("ARK_AUTH_SESSION_TTL", auth.SessionTTLDefault)
	}
	if password == "" && passwordFile == "" && passwordHash == "" {
		saved, ok, err := persistedAuth(org.DefaultPath(o.root), username)
		if err != nil {
			return nil, err
		}
		if ok {
			saved.SessionTTL = max(auth.SessionTTLMin, min(auth.SessionTTLMax, ttl))
			return saved, nil
		}
		if _, err := os.Stat(filepath.Join(expandUser(o.root), ".auth-secret")); err == nil {
			return nil, &auth.ConfigError{Message: "Existing sign-in state has no organization accounts; restore the organization database before starting"}
		}
	}
	direct, hashed, file := password, pytext.Strip(passwordHash), pytext.Strip(passwordFile)
	sources := 0
	for _, s := range []string{direct, hashed, file} {
		if s != "" {
			sources++
		}
	}
	if username == "" && sources == 0 {
		return nil, nil
	}
	if username == "" {
		return nil, &auth.ConfigError{Message: "auth username is required (ARK_AUTH_USER or --auth-user)"}
	}
	if sources > 1 {
		return nil, &auth.ConfigError{Message: "configure only one of auth password, password file or password hash"}
	}
	if hashed == "" {
		if file != "" {
			var err error
			if direct, err = auth.ReadPasswordFile(file); err != nil {
				return nil, err
			}
		}
		if direct == "" {
			return nil, &auth.ConfigError{Message: "auth password is required (ARK_AUTH_PASSWORD, ARK_AUTH_PASSWORD_FILE " +
				"or ARK_AUTH_PASSWORD_HASH; --auth-password is supported but visible to other processes)"}
		}
		var err error
		if hashed, err = auth.HashPassword(direct, auth.Iterations); err != nil {
			return nil, err
		}
	}
	return auth.NewManager(username, hashed, ttl)
}

func envInt(name string, def int) int {
	n, err := pytext.Int(os.Getenv(name))
	if err != nil {
		return def
	}
	return int(n)
}

func envBool(name string) bool {
	v := strings.ToLower(pytext.Strip(os.Getenv(name)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

var _ = fmt.Sprint
