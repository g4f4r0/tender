package main

import (
	"testing"

	"github.com/g4f4r0/tender/internal/security"
)

// SSH is weak only when someone can log in with a password: root may log in with a key.
func TestSecurityReportSSH(t *testing.T) {
	for _, c := range []struct {
		passwords bool
		root      string
		weak      bool
		text      string
	}{
		{false, "prohibit-password", false, ""},
		{false, "yes", false, ""},
		{true, "prohibit-password", true, "allows password login (PasswordAuthentication yes)"},
		{true, "yes", true, "allows password login (PasswordAuthentication yes) and lets root log in with a password (PermitRootLogin yes)"},
	} {
		r := security.Report{SSH: security.SSH{Check: security.CheckOK, Installed: true, PasswordAuthentication: &c.passwords, PermitRootLogin: &c.root}}
		got := securityReport(r)
		if got.SSHPasswords != c.weak || got.SSH != c.text {
			t.Errorf("passwords %v, root %s: weak %v %q, want %v %q", c.passwords, c.root, got.SSHPasswords, got.SSH, c.weak, c.text)
		}
	}
}
