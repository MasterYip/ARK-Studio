// Package hostid is the stable id of the device an agent or ARK itself runs
// on (the X-ARK-Host header, setup locations).  User decision 2026-11-19:
// sha256("ark|" + the OS machine id), first 12 hex characters; the MAC of
// the primary physical network adapter only when the OS has no machine id.
//
//	Windows  HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid
//	Linux    /etc/machine-id (or /var/lib/dbus/machine-id)
//	macOS    IOPlatformUUID (ioreg -rd1 -c IOPlatformExpertDevice)
//
// The method is chosen in one place (Compute) so it can change; the same
// value can be computed with a one-line shell command (docs/agents/
// external-agent.md#device-id), which agents use when ark-server is not
// installed.
package hostid

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"sort"
	"strings"
)

const (
	MethodMachineID = "machine-id"
	MethodMAC       = "mac"
	MethodNone      = "none"
)

// ID is a device id and how it was made.
type ID struct {
	ID     string `json:"id"`
	Method string `json:"method"`
}

// FromMachineID hashes a raw OS machine id (surrounding space trimmed).
func FromMachineID(raw string) string {
	return short("ark|" + strings.TrimSpace(raw))
}

// FromMAC hashes a hardware address ("aa:bb:..", lower case).
func FromMAC(mac string) string {
	return short("ark|mac|" + strings.ToLower(strings.TrimSpace(mac)))
}

func short(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// Compute is this device's id.
func Compute() ID {
	if raw := strings.TrimSpace(machineID()); raw != "" {
		return ID{FromMachineID(raw), MethodMachineID}
	}
	if mac := primaryMAC(interfaces()); mac != "" {
		return ID{FromMAC(mac), MethodMAC}
	}
	return ID{"", MethodNone}
}

// Iface is the part of net.Interface PrimaryMAC looks at.
type Iface struct {
	Name  string
	Flags net.Flags
	MAC   net.HardwareAddr
	HasIP bool // a non-loopback unicast address
}

func interfaces() []Iface {
	list, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]Iface, 0, len(list))
	for _, it := range list {
		x := Iface{Name: it.Name, Flags: it.Flags, MAC: it.HardwareAddr}
		if addrs, err := it.Addrs(); err == nil {
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
					x.HasIP = true
				}
			}
		}
		out = append(out, x)
	}
	return out
}

var virtualPrefixes = []string{"docker", "veth", "br-", "virbr", "vmnet", "vboxnet", "vethernet", "virtualbox",
	"tun", "tap", "wsl", "zt", "tailscale", "utun", "awdl", "llw", "bridge", "lo", "hyper-v", "vmware",
	"npcap", "bluetooth", "isatap", "teredo"}

// primaryMAC picks the primary physical adapter: not loopback, not a known
// virtual adapter, a universally administered unicast MAC (randomised and
// locally administered ones change), preferring one that is up with an
// address; ties broken by name so the choice is stable.
func primaryMAC(list []Iface) string {
	type cand struct {
		score int
		name  string
		mac   string
	}
	var cands []cand
	for _, it := range list {
		if it.Flags&net.FlagLoopback != 0 || len(it.MAC) != 6 {
			continue
		}
		if it.MAC[0]&0x02 != 0 || it.MAC[0]&0x01 != 0 { // locally administered or multicast
			continue
		}
		name := strings.ToLower(it.Name)
		virtual := false
		for _, p := range virtualPrefixes {
			if strings.HasPrefix(name, p) || strings.Contains(name, "virtual") {
				virtual = true
				break
			}
		}
		if virtual {
			continue
		}
		score := 0
		if it.Flags&net.FlagUp != 0 {
			score++
		}
		if it.HasIP {
			score += 2
		}
		cands = append(cands, cand{score, name, it.MAC.String()})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].name < cands[j].name
	})
	if len(cands) == 0 {
		return ""
	}
	return cands[0].mac
}
