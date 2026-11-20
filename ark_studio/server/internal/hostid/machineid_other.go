//go:build !linux && !windows && !darwin

package hostid

func machineID() string { return "" }
