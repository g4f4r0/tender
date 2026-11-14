package main

import (
	"os"
	"strings"
	"testing"
)

func TestReadLine(t *testing.T) {
	for in, want := range map[string]string{"token\n": "token", "token\r\nmore\n": "token", "token": "token", "": ""} {
		if got, err := readLine(strings.NewReader(in)); err != nil || got != want {
			t.Errorf("readLine(%q) = %q, %v", in, got, err)
		}
	}
}

func TestInstallUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		nil, {"bogus"}, {"agent"}, {"agent", "--hub", "https://h.example.net"}, {"hub", "extra"}, {"refresh", "extra"},
		{"credential", "r2.password"}, // removed: the hub keeps the destination's secrets now
		{"hub", "--smtp-port", "70000"}, {"hub", "--smtp-password-file", "x"},
	} {
		if err := runInstall(args, strings.NewReader(""), session{out: &strings.Builder{}}); err == nil {
			t.Errorf("runInstall(%v) accepted", args)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	if isTerminal(strings.NewReader("token\n")) {
		t.Error("a reader is not a terminal")
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = null.Close() }()
	if !isTerminal(null) {
		t.Error("/dev/null should count as no input")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if isTerminal(r) {
		t.Error("a pipe is not a terminal")
	}
}
