package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// dropToDatabaseOwner makes a root-run admin command act as the owner of the database's folder,
// so the files it creates stay owned by the user the hub runs as. Other users are left alone.
func dropToDatabaseOwner(database string) error {
	uid, gid, ok := dropTarget(os.Geteuid(), filepath.Dir(database))
	if !ok {
		return nil
	}
	if err := syscall.Setgroups(nil); err != nil {
		return err
	}
	if err := syscall.Setgid(gid); err != nil {
		return err
	}
	return syscall.Setuid(uid)
}

// dropTarget says who a command running as euid should become: the owner of dir, when euid is root
// and dir exists with another owner. A missing folder or a root-owned one means no switch.
func dropTarget(euid int, dir string) (uid, gid int, ok bool) {
	if euid != 0 {
		return 0, 0, false
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return 0, 0, false
	}
	st, isStat := fi.Sys().(*syscall.Stat_t)
	if !isStat || st.Uid == 0 {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
