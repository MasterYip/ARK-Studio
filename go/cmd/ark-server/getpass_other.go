//go:build !windows && !linux

package main

func getpass(prompt string) (string, error) { return readStdinLine(prompt) }
