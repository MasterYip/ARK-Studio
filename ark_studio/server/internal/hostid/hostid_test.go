package hostid

import (
	"net"
	"regexp"
	"testing"
)

func TestHashes(t *testing.T) {
	// The documented shell one-liners hash exactly "ark|" + the raw id.
	if got := FromMachineID("0123456789abcdef0123456789abcdef\n"); got != short("ark|0123456789abcdef0123456789abcdef") {
		t.Fatal(got)
	}
	if got := FromMachineID("x"); len(got) != 12 {
		t.Fatal(got)
	}
	if FromMAC("AA:BB:CC:00:11:22") != FromMAC("aa:bb:cc:00:11:22") {
		t.Fatal("MAC case")
	}
	if FromMAC("aa:bb:cc:00:11:22") == FromMachineID("aa:bb:cc:00:11:22") {
		t.Fatal("MAC and machine id share a namespace")
	}
}

func TestCompute(t *testing.T) {
	id := Compute()
	if id.Method == MethodNone {
		t.Skip("no machine id and no physical adapter here")
	}
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(id.ID) {
		t.Fatalf("%+v", id)
	}
	if Compute() != id {
		t.Fatal("not stable")
	}
}

func TestPrimaryMAC(t *testing.T) {
	mac := func(s string) net.HardwareAddr { m, _ := net.ParseMAC(s); return m }
	list := []Iface{
		{Name: "lo", Flags: net.FlagLoopback | net.FlagUp, MAC: mac("00:00:00:00:00:00")},
		{Name: "docker0", Flags: net.FlagUp, MAC: mac("02:42:ac:11:00:02"), HasIP: true},
		{Name: "wlan0", Flags: net.FlagUp, MAC: mac("3a:11:22:33:44:55"), HasIP: true}, // randomised (local bit)
		{Name: "eth1", Flags: 0, MAC: mac("00:1b:21:aa:bb:01")},
		{Name: "eth0", Flags: net.FlagUp, MAC: mac("00:1b:21:aa:bb:00"), HasIP: true},
		{Name: "vEthernet (WSL)", Flags: net.FlagUp, MAC: mac("00:15:5d:01:02:03"), HasIP: true},
	}
	if got := primaryMAC(list); got != "00:1b:21:aa:bb:00" {
		t.Fatalf("got %q", got)
	}
	if primaryMAC(list[:3]) != "" {
		t.Fatal("virtual or randomised adapters must not be used")
	}
}
