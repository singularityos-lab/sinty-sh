// SPDX-License-Identifier: GPL-3.0-only

// Package policy holds the rules sinty-sh applies to scripts that run as
// root. They only tighten what a script may inherit or read; a script that
// follows them behaves exactly as under any other POSIX shell.
package policy

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// inheritedLoader lists the dynamic loader variables a root script must not
// pick up from whoever started it. A script may still set them itself.
var inheritedLoader = []string{"LD_PRELOAD", "LD_AUDIT", "LD_LIBRARY_PATH"}

// Policy is the set of rules for the current process.
type Policy struct {
	root    bool
	service bool
}

// Load decides which rules apply: the root rules when the shell runs with
// root privileges, and service logging when PID 1 started it.
func Load() *Policy {
	return &Policy{
		root:    os.Geteuid() == 0,
		service: os.Getppid() == 1,
	}
}

// Environment drops the loader variables a root shell inherited.
func (p *Policy) Environment() {
	if !p.root {
		return
	}
	for _, name := range inheritedLoader {
		os.Unsetenv(name)
	}
}

// CheckFile refuses a script or sourced file that someone other than root
// could have changed, when the shell runs as root.
func (p *Policy) CheckFile(path string) error {
	if !p.root {
		return nil
	}
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return nil
		}
		return err
	}
	if st.Uid != 0 {
		return fmt.Errorf("refusing to run as root: owned by uid %d", st.Uid)
	}
	if st.Mode&0o022 != 0 {
		return errors.New("refusing to run as root: writable by group or others")
	}
	return nil
}

// Started records a script that a service runs as root in the kernel log, at
// debug level, so a boot log shows which shell scripts ran.
func (p *Policy) Started(name, how string) {
	if !p.root || !p.service {
		return
	}
	f, err := os.OpenFile("/dev/kmsg", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	msg := name
	if how != "" {
		msg = how + " " + name
	}
	msg = strings.Map(func(r rune) rune {
		if r < 0x20 {
			return ' '
		}
		return r
	}, msg)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	fmt.Fprintf(f, "<7>sinty-sh[%d]: %s\n", os.Getpid(), msg)
}
