// Command fakessh is the `ssh` of the GPU replay (replay_services.py, module
// "gpu"): it never opens a connection.  It answers the GPU probe, system,
// allocation and queue commands from fixed scripts chosen by the target host,
// keeps the fake remote job state of each server under $ARK_FAKE_SSH_DIR/<who>
// (who = the server's data folder name, from ARK_PRIVATE_DIR) and logs every
// argv there so the replay can compare what both servers sent.
//
//	go build -o ssh.exe fakessh.go     (replay_services.py builds it)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const marker = "__ARK_QUEUE_LOG_TAIL_V1__"

var (
	dir  string
	who  string
	host string
)

func main() {
	args := os.Args[1:]
	dir = os.Getenv("ARK_FAKE_SSH_DIR")
	who = filepath.Base(filepath.Dir(os.Getenv("ARK_PRIVATE_DIR")))
	if dir == "" {
		fmt.Fprintln(os.Stderr, "fake ssh: ARK_FAKE_SSH_DIR is not set")
		os.Exit(255)
	}
	logInvocation(args)
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || sep+2 > len(args) {
		fail(255, "usage: ssh [options] -- target command")
	}
	target := args[sep+1]
	host = target[strings.LastIndex(target, "@")+1:]
	rest := args[sep+2:]
	if len(rest) == 3 && rest[0] == "nvidia-smi" {
		probe()
		return
	}
	if len(rest) == 3 && rest[0] == "bash" && rest[1] == "-lc" {
		command(unquote(rest[2]))
		return
	}
	fail(127, "fake ssh: unsupported remote command")
}

func logInvocation(args []string) {
	path := filepath.Join(dir, who, "log")
	os.MkdirAll(path, 0o755) //nolint:errcheck
	data, _ := json.Marshal(args)
	name := fmt.Sprintf("%d-%d.json", time.Now().UnixNano(), os.Getpid())
	os.WriteFile(filepath.Join(path, name), data, 0o644) //nolint:errcheck
}

func fail(code int, stderr string) {
	os.Stderr.WriteString(stderr)
	os.Exit(code)
}

// unquote undoes shlex.quote ('...' with '"'"' for a quote).
func unquote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return s
			}
			b.WriteString(s[i+1 : i+1+j])
			i += j + 2
		case '"':
			j := strings.IndexByte(s[i+1:], '"')
			if j < 0 {
				return s
			}
			b.WriteString(s[i+1 : i+1+j])
			i += j + 2
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

var gpuRows = map[string]string{
	"a": "0, NVIDIA GeForce RTX 4090, 24564, 1200, 3, 5, 41, 70.25\n" +
		"1, NVIDIA GeForce RTX 4090, 24564, 23000, 97, 80, 77, 350.5\n",
	"b": "0, NVIDIA H20, 97871, 0, 0, 0, 30, 60.0\n1, NVIDIA H20, 97871, 97500, 0, 0, 30, [N/A]\n" +
		"2, NVIDIA H20, 97871, 50000, 100, 3, 50, 300\n3, NVIDIA H20, [N/A], 100, 5, [Not Supported], 35, 61.125\n" +
		"4, NVIDIA H20, 97871, 1000.15, 0.15, 2.675, 0.25, 0.35\n",
	"odd": "index, name, memory.total [MiB]\r\nNo devices were found\r\n\r\n0, short\r\nx, bad index, 1, 1, 1\r\n" +
		"1.5, half, 1, 1, 1\r\n３, fullwidth, 8000, 1000, ２\r\n1_0, underscore, 8_000, 1e3, 5e0\r\n" +
		" 4 ,   , 1E4, 5e2, 50.05 , n/a, unknown, none\r" +
		"5, " + strings.Repeat("长名字", 50) + ", inf, 10, 1\n" +
		"6, nan util, 4000, 100, nan\n7, neg, 1000, 2000, -5, 1, 2, 3\n8, extra, 1, 0, 0, 1, 2, 3, 4, 5\n" +
		"9, Ünïcode ＧＰＵ, 20000, 10000, 1 " + "10, ls, 20000, 100, 0\x0b11, vt, 30000, 0, 0\n",
	"warn":    strings.Repeat("broken line\n", 25),
	"empty":   "",
	"badutf8": "0, bad \xff\xfe name, 1000, 10, 1\n",
	"nan":     "nan, x, 1, 1, 1\n",
	"inf":     "inf, x, 1, 1, 1\n",
	"q1":      "0, A100, 40000, 20000, 2\n1, A100, 40000, 32000, 0\n",
	"q2":      "0, H20, 97871, 57871, 50\n",
}

func short(h string) string { return strings.TrimSuffix(h, ".fake") }

func probe() {
	h := short(host)
	switch h {
	case "auth":
		fail(255, "replay@auth.fake: Permission denied (publickey,password).\r\n")
	case "unreach", "q3":
		fail(255, "ssh: Could not resolve hostname "+host+": Name or service not known\n")
	case "kex":
		fail(255, "kex_exchange_identification: read: Software caused connection abort\r\n")
	case "fail":
		os.Exit(9)
	case "fail2":
		fail(1, "  nvidia-smi: 未找到命令 "+strings.Repeat("错误", 200)+"\n")
	case "slow":
		time.Sleep(60 * time.Second)
	case "big":
		var b strings.Builder
		for i := 0; b.Len() < 70000; i++ {
			fmt.Fprintf(&b, "%d, 卡%s, %d, %d, %d\n", i%16, strings.Repeat("名", 20), 8000+i, i%7000, i%100)
		}
		os.Stdout.WriteString(b.String())
		return
	}
	rows, ok := gpuRows[h]
	if !ok {
		fail(255, "ssh: connect to host "+host+" port 22: Connection refused\n")
	}
	os.Stdout.WriteString(rows)
}

const systemA = `__ARK_LOAD__
1.50 0.75 0.25
__ARK_NPROC__
16
__ARK_MEM__
64000 12000 30000 50000
__ARK_CPU__
%Cpu(s):  3.1 us,  1.0 sy,  0.0 ni, 95.2 id,  0.5 wa,  0.0 hi,  0.2 si,  0.0 st
__ARK_BUS__
0, 00000000:01:00.0
1, 00000000:02:00.0
x, 00000000:03:00.0
__ARK_PROC__
1234, 00000000:01:00.0, 1100
2345, 00000000:02:00.0, [N/A]
3456, 00000000:09:00.0, 10
abc, 00000000:01:00.0, 5
4567, 00000000:01:00.0, nan
__ARK_PMON__
# gpu         pid  type    sm   mem   enc   dec   command
# Idx           #   C/G     %     %     %     %   name
    0       1234     C     12     3     -     -   python
    1       2345     C      -     -     -     -   python
    0       4567     C    250     -     -     -   python
    x       9999     C      1     -     -     -   bad
__ARK_ENV__
1234 gpu_abc
`

var systems = map[string]string{
	"a":       systemA,
	"b":       "__ARK_LOAD__\n0.10 0.20 0.30\n__ARK_NPROC__\n0\n__ARK_MEM__\n1 2 3\n__ARK_CPU__\nno idle here\n__ARK_BUS__\n0, 00000000:01:00.0\n__ARK_PROC__\n77, 00000000:01:00.0, 500\n__ARK_PMON__\n__ARK_ENV__\n",
	"odd":     "__ARK_LOAD__\nx y z\n__ARK_NPROC__\n4\n",
	"warn":    "__ARK_NPROC__\nabc\n",
	"big":     "__ARK_BUS__\n0, b0\n__ARK_PROC__\n1, b0, inf\n",
	"badutf8": "",
	"kex":     "__ARK_LOAD__\n２.5 1 1\n__ARK_NPROC__\n８\n__ARK_CPU__\n%Cpu(s): 12.5　id\n",
}

// dfOutputs answer the Go-only df section of the system query
// (ark_studio/server/internal/gpu/storage.go): a GPU box with a data disk, a bind mount and
// pseudo filesystems (a), df missing (b: only the marker) and junk (odd).
var dfOutputs = map[string]string{
	"a": "__ARK_DF__\n" +
		"Filesystem          1-blocks          Used     Available Capacity Mounted on\n" +
		"udev             67430334464             0   67430334464       0% /dev\n" +
		"tmpfs            13496356864       3194880   13493161984       1% /run\n" +
		"/dev/nvme0n1p2  983349346304  432673669120  500644196352      47% /\n" +
		"/dev/nvme0n1p1     535805952       6201344     529604608       2% /boot/efi\n" +
		"/dev/sda1      7937876074496 7143925706752  793950367744      91% /data\n" +
		"/dev/sda1      7937876074496 7143925706752  793950367744      91% /data/bind\n" +
		"/dev/sdb1      3937876074496 1181362822348 2756513252148      30% /scratch\n" +
		"/dev/loop3          67108864      67108864             0     100% /snap/core20/1974\n",
	"b":   "__ARK_DF__\n",
	"odd": "__ARK_DF__\ndf: unrecognized option '-x'\nnot a df row\n",
}

var (
	reJobID  = regexp.MustCompile(`ARK_GPU_JOB_ID=(gpu_[0-9a-f]+)`)
	reDevs   = regexp.MustCompile(`CUDA_VISIBLE_DEVICES=([0-9,]+)`)
	reStatus = regexp.MustCompile(`/(gpu_[0-9a-f]+)/status`)
	reLog    = regexp.MustCompile(`/(gpu_[0-9a-f]+)/job\.log`)
	reKill   = regexp.MustCompile(`kill -TERM -(\d+)`)
	reMarker = regexp.MustCompile(`replay:(\w+)(?:=(\d+))?`)
)

type job struct {
	ID     string `json:"id"`
	Host   string `json:"host"`
	PID    int    `json:"pid"`
	GPUs   []int  `json:"gpus"`
	Run    int    `json:"run"`
	Exit   int    `json:"exit"`
	Lost   bool   `json:"lost"`
	Mem    int    `json:"mem"`
	Reaps  int    `json:"reaps"`
	Killed bool   `json:"killed"`
	Done   bool   `json:"done"`
}

func stateDir() string { return filepath.Join(dir, who, "jobs") }

func loadJobs() []job {
	entries, _ := os.ReadDir(stateDir())
	var out []job
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(stateDir(), e.Name()))
		if err != nil {
			continue
		}
		var j job
		if json.Unmarshal(raw, &j) == nil {
			out = append(out, j)
		}
	}
	return out
}

func saveJob(j job) {
	os.MkdirAll(stateDir(), 0o755) //nolint:errcheck
	raw, _ := json.Marshal(j)
	os.WriteFile(filepath.Join(stateDir(), j.ID+".json"), raw, 0o644) //nolint:errcheck
}

func findJob(id string) (job, bool) {
	for _, j := range loadJobs() {
		if j.ID == id {
			return j, true
		}
	}
	return job{}, false
}

func busID(index int) string { return fmt.Sprintf("00000000:%02d:00.0", index+1) }

func command(cmd string) {
	h := short(host)
	switch {
	case strings.HasPrefix(cmd, "echo __ARK_LOAD__"): // SYSTEM_COMMAND
		if h == "empty" {
			fail(1, "bash: free: command not found\n")
		}
		os.Stdout.WriteString(systems[h])
		if strings.Contains(cmd, "echo __ARK_DF__; df -PB1") { // Go-only storage section (gpu/storage.go)
			os.Stdout.WriteString(dfOutputs[h])
		}
	case strings.HasPrefix(cmd, "echo __ARK_BUS__"): // ALLOCATION_COMMAND
		allocation(h)
	case strings.Contains(cmd, "setsid -f bash"):
		launch(cmd)
	case strings.HasPrefix(cmd, "if [ -f "):
		reap(cmd)
	case strings.HasPrefix(cmd, "kill -TERM -"):
		m := reKill.FindStringSubmatch(cmd)
		pid, _ := strconv.Atoi(m[1])
		for _, j := range loadJobs() {
			if j.PID == pid && j.Host == host {
				j.Killed = true
				saveJob(j)
			}
		}
	case strings.HasPrefix(cmd, "tail -n "):
		fields := strings.Fields(cmd)
		n, _ := strconv.Atoi(fields[2])
		m := reLog.FindStringSubmatch(cmd)
		if m == nil {
			return
		}
		if _, ok := findJob(m[1]); !ok {
			return
		}
		for i := 1; i <= min(n, 30); i++ {
			fmt.Printf("log line %d of %s 日志\n", i, m[1])
		}
	default:
		fail(127, "fake ssh: unknown queue command\n")
	}
}

func allocation(h string) {
	rows, ok := gpuRows[h]
	if !ok || h == "q3" {
		fail(255, "ssh: Could not resolve hostname "+host+": Name or service not known\n")
	}
	var b strings.Builder
	b.WriteString("__ARK_BUS__\n")
	for i := 0; i < strings.Count(rows, "\n"); i++ {
		fmt.Fprintf(&b, "%d, %s\n", i, busID(i))
	}
	b.WriteString("__ARK_PROC__\n")
	var env []string
	for _, j := range loadJobs() {
		if j.Host != host || j.Done || j.Killed || j.Lost {
			continue
		}
		for _, g := range j.GPUs {
			mem := "[N/A]"
			if j.Mem > 0 {
				mem = strconv.Itoa(j.Mem)
			}
			fmt.Fprintf(&b, "%d, %s, %s\n", j.PID, busID(g), mem)
		}
		env = append(env, fmt.Sprintf("%d %s", j.PID, j.ID))
	}
	b.WriteString("\n__ARK_ENV__\n")
	for _, e := range env {
		b.WriteString(e + "\n")
	}
	b.WriteString("__ARK_GPU__\n" + rows)
	os.Stdout.WriteString(b.String())
}

func launch(cmd string) {
	m := reJobID.FindStringSubmatch(cmd)
	if m == nil {
		fail(1, "fake ssh: no job id\n")
	}
	j := job{ID: m[1], Host: host}
	if d := reDevs.FindStringSubmatch(cmd); d != nil {
		for _, part := range strings.Split(d[1], ",") {
			n, _ := strconv.Atoi(part)
			j.GPUs = append(j.GPUs, n)
		}
	}
	for _, mk := range reMarker.FindAllStringSubmatch(cmd, -1) {
		n, _ := strconv.Atoi(mk[2])
		switch mk[1] {
		case "launchfail":
			fail(1, "mkdir: cannot create directory '/tmp/ark-gpu-queue': No space left on device\nsecond line\n")
		case "nopid":
			os.Stdout.WriteString("no pid here\n")
			return
		case "run":
			j.Run = n
		case "exit":
			j.Exit = n
		case "lost":
			j.Lost = true
		case "mem":
			j.Mem = n
		}
	}
	j.PID = 4000 + len(loadJobs())
	saveJob(j)
	fmt.Printf("%d\n", j.PID)
}

func reap(cmd string) {
	m := reStatus.FindStringSubmatch(cmd)
	j, ok := findJob(m[1])
	if !ok || j.Killed {
		fmt.Print("LOST\n\n" + marker + "\n")
		return
	}
	j.Reaps++
	defer saveJob(j)
	tail := "\n" + marker + "\nstep 1 ok\nTraceback (most recent call last):\nRuntimeError: CUDA out of memory 显存不足\n"
	switch {
	case j.Lost:
		j.Done = true
		fmt.Print("LOST" + tail)
	case j.Reaps <= j.Run:
		fmt.Print("RUNNING\n")
	default:
		j.Done = true
		if j.Exit == 0 {
			fmt.Print("DONE:0\n")
		} else {
			fmt.Printf("DONE:%d%s", j.Exit, tail)
		}
	}
}
