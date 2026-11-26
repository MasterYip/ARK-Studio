package main

// `ark-server agent paths` and `ark-server agent relocate` (Go only,
// 2026-11-24): this device's task space and paths, and moving recorded
// paths to a new prefix (or to task: paths) through the agent API
// (server/routes_taskspace.go).  The token comes from ~/.ark/tokens/<project>
// or an environment variable, never from the command line.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"arkstudio/go/internal/hostid"
)

type agentAPIOptions struct {
	URL, Project, Host, TokenFile, token string
}

func agentAPIFlags(fs *flag.FlagSet, o *agentAPIOptions) *string {
	url := os.Getenv("ARK_URL")
	if url == "" {
		url = "http://127.0.0.1:8890"
	}
	fs.StringVar(&o.URL, "url", url, "ARK server URL (env ARK_URL)")
	fs.StringVar(&o.Project, "project", os.Getenv("ARK_PROJECT"), "project id (env ARK_PROJECT)")
	fs.StringVar(&o.Host, "host", os.Getenv("ARK_HOST"), "this device's id, sent as X-ARK-Host (default: ark-server agent host-id)")
	fs.StringVar(&o.TokenFile, "token-file", os.Getenv("ARK_TOKEN_FILE"), "file holding the agent token (default ~/.ark/tokens/<project>)")
	return fs.String("token-env", "ARK_TOKEN", "environment variable holding the agent token")
}

func (o *agentAPIOptions) ready(tokenEnv string, errw io.Writer, name string) bool {
	if o.Host == "" {
		o.Host = hostid.Compute().ID
	}
	o.token = agentTokenFor(o.TokenFile, o.Project, tokenEnv)
	if o.token == "" {
		fmt.Fprintf(errw, "ark-server agent %s: no agent token; write it to ~/.ark/tokens/<project> or set %s\n", name, tokenEnv)
		return false
	}
	return true
}

func (o *agentAPIOptions) call(method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(o.URL, "/")+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+o.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ark-server-agent/"+version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if o.Project != "" {
		req.Header.Set("X-ARK-Project", o.Project)
	}
	if o.Host != "" {
		req.Header.Set("X-ARK-Host", o.Host)
	}
	setSessionHeader(req)
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	return resp.StatusCode, out, err
}

func printReply(out io.Writer, code int, raw []byte) int {
	var v any
	if json.Unmarshal(raw, &v) == nil {
		b, _ := json.MarshalIndent(v, "", "  ")
		raw = b
	}
	fmt.Fprintln(out, string(raw))
	if code != 200 {
		return 1
	}
	return 0
}

// agentPaths is `ark-server agent paths`.
func agentPaths(argv []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("ark-server agent paths", flag.ContinueOnError)
	fs.SetOutput(errw)
	o := &agentAPIOptions{}
	tokenEnv := agentAPIFlags(fs, o)
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if !o.ready(*tokenEnv, errw, "paths") {
		return 2
	}
	code, raw, err := o.call("GET", "/api/agent/v1/setup/paths", nil)
	if err != nil {
		fmt.Fprintln(errw, "ark-server agent paths:", err)
		return 1
	}
	return printReply(out, code, raw)
}

// agentRelocate is `ark-server agent relocate`.
func agentRelocate(argv []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("ark-server agent relocate", flag.ContinueOnError)
	fs.SetOutput(errw)
	o := &agentAPIOptions{}
	tokenEnv := agentAPIFlags(fs, o)
	from := fs.String("from", "", "old path prefix (an absolute folder)")
	to := fs.String("to", "", "new path prefix")
	logical := fs.Bool("logical", false, "convert absolute paths inside the task space to task: paths instead")
	all := fs.Bool("all-devices", false, "move setup locations of every device (default: only this device's)")
	apply := fs.Bool("apply", false, "write the changes (default: a dry run that lists them)")
	undo := fs.String("undo", "", "undo an applied relocation by its id (rl_...)")
	fs.Usage = func() {
		fmt.Fprintln(errw, "usage: ark-server agent relocate --from OLD --to NEW [--apply] [--all-devices]\n"+
			"       ark-server agent relocate --logical [--apply]\n"+
			"       ark-server agent relocate --undo rl_...\n\n"+
			"Rewrites paths recorded in ARK (unit bodies, attachment links, setup locations).\n"+
			"Files are not moved: move them first. Without --apply nothing is written.\n\noptions:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	body := map[string]any{}
	switch {
	case *undo != "":
		body["undo"] = *undo
	case *logical:
		body["logical"], body["apply"] = true, *apply
	case *from != "" && *to != "":
		body["from"], body["to"], body["apply"], body["all_devices"] = *from, *to, *apply, *all
	default:
		fs.Usage()
		return 2
	}
	if !o.ready(*tokenEnv, errw, "relocate") {
		return 2
	}
	code, raw, err := o.call("POST", "/api/agent/v1/setup/relocate", body)
	if err != nil {
		fmt.Fprintln(errw, "ark-server agent relocate:", err)
		return 1
	}
	return printReply(out, code, raw)
}

// agentFolder is `ark-server agent folder <UID>` (T-0132, 2026-11-26): the
// absolute folder for a unit's work on this device (setup/paths?unit=), as
// one line, created by ARK when the task space is on this machine.
func agentFolder(argv []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("ark-server agent folder", flag.ContinueOnError)
	fs.SetOutput(errw)
	o := &agentAPIOptions{}
	tokenEnv := agentAPIFlags(fs, o)
	asJSON := fs.Bool("json", false, "print the whole task_folder object")
	fs.Usage = func() {
		fmt.Fprintln(errw, "usage: ark-server agent folder <UID> [--json]\n\n"+
			"Prints the folder for that unit's work on this device: its task's folder, or the\n"+
			"unit's own folder when no task is above it.\n\noptions:")
		fs.PrintDefaults()
	}
	var uid string
	if len(argv) > 0 && !strings.HasPrefix(argv[0], "-") {
		uid, argv = argv[0], argv[1:]
	}
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if uid == "" && fs.NArg() > 0 {
		uid = fs.Arg(0)
	}
	if uid == "" {
		fs.Usage()
		return 2
	}
	if !o.ready(*tokenEnv, errw, "folder") {
		return 2
	}
	code, raw, err := o.call("GET", "/api/agent/v1/setup/paths?unit="+url.QueryEscape(uid), nil)
	if err != nil {
		fmt.Fprintln(errw, "ark-server agent folder:", err)
		return 1
	}
	if code != 200 {
		return printReply(errw, code, raw)
	}
	var reply struct {
		TaskFolder map[string]any `json:"task_folder"`
		Missing    []string       `json:"missing"`
	}
	if json.Unmarshal(raw, &reply) != nil {
		return printReply(errw, code, raw)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(reply.TaskFolder, "", "  ")
		fmt.Fprintln(out, string(b))
		return 0
	}
	path, _ := reply.TaskFolder["path"].(string)
	if path == "" {
		note, _ := reply.TaskFolder["note"].(string)
		fmt.Fprintln(errw, "ark-server agent folder: no folder for "+uid+"; "+note)
		return 1
	}
	fmt.Fprintln(out, path)
	return 0
}
