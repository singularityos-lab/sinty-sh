// SPDX-License-Identifier: GPL-3.0-only

// sinty-sh is the system /bin/sh of SintyOS: a non-interactive POSIX shell
// for scripts, built on the same interpreter as ush.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/singularityos-lab/sinty-sh/internal/expand"
	"golang.org/x/term"
	"mvdan.cc/sh/v3/syntax"

	"github.com/singularityos-lab/sinty-sh/internal/interp"
	"github.com/singularityos-lab/sinty-sh/internal/policy"
)

const defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

type invocation struct {
	opts      []string
	command   string
	hasCmd    bool
	stdin     bool
	script    string
	name      string
	params    []string
	stateFD   int
	parentPID int
	noExec    bool
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: sh [-aefnux] [-o option] [-c command [name [arg ...]] | -s [arg ...] | file [arg ...]]")
	os.Exit(2)
}

func parseArgs(argv []string) invocation {
	inv := invocation{name: argv[0], stateFD: -1}
	args := argv[1:]
	for len(args) > 0 {
		a := args[0]
		if strings.HasPrefix(a, "--sinty-state=") {
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--sinty-state="))
			if err != nil {
				usage()
			}
			inv.stateFD = n
			args = args[1:]
			continue
		}
		if strings.HasPrefix(a, "--sinty-pid=") {
			n, _ := strconv.Atoi(strings.TrimPrefix(a, "--sinty-pid="))
			inv.parentPID = n
			args = args[1:]
			continue
		}
		if a == "--" || a == "-" {
			args = args[1:]
			break
		}
		if len(a) < 2 || (a[0] != '-' && a[0] != '+') {
			break
		}
		args = args[1:]
		on := a[0] == '-'
		for i := 1; i < len(a); i++ {
			c := a[i]
			switch c {
			case 'c':
				inv.hasCmd = on
			case 's':
				inv.stdin = on
			case 'i', 'm', 'b', 'h', 'v', 'C', 'l', 'E', 'p', 'P':
			case 'o':
				if len(args) == 0 {
					usage()
				}
				opt := args[0]
				args = args[1:]
				switch opt {
				case "allexport", "errexit", "noexec", "noglob", "nounset", "xtrace", "pipefail":
					inv.opts = append(inv.opts, string(a[0])+"o", opt)
				}
			case 'a', 'e', 'f', 'u', 'x':
				inv.opts = append(inv.opts, string(a[0])+string(c))
			case 'n':
				inv.noExec = on
			default:
				fmt.Fprintf(os.Stderr, "sh: -%c: invalid option\n", c)
				usage()
			}
		}
	}
	switch {
	case inv.stateFD >= 0:
		if len(args) > 0 {
			inv.name = args[0]
			inv.params = args[1:]
		}
	case inv.hasCmd:
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "sh: -c requires an argument")
			os.Exit(2)
		}
		inv.command = args[0]
		if len(args) > 1 {
			inv.name = args[1]
			inv.params = args[2:]
		}
	case inv.stdin || len(args) == 0:
		inv.stdin = true
		inv.params = args
	default:
		inv.script = args[0]
		inv.name = args[0]
		inv.params = args[1:]
	}
	return inv
}

func main() {
	inv := parseArgs(os.Args)
	pol := policy.Load()
	pol.Environment()
	if os.Getenv("PATH") == "" {
		os.Setenv("PATH", defaultPath)
	}

	self, err := os.Executable()
	if err != nil {
		self = ""
	}
	pid := os.Getpid()
	if inv.parentPID > 0 {
		pid = inv.parentPID
	}
	opts := []interp.RunnerOption{
		interp.Env(expand.ListEnviron(os.Environ()...)),
		interp.StdIO(os.Stdin, os.Stdout, os.Stderr),
		interp.System(self, pid),
		interp.InheritFDs(),
		interp.SourceCheck(pol.CheckFile),
		interp.Params(append(append([]string{}, inv.opts...), append([]string{"--"}, inv.params...)...)...),
	}
	if dir, err := os.Getwd(); err == nil {
		opts = append(opts, interp.Dir(dir))
	}
	r, err := interp.New(opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sh: %v\n", err)
		os.Exit(2)
	}
	ctx := context.Background()
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))

	switch {
	case inv.stateFD >= 0:
		f := os.NewFile(uintptr(inv.stateFD), "state")
		src, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "sh: %v\n", err)
			os.Exit(2)
		}
		os.Exit(runSource(ctx, r, parser, string(src), inv.name, inv.noExec))
	case inv.hasCmd:
		pol.Started(inv.name, "-c")
		os.Exit(runSource(ctx, r, parser, inv.command, inv.name, inv.noExec))
	case inv.stdin:
		os.Exit(runStream(ctx, r, parser, os.Stdin, inv.name, false, inv.noExec))
	default:
		if err := pol.CheckFile(inv.script); err != nil {
			fmt.Fprintf(os.Stderr, "sh: %s: %v\n", inv.script, err)
			os.Exit(126)
		}
		f, err := os.Open(inv.script)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sh: cannot open %s: %v\n", inv.script, notFound(err))
			os.Exit(127)
		}
		pol.Started(inv.script, "")
		os.Exit(runStream(ctx, r, parser, f, inv.name, true, inv.noExec))
	}
}

func notFound(err error) string {
	if os.IsNotExist(err) {
		return "No such file"
	}
	return err.Error()
}

func runSource(ctx context.Context, r *interp.Runner, parser *syntax.Parser, src, name string, noExec bool) int {
	file, err := parser.Parse(strings.NewReader(src), name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		return 2
	}
	if noExec {
		return 0
	}
	r.Begin(ctx, name)
	for i, st := range file.Stmts {
		if r.RunStmt(ctx, st, i == len(file.Stmts)-1) {
			break
		}
	}
	return r.Finish(ctx)
}

// runStream runs a script as it is read, like other shells do, so commands
// before a syntax error still run and a script may read the rest of its own
// input. With lookahead, the statement before the end of a file is known to be
// the last one and may replace the shell.
func runStream(ctx context.Context, r *interp.Runner, parser *syntax.Parser, in io.Reader, name string, lookahead, noExec bool) int {
	r.Begin(ctx, name)
	var held *syntax.Stmt
	stopped := false
	interactive := !lookahead && term.IsTerminal(int(os.Stdin.Fd()))
	reader := io.Reader(in)
	if lookahead {
		reader = bufio.NewReaderSize(in, 64*1024)
	}
	if interactive {
		fmt.Fprint(os.Stderr, prompt())
	}
	err := parser.Stmts(reader, func(st *syntax.Stmt) bool {
		if noExec {
			return true
		}
		if !lookahead {
			stopped = r.RunStmt(ctx, st, false)
			if interactive && !stopped {
				fmt.Fprint(os.Stderr, prompt())
			}
			return !stopped
		}
		if held != nil {
			if stopped = r.RunStmt(ctx, held, false); stopped {
				return false
			}
		}
		held = st
		return true
	})
	if err != nil {
		if held != nil && !stopped {
			r.RunStmt(ctx, held, false)
		}
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		if code := r.Finish(ctx); code != 0 {
			return code
		}
		return 2
	}
	if held != nil && !stopped {
		r.RunStmt(ctx, held, true)
	}
	return r.Finish(ctx)
}

func prompt() string {
	if p := os.Getenv("PS1"); p != "" {
		return p
	}
	if os.Geteuid() == 0 {
		return "# "
	}
	return "$ "
}
