package gpu

import (
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// A GNU df -PB1 listing of a GPU box: root, a data disk, an NFS share, a bind
// mount of the data disk, pseudo and snap filesystems, a mount point with a
// space and junk lines.
const fakeDF = `Filesystem                1-blocks          Used     Available Capacity Mounted on
udev                   67430334464             0   67430334464       0% /dev
tmpfs                  13496356864       3194880   13493161984       1% /run
/dev/nvme0n1p2        983349346304  432673669120  500644196352      47% /
tmpfs                  67481776128             0   67481776128       0% /dev/shm
/dev/nvme0n1p1           535805952       6201344     529604608       2% /boot/efi
/dev/sda1           7937876074496 7143925706752  793950367744      91% /data
/dev/sda1           7937876074496 7143925706752  793950367744      91% /data/projects/bind
nas:/export/home    1099511627776  549755813888  549755813888      50% /mnt/nas home
/dev/loop3                67108864      67108864             0     100% /snap/core20/1974
overlay               983349346304  432673669120  500644196352      47% /var/lib/docker/overlay2/x/merged
/dev/sdb1            107374182400   10737418240   96636764160      10% /scratch
broken line without numbers
/dev/sdc1                  zero          0          0     0% /bad
/dev/sdd1                     0          0          0     0% /empty
`

func TestParseDFKeepsRealDeduplicatedMounts(t *testing.T) {
	mounts := ParseDF(fakeDF)
	var got []string
	for _, m := range mounts {
		got = append(got, m.Mount)
	}
	if want := "/|/data|/mnt/nas home|/scratch"; strings.Join(got, "|") != want {
		t.Fatalf("mounts %q, want %q", strings.Join(got, "|"), want)
	}
	if p := mounts[1].Percent(); p != 90.0 {
		t.Errorf("/data percent %v, want 90.0 (used / (used + available))", p)
	}
	if ParseDF("") != nil || ParseDF("Filesystem 1-blocks Used Available Capacity Mounted on\n") != nil {
		t.Error("empty df output should give no mounts")
	}
	// A bind mount listed first keeps the shorter mount point of its device.
	twice := "/dev/sda1 100 50 50 50% /data/x\n/dev/sda1 100 50 50 50% /data\n"
	if m := ParseDF(twice); len(m) != 1 || m[0].Mount != "/data" {
		t.Errorf("bind mount dedupe: %+v", m)
	}
}

func storageMounts(items []any) string {
	var out []string
	for _, item := range items {
		o := item.(*pyjson.Object)
		s := pyjson.Str(o.Value("mount"))
		if o.Value("data") == true {
			s += "*"
		}
		out = append(out, s)
	}
	return strings.Join(out, "|")
}

func TestSelectStorage(t *testing.T) {
	mounts := ParseDF(fakeDF + "/dev/sde1 1000000 1 999999 1% /tiny\n")
	if got := storageMounts(SelectStorage(mounts, nil)); got != "/data|/mnt/nas home|/|/scratch" {
		t.Errorf("largest first, at most 4: %s", got)
	}
	if got := storageMounts(SelectStorage(mounts, []string{"/scratch/proj", "/home/me"})); got != "/scratch*|/*|/data|/mnt/nas home" {
		t.Errorf("data mounts first: %s", got)
	}
	if got := storageMounts(SelectStorage(mounts, []string{"/tiny"})); got != "/tiny*|/data|/mnt/nas home|/" {
		t.Errorf("a small data mount is kept: %s", got)
	}
	if SelectStorage(nil, []string{"/x"}) != nil {
		t.Error("no mounts, no storage")
	}
	first := SelectStorage(mounts, nil)[0].(*pyjson.Object)
	want := `{"mount": "/data", "used_bytes": 7143925706752, "total_bytes": 7937876074496, "free_bytes": 793950367744, "percent": 90.0, "data": false}`
	if got := string(pyjson.Dumps(first)); got != want {
		t.Errorf("item\n got %s\nwant %s", got, want)
	}
}

func TestParseStorageSection(t *testing.T) {
	text := "__ARK_LOAD__\n1 1 1\n__ARK_ENV__\n__ARK_DF__\n" + fakeDF
	if got := storageMounts(ParseStorage(text, nil)); got != "/data|/mnt/nas home|/|/scratch" {
		t.Errorf("section: %s", got)
	}
	for _, text := range []string{"__ARK_LOAD__\n1 1 1\n", "__ARK_DF__\n", "__ARK_DF__\nbash: df: command not found\n",
		"__ARK_DF__\n" + strings.Repeat("\xff garbage 1 2 3\n", 10)} {
		if got := ParseStorage(text, nil); got != nil {
			t.Errorf("%q: want no storage, got %v", text, got)
		}
	}
	if !strings.Contains(SystemCommand, StorageCommand) || !strings.Contains(StorageCommand, "df -PB1") {
		t.Error("SystemCommand must run the df section")
	}
}

func TestDataPaths(t *testing.T) {
	cfg := pyjson.NewObject("queue", pyjson.NewObject("path_map", []any{
		pyjson.NewObject("from", "/home/me/a", "to", "/data/a", "servers", []any{}),
		pyjson.NewObject("from", "/home/me/b", "to", "/scratch/b", "servers", []any{"gpu2"}),
	}))
	if got := strings.Join(DataPaths(cfg, "gpu1"), "|"); got != "/data/a" {
		t.Errorf("gpu1: %s", got)
	}
	if got := strings.Join(DataPaths(cfg, "gpu2"), "|"); got != "/data/a|/scratch/b" {
		t.Errorf("gpu2: %s", got)
	}
	if DataPaths(nil, "x") != nil || DataPaths(pyjson.NewObject(), "x") != nil {
		t.Error("no queue section, no data paths")
	}
}

// The probe gains "storage" from the system query; a system query without
// df output (or failing) leaves it out and the GPU result is unchanged.
func TestProbeServerStorage(t *testing.T) {
	server := pyjson.NewObject("name", "a", "host", "a.fake")
	gpus := func(*pyjson.Object, int) CommandResult {
		return CommandResult{Stdout: "0, RTX 4090, 24564, 1200, 3\n"}
	}
	system := func(out string, code int) Runner {
		return func(*pyjson.Object, int) CommandResult {
			if code != 0 {
				return CommandResult{ReturnCode: code, ErrorKind: "command_failed", Message: "boom"}
			}
			return CommandResult{Stdout: out}
		}
	}
	withDF := "__ARK_LOAD__\n0.5 0.4 0.3\n__ARK_NPROC__\n8\n__ARK_ENV__\n__ARK_DF__\n" + fakeDF
	payload, err := ProbeServer(server, gpus, 5, system(withDF, 0), "/scratch/run")
	if err != nil {
		t.Fatal(err)
	}
	if got := storageMounts(payload.Value("storage").([]any)); got != "/scratch*|/data|/mnt/nas home|/" {
		t.Errorf("storage: %s", got)
	}
	if sys := payload.Value("system").(*pyjson.Object); sys.Value("cores") != int64(8) || sys.Value("error") != nil {
		t.Errorf("system parsing disturbed by the df section: %s", pyjson.Dumps(sys))
	}
	for name, runner := range map[string]Runner{
		"no df":       system("__ARK_LOAD__\n0.5 0.4 0.3\n__ARK_DF__\n", 0),
		"df garbage":  system("__ARK_DF__\nnot df output\n", 0),
		"system fail": system("", 1),
	} {
		payload, err := ProbeServer(server, gpus, 5, runner)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if payload.Has("storage") {
			t.Errorf("%s: unexpected storage %s", name, pyjson.Dumps(payload.Value("storage")))
		}
		if payload.Value("ok") != true || len(payload.Value("gpus").([]any)) != 1 {
			t.Errorf("%s: GPU probe disturbed: %s", name, pyjson.Dumps(payload))
		}
	}
	// No system runner (the queue's allocation probe): no storage either.
	payload, _ = ProbeServer(server, gpus, 5, nil, "/data")
	if payload.Has("storage") {
		t.Error("storage without a system query")
	}
}
