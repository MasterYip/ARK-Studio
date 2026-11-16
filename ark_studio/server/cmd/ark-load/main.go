// Command ark-load simulates many signed-in ARK users against one server.
//
//	ark-load --url http://127.0.0.1:8890 --users users.txt --password P \
//	         --duration 60s --think 1s --pid 1234 --out result.json
//
// Every virtual user signs in through the real /login form (CSRF + PBKDF2),
// keeps its own cookie and keep-alive connection like a browser tab, and then
// loops over a weighted mix of the ported endpoints with a randomized think
// time.  With --pid the process tree of the server is sampled for CPU and
// memory.  The same binary drives the Python and the Go server, so the
// numbers are comparable.
//
// Mix and flags:
//
//	--write-pct P   share of writes in the mix, 0..50 (default 15).  Reads keep
//	                their relative weights (units list 25, unit detail 20,
//	                revision 20, home 8, graph snapshot 7, activity 7,
//	                comments 5).  Writes, with the bodies web/js sends:
//	                comment POST 40%, node edit PATCH /api/units/<uid>
//	                (title, body, status, tags) 30%, topic canvas layout save
//	                PATCH /api/units/<topic>/members {"items":[{unit,x,y}]}
//	                15%, topic design 15% (POST .../members {"add":[uid]},
//	                later PATCH {"remove":[uid]} of the same unit).  Topic
//	                ops are dropped when the project has no topic members.
//	--read-only     same as --write-pct 0.
//	--user-count N  use only the first N names of --users.
//	--timeout D     client timeout per request (default 60s).
//	--show-errors N print the first N failed requests with their answer.
//	--stats-url U   the Go server's expvar endpoint, e.g.
//	                http://127.0.0.1:6061/debug/vars (ARK_PPROF=127.0.0.1:6061).
//	                Snapshotted before and after the measured run; the
//	                "server_sqlite" section reports writer-queue waits
//	                (count, queued, p50/p95/p99 from a histogram, mean, max),
//	                hold times, queue depth max, SQLITE_BUSY/LOCKED errors and
//	                read-pool waits, per database class ("wal" = projects,
//	                "delete" = org).  Absent for the Python server.
//
// The result reports per op and overall p50/p95/p99, reads vs writes,
// throughput, errors by HTTP status and by class (409 conflicts are counted
// apart from errors; "db-locked" = 5xx mentioning a locked/busy database,
// "server-timeout" = 503/504 deadline answers, "client-timeout").
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type vuser struct {
	name      string
	client    *http.Client
	uids      []string
	topics    []topic
	topicUIDs map[string]bool
	added     []added // topic members this user added and may remove again
}

type sample struct {
	name   string
	write  bool
	dur    time.Duration
	class  string // "ok", "conflict" or an error class
	status string // HTTP status or transport error class
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func newUser(name string, uids []string, timeout time.Duration) *vuser {
	jar, _ := cookiejar.New(nil)
	tr := &http.Transport{MaxConnsPerHost: 2, MaxIdleConnsPerHost: 2, IdleConnTimeout: 90 * time.Second,
		DisableCompression: false}
	return &vuser{name: name, uids: uids, client: &http.Client{Jar: jar, Transport: tr, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (u *vuser) login(base, password string) error {
	resp, err := u.client.Get(base + "/login")
	if err != nil {
		return err
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := csrfRe.FindSubmatch(page)
	if m == nil {
		return fmt.Errorf("no csrf token for %s (status %d)", u.name, resp.StatusCode)
	}
	form := url.Values{"username": {u.name}, "password": {password}, "csrf": {string(m[1])}, "next": {"/"}}
	req, _ := http.NewRequest("POST", base+"/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", base)
	resp, err = u.client.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()
	if resp.StatusCode != 303 {
		return fmt.Errorf("login %s: status %d", u.name, resp.StatusCode)
	}
	return nil
}

func (u *vuser) getJSON(target string, v any) error {
	resp, err := u.client.Get(target)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: status %d", target, resp.StatusCode)
	}
	return json.Unmarshal(data, v)
}

// do sends rq and returns the outcome class, the status (or transport error
// class) and the body when rq.next needs it (else the start of error bodies).
func (u *vuser) do(base string, rq request) (string, string, []byte) {
	var body io.Reader
	if rq.body != "" {
		body = strings.NewReader(rq.body)
	}
	req, _ := http.NewRequest(rq.method, base+rq.path, body)
	if rq.method != "GET" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", base)
	}
	resp, err := u.client.Do(req)
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "Timeout") || strings.Contains(msg, "deadline"):
			return "client-timeout", "client-timeout", nil
		case strings.Contains(msg, "refused"):
			return "refused", "refused", nil
		case strings.Contains(msg, "reset") || strings.Contains(msg, "forcibly closed") || strings.Contains(msg, "EOF"):
			return "reset", "reset", nil
		}
		return "transport", "transport", nil
	}
	var head []byte
	switch {
	case rq.next != nil:
		head, _ = io.ReadAll(resp.Body)
	case resp.StatusCode >= 300:
		head, _ = io.ReadAll(io.LimitReader(resp.Body, 8192))
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()
	return classify(resp.StatusCode, head), strconv.Itoa(resp.StatusCode), head
}

func percentile(sorted []time.Duration, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * p)
	return float64(sorted[i].Microseconds()) / 1000
}

type stats struct {
	Count     int            `json:"count"`
	OK        int            `json:"ok"`
	Errors    int            `json:"errors"`
	Conflicts int            `json:"conflicts"`
	P50ms     float64        `json:"p50_ms"`
	P95ms     float64        `json:"p95_ms"`
	P99ms     float64        `json:"p99_ms"`
	MaxMs     float64        `json:"max_ms"`
	MeanMs    float64        `json:"mean_ms"`
	ErrRate   float64        `json:"error_rate"`
	RPS       float64        `json:"throughput_rps"`
	ErrKind   map[string]int `json:"errors_by_status,omitempty"`
	ErrClass  map[string]int `json:"errors_by_class,omitempty"`
}

func summarize(samples []sample, elapsed time.Duration) stats {
	d := make([]time.Duration, 0, len(samples))
	s := stats{}
	var total time.Duration
	for _, x := range samples {
		s.Count++
		switch x.class {
		case "ok":
			s.OK++
		case "conflict":
			s.Conflicts++
		default:
			s.Errors++
			if s.ErrKind == nil {
				s.ErrKind, s.ErrClass = map[string]int{}, map[string]int{}
			}
			s.ErrKind[x.status]++
			s.ErrClass[x.class]++
		}
		d = append(d, x.dur)
		total += x.dur
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	s.P50ms, s.P95ms, s.P99ms = percentile(d, .50), percentile(d, .95), percentile(d, .99)
	if len(d) > 0 {
		s.MaxMs = float64(d[len(d)-1].Microseconds()) / 1000
		s.MeanMs = float64(total.Microseconds()) / 1000 / float64(len(d))
		s.ErrRate = float64(s.Errors) / float64(s.Count)
	}
	if elapsed > 0 {
		s.RPS = float64(s.Count) / elapsed.Seconds()
	}
	return s
}

func main() {
	base := flag.String("url", "http://127.0.0.1:8890", "server base URL")
	usersFile := flag.String("users", "", "file with one username per line")
	userCount := flag.Int("user-count", 0, "use only the first N users of --users (0 = all)")
	password := flag.String("password", "", "password of every user")
	uidsFile := flag.String("uids", "", "file with unit uids to read/comment (one per line)")
	duration := flag.Duration("duration", 60*time.Second, "measured duration after every user signed in")
	think := flag.Duration("think", time.Second, "mean think time between requests per user (0 = closed loop)")
	pid := flag.Int("pid", 0, "server process id to sample (with its children)")
	out := flag.String("out", "", "write the JSON result here")
	label := flag.String("label", "", "label stored in the result")
	loginParallel := flag.Int("login-parallel", 32, "concurrent sign-ins during ramp-up")
	readOnly := flag.Bool("read-only", false, "drop the write operations from the mix (= --write-pct 0)")
	writePct := flag.Float64("write-pct", 15, "percentage of write requests in the mix, 0..50")
	timeout := flag.Duration("timeout", 60*time.Second, "client timeout per request")
	showErrors := flag.Int("show-errors", 0, "print the first N error answers (request and body start) to stderr")
	statsURL := flag.String("stats-url", "", "Go server expvar URL (e.g. http://127.0.0.1:6061/debug/vars) for SQLite writer counters")
	flag.Parse()
	if *readOnly {
		*writePct = 0
	}
	if *writePct < 0 || *writePct > 50 {
		fmt.Fprintln(os.Stderr, "ark-load: --write-pct must be between 0 and 50")
		os.Exit(2)
	}
	users := readLines(*usersFile)
	if *userCount > 0 {
		if *userCount > len(users) {
			fmt.Fprintf(os.Stderr, "ark-load: --user-count %d but %s has %d users\n", *userCount, *usersFile, len(users))
			os.Exit(2)
		}
		users = users[:*userCount]
	}
	uids := readLines(*uidsFile)
	if len(users) == 0 || len(uids) == 0 {
		fmt.Fprintln(os.Stderr, "ark-load: --users and --uids are required")
		os.Exit(2)
	}
	vus := make([]*vuser, len(users))
	loginStart := time.Now()
	var wg sync.WaitGroup
	var loginErrors atomic.Int64
	var signedIn atomic.Pointer[vuser]
	sem := make(chan struct{}, *loginParallel)
	for i, name := range users {
		vus[i] = newUser(name, uids, *timeout)
		wg.Add(1)
		go func(u *vuser) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// Python's socketserver listens with a backlog of 5, so a burst of
			// new connections can be refused; retry like a browser would.
			var err error
			for attempt := 0; attempt < 8; attempt++ {
				if err = u.login(*base, *password); err == nil {
					signedIn.CompareAndSwap(nil, u)
					return
				}
				time.Sleep(time.Duration(50*(1<<attempt)) * time.Millisecond)
			}
			loginErrors.Add(1)
			fmt.Fprintln(os.Stderr, err)
		}(vus[i])
	}
	wg.Wait()
	loginTime := time.Since(loginStart)
	fmt.Fprintf(os.Stderr, "%d users signed in in %s (%d errors)\n", len(users), loginTime.Round(time.Millisecond), loginErrors.Load())

	var topics []topic
	if *writePct > 0 {
		if u := signedIn.Load(); u != nil {
			var err error
			if topics, err = discoverTopics(u, *base); err != nil {
				fmt.Fprintln(os.Stderr, "ark-load: topics unavailable, topic writes dropped:", err)
				topics = nil
			}
		}
	}
	topicUIDs := map[string]bool{}
	for _, t := range topics {
		topicUIDs[t.uid] = true
	}
	for _, u := range vus {
		u.topics, u.topicUIDs = topics, topicUIDs
	}
	mix := buildMix(*writePct, len(topics) > 0)
	weights := map[string]float64{}
	for _, o := range mix {
		weights[o.name] = o.weight
	}

	var before map[string]sqliteClass
	if *statsURL != "" {
		var err error
		if before, err = fetchServerStats(*statsURL); err != nil {
			fmt.Fprintln(os.Stderr, "ark-load: --stats-url:", err)
		}
	}
	var mu sync.Mutex
	var samples []sample
	var shown atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	sampler := startSampler(*pid)
	start := time.Now()
	for _, u := range vus {
		wg.Add(1)
		go func(u *vuser) {
			defer wg.Done()
			local := make([]sample, 0, 256)
			// Spread the first requests so 500 users do not start in lockstep.
			if *think > 0 {
				time.Sleep(time.Duration(rand.Int64N(int64(*think))))
			}
			for ctx.Err() == nil {
				o := mix[choose(mix)]
				first := o.build(u)
				for rq := &first; rq != nil && ctx.Err() == nil; {
					if rq.name == "" {
						rq.name, rq.write = o.name, o.write
					}
					t0 := time.Now()
					class, status, body := u.do(*base, *rq)
					local = append(local, sample{rq.name, rq.write, time.Since(t0), class, status})
					if class != "ok" && shown.Add(1) <= int64(*showErrors) {
						fmt.Fprintf(os.Stderr, "error %s: %s %s %s -> %s %.300s\n", rq.name, rq.method, rq.path, rq.body, status, body)
					}
					if rq.next == nil {
						break
					}
					rq = rq.next(class, body)
				}
				if *think > 0 {
					// Exponential think time with the given mean, capped at 5x.
					wait := time.Duration(rand.ExpFloat64() * float64(*think))
					wait = min(wait, 5**think)
					select {
					case <-time.After(wait):
					case <-ctx.Done():
					}
				}
			}
			mu.Lock()
			samples = append(samples, local...)
			mu.Unlock()
		}(u)
	}
	wg.Wait()
	elapsed := time.Since(start)
	usage := sampler.stop()
	byName := map[string][]sample{}
	var reads, writes []sample
	contention := map[string]int{"conflicts_409": 0, "db_locked_5xx": 0, "server_timeouts": 0, "client_timeouts": 0}
	for _, s := range samples {
		byName[s.name] = append(byName[s.name], s)
		if s.write {
			writes = append(writes, s)
		} else {
			reads = append(reads, s)
		}
		switch s.class {
		case "conflict":
			contention["conflicts_409"]++
		case "db-locked":
			contention["db_locked_5xx"]++
		case "server-timeout":
			contention["server_timeouts"]++
		case "client-timeout":
			contention["client_timeouts"]++
		}
	}
	perOp := map[string]stats{}
	for name, sel := range byName {
		perOp[name] = summarize(sel, elapsed)
	}
	overall := summarize(samples, elapsed)
	result := map[string]any{
		"label": *label, "url": *base, "users": len(users), "login_errors": loginErrors.Load(),
		"login_seconds": loginTime.Seconds(), "duration_seconds": elapsed.Seconds(),
		"think_mean_seconds": think.Seconds(), "write_pct": *writePct, "mix_weights": weights,
		"topics": len(topics), "requests": overall.Count,
		"throughput_rps": overall.RPS, "overall": overall,
		"reads": summarize(reads, elapsed), "writes": summarize(writes, elapsed),
		"client_contention": contention,
		"per_endpoint":      perOp, "server": usage,
	}
	if before != nil {
		if after, err := fetchServerStats(*statsURL); err != nil {
			fmt.Fprintln(os.Stderr, "ark-load: --stats-url:", err)
		} else {
			result["server_sqlite"] = statsDelta(before, after)
		}
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	if *out != "" {
		os.WriteFile(*out, data, 0o644) //nolint:errcheck
	}
	fmt.Println(string(data))
}

func readLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			out = append(out, t)
		}
	}
	return out
}
