// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

var shellBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sinty-sh-test")
	if err != nil {
		panic(err)
	}
	shellBin = filepath.Join(dir, "sinty-sh")
	build := exec.Command("go", "build", "-o", shellBin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func run(t *testing.T, shell, script string) (string, int) {
	t.Helper()
	cmd := exec.Command(shell, script)
	cmd.Env = append(os.Environ(), "TMPDIR="+t.TempDir(), "SH_UNDER_TEST="+shell)
	var out bytes.Buffer
	cmd.Stdout = &out
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return out.String(), code
	case <-time.After(20 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("%s %s: timed out", shell, script)
	}
	return "", 0
}

// TestCompat runs every script under testdata/compat with dash and with
// sinty-sh and requires the same output and exit status.
func TestCompat(t *testing.T) {
	dash, err := exec.LookPath("dash")
	if err != nil {
		t.Skip("dash is not installed")
	}
	scripts, _ := filepath.Glob("../../testdata/compat/*.sh")
	if len(scripts) == 0 {
		t.Fatal("no compat scripts")
	}
	for _, script := range scripts {
		t.Run(filepath.Base(script), func(t *testing.T) {
			want, wantCode := run(t, dash, script)
			got, gotCode := run(t, shellBin, script)
			if got != want || gotCode != wantCode {
				t.Errorf("dash: status %d\n%s\nsinty-sh: status %d\n%s", wantCode, want, gotCode, got)
			}
		})
	}
}

func TestTailExecKeepsPid(t *testing.T) {
	out, err := exec.Command(shellBin, "-c", `echo $$; exec sh -c 'echo $$'`).Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Fields(out)
	if len(lines) != 2 || !bytes.Equal(lines[0], lines[1]) {
		t.Fatalf("exec did not replace the shell: %q", out)
	}
	out, err = exec.Command(shellBin, "-c", `echo $$; sh -c 'echo $$'`).Output()
	if err != nil {
		t.Fatal(err)
	}
	lines = bytes.Fields(out)
	if len(lines) != 2 || !bytes.Equal(lines[0], lines[1]) {
		t.Fatalf("the last command of -c did not replace the shell: %q", out)
	}
}
