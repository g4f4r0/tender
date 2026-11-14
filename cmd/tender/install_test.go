package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/config"
)

func TestEmailFlags(t *testing.T) {
	if e, err := emailFlags("", "", "", "", 0); e != nil || err != nil {
		t.Errorf("no flags = %v, %v", e, err)
	}
	e, err := emailFlags("a@example.net, b@example.net", "tender <t@example.net>", "smtp.example.net", "api", 465)
	want := &config.Email{Host: "smtp.example.net", Port: 465, Username: "api", From: "tender <t@example.net>", To: []string{"a@example.net", "b@example.net"}}
	if err != nil || !reflect.DeepEqual(e, want) {
		t.Errorf("all flags = %+v, %v", e, err)
	}
	if _, err := emailFlags("a@example.net", "", "", "", 0); err == nil {
		t.Error("a partial set was accepted")
	}
}

func TestReadLine(t *testing.T) {
	for in, want := range map[string]string{"token\n": "token", "token\r\nmore\n": "token", "token": "token", "": ""} {
		if got, err := readLine(strings.NewReader(in)); err != nil || got != want {
			t.Errorf("readLine(%q) = %q, %v", in, got, err)
		}
	}
}

func TestInstallUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		nil, {"bogus"}, {"agent"}, {"agent", "--hub", "https://h.example.net"}, {"hub", "extra"}, {"hub", "--smtp-host", "x"}, {"credential"}, {"credential", "a", "b"},
	} {
		if err := runInstall(args, strings.NewReader(""), &strings.Builder{}); err == nil {
			t.Errorf("runInstall(%v) accepted", args)
		}
	}
}
