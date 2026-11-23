//go:build !windows && !linux

package cftunnel

import "os/exec"

func prepare(*exec.Cmd) {}

func adopt(*exec.Cmd) {}
