package main

// `ark-server agent watch` (Go only, T-0044): follow GET
// /api/agent/v1/updates and print what people did that the agent should
// act on.  Like `routes` and `docs-check` it is dispatched before the
// argparse snapshot of the Python CLI (cli_spec.json), so `ark-server agent
// --help` still lists only `context`.  Docs: docs/agents/working-mode.md
// ("Watch for replies").

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	watchStateFile = ".ark-watch-cursor"
	watchExitAuth  = 3 // the token was refused (401/403)
	watchExitError = 1
)

type watchOptions struct {
	URL, Token, TokenFile, Project, Name, Kinds, State string
	Wait, Interval                                     float64
	Once, JSON, Mine                                   bool
	Until                                              bool    // exit after the first news (T-0053: a background watcher wakes the agent)
	Max                                                float64 // with --until-update: seconds to watch before giving up
	Host                                               string  // X-ARK-Host
}

type watchState struct {
	URL     string `json:"url"`
	Project string `json:"project"`
	Cursor  string `json:"cursor"`
	Rules   string `json:"rules_version,omitempty"`
	Saved   string `json:"saved_at"`
}

type watchReply struct {
	OK     bool              `json:"ok"`
	Cursor string            `json:"cursor"`
	Items  []json.RawMessage `json:"items"`
	Reset  bool              `json:"reset"`
	Reason string            `json:"reason"`
	More   bool              `json:"more"`
	Rules  string            `json:"rules_version"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type watchItem struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	UID     string `json:"uid"`
	At      string `json:"at"`
	Actor   string `json:"actor"`
	Summary string `json:"summary"`
	Link    string `json:"link"`
	Comment *struct {
		ID int64 `json:"id"`
	} `json:"comment"`
	Message *struct {
		ID    int64  `json:"id"`
		Body  string `json:"body"`
		Reply string `json:"reply"`
	} `json:"message"`
}

// watchError is a failed request: auth errors end the watch.
type watchError struct {
	status int
	msg    string
}

func (e *watchError) Error() string { return e.msg }

func (e *watchError) auth() bool { return e.status == 401 || e.status == 403 }

func agentWatchFlags(errw io.Writer) (*flag.FlagSet, *watchOptions, *string) {
	o := &watchOptions{}
	fs := flag.NewFlagSet("ark-server agent watch", flag.ContinueOnError)
	fs.SetOutput(errw)
	envOr := func(name, def string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return def
	}
	fs.StringVar(&o.URL, "url", envOr("ARK_URL", "http://127.0.0.1:8890"), "ARK server URL (env ARK_URL)")
	fs.StringVar(&o.Token, "token", "", "agent token; avoid it: other processes can read command lines (default: --token-file, then --token-env)")
	tokenEnv := fs.String("token-env", "ARK_TOKEN", "environment variable holding the agent token")
	fs.StringVar(&o.TokenFile, "token-file", os.Getenv("ARK_TOKEN_FILE"), "file holding the agent token (env ARK_TOKEN_FILE; default ~/.ark/tokens/<project>)")
	fs.StringVar(&o.Project, "project", os.Getenv("ARK_PROJECT"), "project id, sent as X-ARK-Project (env ARK_PROJECT)")
	fs.StringVar(&o.Name, "name", os.Getenv("ARK_AGENT_NAME"), "agent name, sent as X-Agent-Name (for @mentions)")
	fs.Float64Var(&o.Wait, "wait", -1, "long-poll seconds per request, max 60 (default 50; 0 with --once)")
	fs.Float64Var(&o.Interval, "interval", 30, "seconds between requests when --wait is 0, and after an error")
	fs.BoolVar(&o.Once, "once", false, "check once, print new updates and exit")
	fs.BoolVar(&o.JSON, "json", false, "print each update as one JSON line")
	fs.StringVar(&o.Kinds, "kinds", "", "only these kinds, comma-separated (see docs/agents/working-mode.md)")
	fs.BoolVar(&o.Mine, "mine", false, "only units you or your agents created or that are assigned to you")
	fs.StringVar(&o.State, "state", watchStateFile, "cursor file (relative to the working directory)")
	fs.BoolVar(&o.Until, "until-update", false, "wait until there is news (a person's update on your units or new agent rules), print it and exit; run it in the background so it wakes you")
	fs.Float64Var(&o.Max, "max", 6900, "with --until-update: give up after this many seconds (prints a line, exit 0)")
	fs.StringVar(&o.Host, "host", os.Getenv("ARK_HOST"), "this device's id, sent as X-ARK-Host (env ARK_HOST; ark-server agent host-id)")
	fs.Usage = func() {
		fmt.Fprintln(errw, "usage: ark-server agent watch [--url URL] [--token-file F | --token-env NAME] [--project ID]\n"+
			"                              [--wait S] [--interval S] [--once] [--json] [--kinds K,..] [--mine]\n\n"+
			"Print what people did in ARK that this agent should act on: comments, replies,\n"+
			"Send back / Accept, decisions, assignments, setup confirmations, messages.\n"+
			"The cursor is kept in .ark-watch-cursor; the first run starts at \"now\".\n"+
			"Exit status: 0 ok, 1 error (with --once), 2 usage, 3 the token was refused.\n\noptions:")
		fs.PrintDefaults()
	}
	return fs, o, tokenEnv
}

// watchTokenFromFile reads the token file (default ~/.ark/tokens/<project>):
// a token there stays out of every command line.
func watchTokenFromFile(path, project string) string {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil || project == "" || strings.ContainsAny(project, `/\`) {
			return ""
		}
		path = filepath.Join(home, ".ark", "tokens", project)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line)
}

// agentWatch is `ark-server agent watch [flags]`.
func agentWatch(argv []string, out, errw io.Writer) int {
	fs, o, tokenEnv := agentWatchFlags(errw)
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(errw, "ark-server agent watch: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if o.Token == "" {
		o.Token = watchTokenFromFile(o.TokenFile, o.Project)
	}
	if o.Token == "" {
		o.Token = strings.TrimSpace(os.Getenv(*tokenEnv))
	}
	if o.Token == "" {
		fmt.Fprintf(errw, "ark-server agent watch: no agent token; write it to ~/.ark/tokens/<project> or set %s\n", *tokenEnv)
		return watchExitAuth
	}
	if o.Wait < 0 {
		o.Wait = 50
		if o.Once {
			o.Wait = 0
		}
	}
	o.Wait = min(o.Wait, 60)
	o.Interval = max(o.Interval, 1)
	o.URL = strings.TrimRight(o.URL, "/")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runWatch(ctx, o, out, errw)
}

func runWatch(ctx context.Context, o *watchOptions, out, errw io.Writer) int {
	state := loadWatchState(o)
	client := &http.Client{Timeout: time.Duration(o.Wait*float64(time.Second)) + 20*time.Second}
	started := time.Now()
	if o.Until {
		o.Mine = true
	}
	for {
		if o.Until && time.Since(started).Seconds() >= o.Max {
			fmt.Fprintf(out, "ARK: no updates in %.0fs; start the watcher again.\n", o.Max)
			return 0
		}
		reply, err := watchOnce(ctx, client, o, state.Cursor)
		if ctx.Err() != nil {
			return 0
		}
		if err != nil {
			var we *watchError
			if errors.As(err, &we) && we.auth() {
				fmt.Fprintln(errw, "ark-server agent watch:", err)
				return watchExitAuth
			}
			fmt.Fprintln(errw, "ark-server agent watch:", err)
			if o.Once {
				return watchExitError
			}
			if !sleepCtx(ctx, o.Interval) {
				return 0
			}
			continue
		}
		if reply.Reset && state.Cursor != "" {
			fmt.Fprintf(errw, "ark-server agent watch: the cursor was reset (%s); continuing from now\n", reply.Reason)
		}
		rulesChanged := state.Rules != "" && reply.Rules != "" && reply.Rules != state.Rules
		if o.Until && (len(reply.Items) > 0 || rulesChanged) {
			note := ""
			if rulesChanged {
				note = "; the agent rules changed to " + reply.Rules + " (GET /api/agent/v1/rules)"
			}
			fmt.Fprintf(out, "ARK UPDATE: %d new update(s)%s. Act on them (the items below, or GET /api/agent/v1/updates), then start this watcher again.\n",
				len(reply.Items), note)
		}
		for _, raw := range reply.Items {
			printWatchItem(out, raw, o.JSON)
		}
		state.Cursor = reply.Cursor
		if reply.Rules != "" {
			state.Rules = reply.Rules
		}
		saveWatchState(o, state, errw)
		if o.Until && (len(reply.Items) > 0 || rulesChanged) {
			return 0
		}
		if o.Once {
			if reply.More {
				fmt.Fprintln(errw, "ark-server agent watch: more updates are waiting; run it again")
			}
			return 0
		}
		if reply.More {
			continue
		}
		if o.Wait <= 0 && !sleepCtx(ctx, o.Interval) {
			return 0
		}
	}
}

func watchOnce(ctx context.Context, client *http.Client, o *watchOptions, cursor string) (*watchReply, error) {
	q := url.Values{}
	if cursor != "" {
		q.Set("since", cursor)
		if o.Wait > 0 {
			q.Set("wait", strconv.FormatFloat(o.Wait, 'f', -1, 64))
		}
	}
	if o.Kinds != "" {
		q.Set("kinds", o.Kinds)
	}
	if o.Mine {
		q.Set("mine", "1")
	}
	target := o.URL + "/api/agent/v1/updates"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+o.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ark-server-agent-watch/"+version)
	if o.Project != "" {
		req.Header.Set("X-ARK-Project", o.Project)
	}
	if o.Name != "" {
		req.Header.Set("X-Agent-Name", o.Name)
	}
	if o.Host != "" {
		req.Header.Set("X-ARK-Host", o.Host)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	var reply watchReply
	jerr := json.Unmarshal(body, &reply)
	if resp.StatusCode != 200 {
		msg := strings.TrimSpace(string(body))
		if jerr == nil && reply.Error != nil {
			msg = reply.Error.Message
			if reply.Error.Code != "" {
				msg += " (" + reply.Error.Code + ")"
			}
		}
		if (resp.StatusCode == 404 && jerr != nil) || strings.Contains(msg, "Unknown agent endpoint") {
			msg = "this ARK server has no /api/agent/v1/updates (update it): " + msg
		}
		return nil, &watchError{resp.StatusCode, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, msg)}
	}
	if jerr != nil {
		return nil, fmt.Errorf("bad reply from %s: %v", target, jerr)
	}
	return &reply, nil
}

func printWatchItem(out io.Writer, raw json.RawMessage, asJSON bool) {
	if asJSON {
		fmt.Fprintln(out, string(raw))
		return
	}
	var it watchItem
	if json.Unmarshal(raw, &it) != nil {
		fmt.Fprintln(out, string(raw))
		return
	}
	at := it.At
	if t, err := time.Parse(time.RFC3339, it.At); err == nil {
		at = t.Local().Format("01-02 15:04")
	}
	if it.Kind == "message" && it.Message != nil {
		fmt.Fprintf(out, "%s  >>> MESSAGE #%d from %s: %s\n", at, it.Message.ID, it.Actor, oneLine(it.Message.Body))
		fmt.Fprintf(out, "             reply: POST %s {\"body\": \"...\"}\n", it.Message.Reply)
		return
	}
	line := fmt.Sprintf("%s  %-16s %s", at, it.Kind, oneLine(it.Summary))
	if it.Comment != nil {
		line += fmt.Sprintf("  [comment %d]", it.Comment.ID)
	}
	if it.Link != "" {
		line += "  " + it.Link
	}
	fmt.Fprintln(out, line)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func sleepCtx(ctx context.Context, seconds float64) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(time.Duration(seconds * float64(time.Second))):
		return true
	}
}

// loadWatchState reads the cursor file; a cursor saved for another server
// or project is not used.
func loadWatchState(o *watchOptions) watchState {
	fresh := watchState{URL: o.URL, Project: o.Project}
	raw, err := os.ReadFile(o.State)
	if err != nil {
		return fresh
	}
	var s watchState
	if json.Unmarshal(raw, &s) != nil || s.URL != o.URL || s.Project != o.Project {
		return fresh
	}
	return s
}

func saveWatchState(o *watchOptions, s watchState, errw io.Writer) {
	s.URL, s.Project, s.Saved = o.URL, o.Project, time.Now().UTC().Format(time.RFC3339)
	data, _ := json.MarshalIndent(s, "", "  ")
	tmp := o.State + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		fmt.Fprintln(errw, "ark-server agent watch: cannot save the cursor:", err)
		return
	}
	if err := os.Rename(tmp, o.State); err != nil {
		fmt.Fprintln(errw, "ark-server agent watch: cannot save the cursor:", err)
	}
}
