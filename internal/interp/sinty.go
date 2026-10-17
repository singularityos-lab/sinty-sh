// SPDX-License-Identifier: GPL-3.0-only

package interp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/singularityos-lab/sinty-sh/internal/expand"
	"golang.org/x/sys/unix"
	"mvdan.cc/sh/v3/syntax"
)

// shellSystem is the state a system shell shares between the main runner and
// its subshells: how to start real child shells, the shell's own pid and the
// signal traps.
type shellSystem struct {
	self        string
	pid         int
	sourceCheck func(path string) error

	mu      sync.Mutex
	traps   map[syscall.Signal]string
	pending []syscall.Signal
	wake    chan struct{}
	sigCh   chan os.Signal
}

// System makes the runner behave as a process-level shell. Background jobs
// run as real child processes of self, so $! is a real pid that kill and wait
// understand, exec replaces the process, and trap handles signals.
func System(self string, pid int) RunnerOption {
	return func(r *Runner) error {
		r.sys = &shellSystem{
			self:  self,
			pid:   pid,
			traps: map[syscall.Signal]string{},
			wake:  make(chan struct{}, 1),
		}
		r.trapOwner = true
		return nil
	}
}

// SourceCheck sets a function that approves every file the shell reads
// commands from with "." or source. A non-nil error refuses the file.
func SourceCheck(check func(path string) error) RunnerOption {
	return func(r *Runner) error {
		if r.sys == nil {
			return errors.New("SourceCheck needs System")
		}
		r.sys.sourceCheck = check
		return nil
	}
}

// RunTail runs a whole program like Run. When the last statement is a simple
// command for an external program, and no trap would need the shell after it,
// the shell replaces itself with that program instead of waiting for it.
func (r *Runner) RunTail(ctx context.Context, f *syntax.File) error {
	r.tailStmt = nil
	if n := len(f.Stmts); n > 0 {
		st := f.Stmts[n-1]
		if _, ok := st.Cmd.(*syntax.CallExpr); ok && !st.Background && !st.Disown && !st.Negated && !st.Coprocess {
			r.tailStmt = st
		}
	}
	return r.Run(ctx, f)
}

func (r *Runner) tailAllowed() bool {
	if r.sys == nil || !r.trapOwner || r.callbackExit != "" || r.callbackErr != "" {
		return false
	}
	r.sys.mu.Lock()
	defer r.sys.mu.Unlock()
	for _, code := range r.sys.traps {
		if code != "" {
			return false
		}
	}
	return true
}

func (r *Runner) shellPID() int {
	if r.sys != nil && r.sys.pid > 0 {
		return r.sys.pid
	}
	return os.Getpid()
}

func redirectsStdin(st *syntax.Stmt) bool {
	for _, rd := range st.Redirs {
		fd := ""
		if rd.N != nil {
			fd = rd.N.Value
		}
		switch rd.Op {
		case syntax.RdrIn, syntax.RdrInOut, syntax.DplIn, syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
			if fd == "" || fd == "0" {
				return true
			}
		case syntax.DplOut, syntax.RdrOut, syntax.AppOut, syntax.RdrClob:
			if fd == "0" {
				return true
			}
		}
	}
	return false
}

// startBackground runs an asynchronous list as a real child shell. The child
// gets the variables, functions, options and positional parameters through a
// pipe, so nothing private lands on its command line.
func (r *Runner) startBackground(ctx context.Context, st *syntax.Stmt) (bgProc, error) {
	var script strings.Builder
	if err := r.writeState(&script); err != nil {
		return bgProc{}, err
	}
	st2 := *st
	st2.Background = false
	st2.Disown = false
	if err := syntax.NewPrinter().Print(&script, &st2); err != nil {
		return bgProc{}, err
	}
	script.WriteByte('\n')

	pr, pw, err := os.Pipe()
	if err != nil {
		return bgProc{}, err
	}
	var stdin io.Reader = r.stdin
	if !redirectsStdin(st) {
		devnull, err := os.Open(os.DevNull)
		if err != nil {
			pr.Close()
			pw.Close()
			return bgProc{}, err
		}
		defer devnull.Close()
		stdin = devnull
	}
	name := r.filename
	if name == "" {
		name = "sh"
	}
	cmd := &exec.Cmd{
		Path:       r.sys.self,
		Args:       []string{name, "--sinty-state=10", "--sinty-pid=" + strconv.Itoa(r.shellPID()), "--", name},
		Env:        execEnv(r.writeEnv),
		Dir:        r.Dir,
		Stdin:      stdin,
		Stdout:     r.stdout,
		Stderr:     r.stderr,
		ExtraFiles: r.backgroundFiles(pr),
	}
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return bgProc{}, err
	}
	pr.Close()
	go func() {
		io.WriteString(pw, script.String())
		pw.Close()
	}()
	bg := bgProc{
		done: make(chan struct{}),
		exit: new(exitStatus),
		pid:  cmd.Process.Pid,
	}
	go func() {
		err := cmd.Wait()
		code := uint8(0)
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = uint8(128 + int(ws.Signal()))
			} else {
				code = uint8(ee.ExitCode())
			}
		} else if err != nil {
			code = 127
		}
		bg.exit.code = code
		close(bg.done)
	}()
	return bg, nil
}

func (r *Runner) backgroundFiles(state *os.File) []*os.File {
	files := make([]*os.File, 8)
	for n := 3; n <= 9; n++ {
		if f, ok := r.fds[n].(*os.File); ok {
			files[n-3] = f
		}
	}
	files[7] = state
	return files
}

func quoteShell(s string) string {
	q, err := syntax.Quote(s, syntax.LangBash)
	if err != nil {
		return "''"
	}
	return q
}

// writeState writes the shell state a child shell starts from, as shell
// source the child runs before the job itself.
func (r *Runner) writeState(w *strings.Builder) error {
	var exports, readonly []string
	for name, vr := range r.writeEnv.Each {
		if !vr.IsSet() || !syntax.ValidName(name) || internalVars[name] {
			continue
		}
		switch vr.Kind {
		case expand.String:
			fmt.Fprintf(w, "%s=%s\n", name, quoteShell(vr.Str))
		case expand.Indexed:
			w.WriteString(name + "=(")
			for i, v := range vr.List {
				if i > 0 {
					w.WriteByte(' ')
				}
				w.WriteString(quoteShell(v))
			}
			w.WriteString(")\n")
		case expand.Associative:
			fmt.Fprintf(w, "declare -A %s=(", name)
			keys := make([]string, 0, len(vr.Map))
			for k := range vr.Map {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for i, k := range keys {
				if i > 0 {
					w.WriteByte(' ')
				}
				fmt.Fprintf(w, "[%s]=%s", quoteShell(k), quoteShell(vr.Map[k]))
			}
			w.WriteString(")\n")
		case expand.NameRef:
			fmt.Fprintf(w, "declare -n %s=%s\n", name, quoteShell(vr.Str))
		default:
			continue
		}
		if vr.Exported {
			exports = append(exports, name)
		}
		if vr.ReadOnly {
			readonly = append(readonly, name)
		}
	}
	if len(exports) > 0 {
		w.WriteString("export " + strings.Join(exports, " ") + "\n")
	}
	printer := syntax.NewPrinter()
	names := make([]string, 0, len(r.Funcs))
	for name := range r.Funcs {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		w.WriteString(name + "() ")
		if err := printer.Print(w, r.Funcs[name]); err != nil {
			return err
		}
		w.WriteByte('\n')
	}
	for i, opt := range &posixOptsTable {
		if r.opts[i] {
			w.WriteString("set -o " + opt.name + "\n")
		}
	}
	for i, opt := range bashOptsTable {
		if opt.supported && r.opts[len(posixOptsTable)+i] != opt.defaultState {
			if r.opts[len(posixOptsTable)+i] {
				w.WriteString("shopt -s " + opt.name + "\n")
			} else {
				w.WriteString("shopt -u " + opt.name + "\n")
			}
		}
	}
	w.WriteString("set --")
	for _, p := range r.Params {
		w.WriteString(" " + quoteShell(p))
	}
	w.WriteByte('\n')
	if r.sys != nil {
		r.sys.mu.Lock()
		for sig, code := range r.sys.traps {
			if code == "" {
				fmt.Fprintf(w, "trap '' %d\n", int(sig))
			}
		}
		r.sys.mu.Unlock()
	}
	if len(readonly) > 0 {
		w.WriteString("readonly " + strings.Join(readonly, " ") + "\n")
	}
	return nil
}

// replaceProcess replaces the shell with a program, as exec does. When the
// shell's input or output is not a real file the program cannot inherit it,
// so the program runs as a child and the shell exits with its status.
func (r *Runner) replaceProcess(ctx context.Context, pos syntax.Pos, args []string) exitStatus {
	var exit exitStatus
	exit.exiting = true
	path, err := LookPathDir(r.Dir, r.writeEnv, args[0])
	if err != nil {
		r.errf("%s: %s: not found\n", r.shellName(), args[0])
		exit.code = 127
		return exit
	}
	in, inOK := asFile(r.stdin)
	out, outOK := r.stdout.(*os.File)
	errf, errOK := r.stderr.(*os.File)
	if r.stdin == nil {
		in, inOK = nil, true
	}
	if !inOK || !outOK || !errOK {
		r.exec(ctx, pos, args)
		exit.code = r.exit.code
		return exit
	}
	env := execEnv(r.writeEnv)
	if err := installStdio(in, out, errf, r.fds); err != nil {
		r.errf("%s: exec: %v\n", r.shellName(), err)
		exit.code = 126
		return exit
	}
	if err := unix.Chdir(r.Dir); err != nil {
		r.errf("%s: exec: %v\n", r.shellName(), err)
		exit.code = 126
		return exit
	}
	signal.Reset()
	err = unix.Exec(path, args, env)
	if errors.Is(err, unix.ENOEXEC) && r.sys != nil && r.sys.self != "" {
		err = unix.Exec(r.sys.self, append([]string{args[0], path}, args[1:]...), env)
	}
	r.errf("%s: exec: %s: %v\n", r.shellName(), args[0], err)
	exit.code = 126
	return exit
}

func (r *Runner) shellName() string {
	if r.filename != "" {
		return r.filename
	}
	return "sh"
}

func asFile(rd io.Reader) (*os.File, bool) {
	f, ok := rd.(*os.File)
	return f, ok
}

// installStdio places the shell's current descriptors at 0 to 9 for a
// program that replaces the shell. Every source is copied above 9 first, so
// moving one descriptor cannot clobber another.
func installStdio(in, out, errf *os.File, fds [10]any) error {
	src := [10]int{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
	files := [10]*os.File{in, out, errf}
	for n := 3; n <= 9; n++ {
		if f, ok := fds[n].(*os.File); ok {
			files[n] = f
		}
	}
	for n, f := range files {
		if f == nil {
			if n > 2 {
				continue
			}
			nf, err := os.Open(os.DevNull)
			if err != nil {
				return err
			}
			f = nf
		}
		fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 10)
		if err != nil {
			return err
		}
		src[n] = fd
	}
	for n, fd := range src {
		if fd < 0 {
			continue
		}
		if err := unix.Dup3(fd, n, 0); err != nil {
			return err
		}
	}
	return nil
}

var internalVars = map[string]bool{"UID": true, "EUID": true, "GID": true, "PPID": true}

var signalNames = map[string]syscall.Signal{
	"HUP": syscall.SIGHUP, "INT": syscall.SIGINT, "QUIT": syscall.SIGQUIT,
	"ILL": syscall.SIGILL, "TRAP": syscall.SIGTRAP, "ABRT": syscall.SIGABRT,
	"BUS": syscall.SIGBUS, "FPE": syscall.SIGFPE, "KILL": syscall.SIGKILL,
	"USR1": syscall.SIGUSR1, "SEGV": syscall.SIGSEGV, "USR2": syscall.SIGUSR2,
	"PIPE": syscall.SIGPIPE, "ALRM": syscall.SIGALRM, "TERM": syscall.SIGTERM,
	"CHLD": syscall.SIGCHLD, "CONT": syscall.SIGCONT, "STOP": syscall.SIGSTOP,
	"TSTP": syscall.SIGTSTP, "TTIN": syscall.SIGTTIN, "TTOU": syscall.SIGTTOU,
	"URG": syscall.SIGURG, "XCPU": syscall.SIGXCPU, "XFSZ": syscall.SIGXFSZ,
	"VTALRM": syscall.SIGVTALRM, "PROF": syscall.SIGPROF, "WINCH": syscall.SIGWINCH,
	"IO": syscall.SIGIO, "PWR": syscall.SIGPWR, "SYS": syscall.SIGSYS,
}

func signalName(sig syscall.Signal) string {
	for name, s := range signalNames {
		if s == sig {
			return name
		}
	}
	return strconv.Itoa(int(sig))
}

func parseSignal(s string) (syscall.Signal, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		if n > 0 && n < 65 {
			return syscall.Signal(n), true
		}
		return 0, false
	}
	s = strings.ToUpper(strings.TrimPrefix(strings.ToUpper(s), "SIG"))
	if s == "CLD" {
		s = "CHLD"
	}
	if s == "POLL" {
		s = "IO"
	}
	sig, ok := signalNames[s]
	return sig, ok
}

func (r *Runner) setSignalTrap(sig syscall.Signal, code string, reset bool) {
	if r.sys == nil || !r.trapOwner {
		return
	}
	r.sys.mu.Lock()
	defer r.sys.mu.Unlock()
	if r.sys.sigCh == nil {
		r.sys.sigCh = make(chan os.Signal, 16)
		go r.sys.collect()
	}
	switch {
	case reset:
		delete(r.sys.traps, sig)
		signal.Reset(sig)
	case code == "":
		r.sys.traps[sig] = ""
		signal.Ignore(sig)
	default:
		r.sys.traps[sig] = code
		signal.Notify(r.sys.sigCh, sig)
	}
}

func (s *shellSystem) collect() {
	for sig := range s.sigCh {
		ss, ok := sig.(syscall.Signal)
		if !ok {
			continue
		}
		s.mu.Lock()
		s.pending = append(s.pending, ss)
		s.mu.Unlock()
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

func (s *shellSystem) hasPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending) > 0
}

// runPendingTraps runs the traps of the signals that arrived since the last
// command. $? is kept across a trap unless the trap exits the shell.
func (r *Runner) runPendingTraps(ctx context.Context) {
	if r.sys == nil || !r.trapOwner || r.handlingTrap {
		return
	}
	r.sys.mu.Lock()
	pending := r.sys.pending
	r.sys.pending = nil
	codes := make([]string, 0, len(pending))
	for _, sig := range pending {
		codes = append(codes, r.sys.traps[sig])
	}
	r.sys.mu.Unlock()
	for _, code := range codes {
		if code == "" {
			continue
		}
		file, err := syntax.NewParser().Parse(strings.NewReader(code), "trap")
		if err != nil {
			r.errf("trap: %v\n", err)
			continue
		}
		oldExit, oldLast := r.exit, r.lastExit
		r.handlingTrap = true
		r.stmts(ctx, file.Stmts)
		r.handlingTrap = false
		if r.exit.exiting {
			return
		}
		r.exit, r.lastExit = oldExit, oldLast
	}
}

// waitJob waits for a background job. It gives up early when a trapped signal
// arrives, as POSIX wait does, and reports that signal.
func (r *Runner) waitJob(bg bgProc) (exitStatus, syscall.Signal) {
	if r.sys == nil || !r.trapOwner {
		<-bg.done
		return *bg.exit, 0
	}
	for {
		select {
		case <-bg.done:
			return *bg.exit, 0
		case <-r.sys.wake:
			r.sys.mu.Lock()
			var sig syscall.Signal
			if len(r.sys.pending) > 0 {
				sig = r.sys.pending[len(r.sys.pending)-1]
			}
			r.sys.mu.Unlock()
			if sig != 0 {
				return exitStatus{code: uint8(128 + int(sig))}, sig
			}
		}
	}
}

func (r *Runner) builtinWait(args []string) exitStatus {
	var exit exitStatus
	if len(args) == 0 {
		for _, bg := range r.bgProcs {
			if st, sig := r.waitJob(bg); sig != 0 {
				return st
			}
		}
		return exit
	}
	for _, arg := range args {
		var job *bgProc
		if n, ok := strings.CutPrefix(arg, "g"); ok {
			if i := atoi(n); i > 0 && i <= int64(len(r.bgProcs)) {
				job = &r.bgProcs[i-1]
			}
		} else if pid := atoi(arg); pid > 0 {
			for i := range r.bgProcs {
				if r.bgProcs[i].pid == int(pid) {
					job = &r.bgProcs[i]
				}
			}
		}
		if job == nil {
			r.errf("wait: pid %s is not a child of this shell\n", arg)
			exit.code = 127
			continue
		}
		st, sig := r.waitJob(*job)
		exit.code = st.code
		if sig != 0 {
			return exit
		}
	}
	return exit
}

func (r *Runner) builtinTrap(args []string) exitStatus {
	var exit exitStatus
	if len(args) > 0 && (args[0] == "-l") {
		for i := 1; i < 32; i++ {
			r.outf("%d) SIG%s\n", i, signalName(syscall.Signal(i)))
		}
		return exit
	}
	if len(args) > 0 && args[0] == "-p" {
		args = args[1:]
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		if r.callbackExit != "" {
			r.outf("trap -- %s EXIT\n", quoteShell(r.callbackExit))
		}
		if r.callbackErr != "" {
			r.outf("trap -- %s ERR\n", quoteShell(r.callbackErr))
		}
		if r.sys != nil {
			r.sys.mu.Lock()
			sigs := make([]int, 0, len(r.sys.traps))
			for sig := range r.sys.traps {
				sigs = append(sigs, int(sig))
			}
			slices.Sort(sigs)
			for _, sig := range sigs {
				r.outf("trap -- %s %s\n", quoteShell(r.sys.traps[syscall.Signal(sig)]), signalName(syscall.Signal(sig)))
			}
			r.sys.mu.Unlock()
		}
		return exit
	}
	callback, reset := "", false
	conds := args
	if _, err := strconv.Atoi(args[0]); err != nil || len(args) > 1 {
		if _, isNum := strconv.Atoi(args[0]); isNum != nil || len(args) > 1 {
			callback, conds = args[0], args[1:]
		}
	}
	if _, err := strconv.Atoi(args[0]); err == nil {
		callback, conds, reset = "", args, true
	}
	if callback == "-" {
		callback, reset = "", true
	}
	if len(conds) == 0 {
		conds, callback, reset = args, "", true
	}
	for _, cond := range conds {
		switch strings.ToUpper(strings.TrimPrefix(strings.ToUpper(cond), "SIG")) {
		case "EXIT", "0":
			r.callbackExit = callback
			continue
		case "ERR":
			r.callbackErr = callback
			continue
		case "DEBUG", "RETURN":
			continue
		}
		sig, ok := parseSignal(cond)
		if !ok || sig == syscall.SIGKILL || sig == syscall.SIGSTOP {
			r.errf("trap: %s: invalid signal specification\n", cond)
			exit.code = 1
			continue
		}
		r.setSignalTrap(sig, callback, reset)
	}
	return exit
}

func (r *Runner) builtinKill(args []string) exitStatus {
	var exit exitStatus
	sig := syscall.SIGTERM
	if len(args) == 0 {
		r.errf("kill: usage: kill [-s sigspec | -signum | -sigspec] pid ...\n")
		exit.code = 2
		return exit
	}
	switch {
	case args[0] == "-l" || args[0] == "-L":
		if len(args) > 1 {
			for _, a := range args[1:] {
				n := atoi(a)
				if n > 128 {
					n -= 128
				}
				r.outf("%s\n", signalName(syscall.Signal(n)))
			}
			return exit
		}
		for i := 1; i < 32; i++ {
			r.outf("%s\n", signalName(syscall.Signal(i)))
		}
		return exit
	case args[0] == "-s" || args[0] == "-n":
		if len(args) < 2 {
			r.errf("kill: %s: option requires an argument\n", args[0])
			exit.code = 2
			return exit
		}
		s, ok := parseSignal(args[1])
		if !ok && args[1] != "0" {
			r.errf("kill: %s: invalid signal specification\n", args[1])
			exit.code = 1
			return exit
		}
		sig, args = s, args[2:]
	case args[0] == "--":
		args = args[1:]
	case strings.HasPrefix(args[0], "-") && len(args[0]) > 1 && !isNegativeNumber(args[0], len(args)):
		spec := args[0][1:]
		if spec == "0" {
			sig = 0
		} else {
			s, ok := parseSignal(spec)
			if !ok {
				r.errf("kill: %s: invalid signal specification\n", spec)
				exit.code = 1
				return exit
			}
			sig = s
		}
		args = args[1:]
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	for _, a := range args {
		if strings.HasPrefix(a, "%") {
			r.errf("kill: %s: no job control in this shell\n", a)
			exit.code = 1
			continue
		}
		var pid int
		if n, ok := strings.CutPrefix(a, "g"); ok {
			if i := atoi(n); i > 0 && i <= int64(len(r.bgProcs)) {
				pid = r.bgProcs[i-1].pid
			}
		} else {
			p, err := strconv.Atoi(a)
			if err != nil {
				r.errf("kill: %s: arguments must be process or job IDs\n", a)
				exit.code = 1
				continue
			}
			pid = p
		}
		if pid == 0 && a != "0" {
			r.errf("kill: %s: no such process\n", a)
			exit.code = 1
			continue
		}
		if err := unix.Kill(pid, sig); err != nil {
			r.errf("kill: (%s) - %v\n", a, err)
			exit.code = 1
			continue
		}
		if pid == os.Getpid() || pid == 0 || pid == -1 || pid == -unix.Getpgrp() {
			r.awaitOwnSignal(sig)
		}
	}
	return exit
}

// awaitOwnSignal waits for a trapped signal the shell sent to itself to be
// delivered, so its trap runs before the next command as in other shells.
func (r *Runner) awaitOwnSignal(sig syscall.Signal) {
	if r.sys == nil {
		return
	}
	r.sys.mu.Lock()
	code := r.sys.traps[sig]
	r.sys.mu.Unlock()
	if code == "" {
		return
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		r.sys.mu.Lock()
		got := slices.Contains(r.sys.pending, sig)
		r.sys.mu.Unlock()
		if got {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func isNegativeNumber(s string, n int) bool {
	if n < 2 {
		_, err := strconv.Atoi(s)
		return err == nil
	}
	return false
}

func (r *Runner) builtinUmask(args []string) exitStatus {
	var exit exitStatus
	symbolic := false
	if len(args) > 0 && args[0] == "-S" {
		symbolic, args = true, args[1:]
	}
	old := unix.Umask(0)
	unix.Umask(old)
	if len(args) == 0 {
		if symbolic {
			r.outf("%s\n", symbolicMask(old))
		} else {
			r.outf("%04o\n", old)
		}
		return exit
	}
	if n, err := strconv.ParseUint(args[0], 8, 32); err == nil {
		unix.Umask(int(n & 0o777))
		return exit
	}
	mask, err := parseSymbolicMask(args[0], old)
	if err != nil {
		r.errf("umask: %s: invalid symbolic mode\n", args[0])
		exit.code = 1
		return exit
	}
	unix.Umask(mask)
	return exit
}

func symbolicMask(mask int) string {
	perm := ^mask & 0o777
	part := func(shift int) string {
		var b strings.Builder
		if perm>>shift&4 != 0 {
			b.WriteByte('r')
		}
		if perm>>shift&2 != 0 {
			b.WriteByte('w')
		}
		if perm>>shift&1 != 0 {
			b.WriteByte('x')
		}
		return b.String()
	}
	return "u=" + part(6) + ",g=" + part(3) + ",o=" + part(0)
}

func parseSymbolicMask(spec string, old int) (int, error) {
	perm := ^old & 0o777
	for _, clause := range strings.Split(spec, ",") {
		i := 0
		who := 0
		for i < len(clause) && strings.IndexByte("ugoa", clause[i]) >= 0 {
			switch clause[i] {
			case 'u':
				who |= 0o700
			case 'g':
				who |= 0o070
			case 'o':
				who |= 0o007
			case 'a':
				who |= 0o777
			}
			i++
		}
		if who == 0 {
			who = 0o777
		}
		if i >= len(clause) || strings.IndexByte("+-=", clause[i]) < 0 {
			return 0, errors.New("bad mode")
		}
		op := clause[i]
		bits := 0
		for _, c := range clause[i+1:] {
			switch c {
			case 'r':
				bits |= 0o444
			case 'w':
				bits |= 0o222
			case 'x':
				bits |= 0o111
			default:
				return 0, errors.New("bad mode")
			}
		}
		bits &= who
		switch op {
		case '+':
			perm |= bits
		case '-':
			perm &^= bits
		case '=':
			perm = perm&^who | bits
		}
	}
	return ^perm & 0o777, nil
}

var rlimits = []struct {
	flag     byte
	resource int
	unit     uint64
	name     string
}{
	{'c', unix.RLIMIT_CORE, 512, "core file size (blocks)"},
	{'d', unix.RLIMIT_DATA, 1024, "data seg size (kbytes)"},
	{'f', unix.RLIMIT_FSIZE, 512, "file size (blocks)"},
	{'l', unix.RLIMIT_MEMLOCK, 1024, "max locked memory (kbytes)"},
	{'m', unix.RLIMIT_RSS, 1024, "max memory size (kbytes)"},
	{'n', unix.RLIMIT_NOFILE, 1, "open files"},
	{'s', unix.RLIMIT_STACK, 1024, "stack size (kbytes)"},
	{'t', unix.RLIMIT_CPU, 1, "cpu time (seconds)"},
	{'u', unix.RLIMIT_NPROC, 1, "max user processes"},
	{'v', unix.RLIMIT_AS, 1024, "virtual memory (kbytes)"},
}

func (r *Runner) builtinUlimit(args []string) exitStatus {
	var exit exitStatus
	hard, soft, all := false, false, false
	res := byte('f')
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && len(args[0]) > 1 {
		for _, c := range args[0][1:] {
			switch c {
			case 'H':
				hard = true
			case 'S':
				soft = true
			case 'a':
				all = true
			default:
				res = byte(c)
			}
		}
		args = args[1:]
	}
	if !hard && !soft {
		soft = len(args) == 0
		if len(args) > 0 {
			hard, soft = true, true
		}
	}
	show := func(lim uint64, unit uint64) string {
		if lim == unix.RLIM_INFINITY {
			return "unlimited"
		}
		return strconv.FormatUint(lim/unit, 10)
	}
	if all {
		for _, l := range rlimits {
			var rl unix.Rlimit
			unix.Getrlimit(l.resource, &rl)
			cur := rl.Cur
			if hard && !soft {
				cur = rl.Max
			}
			r.outf("-%c: %-32s %s\n", l.flag, l.name, show(cur, l.unit))
		}
		return exit
	}
	idx := slices.IndexFunc(rlimits, func(l struct {
		flag     byte
		resource int
		unit     uint64
		name     string
	}) bool {
		return l.flag == res
	})
	if idx < 0 {
		r.errf("ulimit: -%c: invalid option\n", res)
		exit.code = 2
		return exit
	}
	l := rlimits[idx]
	var rl unix.Rlimit
	if err := unix.Getrlimit(l.resource, &rl); err != nil {
		r.errf("ulimit: %v\n", err)
		exit.code = 1
		return exit
	}
	if len(args) == 0 {
		if hard && !soft {
			r.outf("%s\n", show(rl.Max, l.unit))
		} else {
			r.outf("%s\n", show(rl.Cur, l.unit))
		}
		return exit
	}
	var v uint64
	if args[0] == "unlimited" {
		v = unix.RLIM_INFINITY
	} else {
		n, err := strconv.ParseUint(args[0], 10, 64)
		if err != nil {
			r.errf("ulimit: %s: invalid number\n", args[0])
			exit.code = 1
			return exit
		}
		v = n * l.unit
	}
	if soft {
		rl.Cur = v
	}
	if hard {
		rl.Max = v
	}
	if err := unix.Setrlimit(l.resource, &rl); err != nil {
		r.errf("ulimit: %v\n", err)
		exit.code = 1
	}
	return exit
}

func (r *Runner) builtinTimes() exitStatus {
	var t unix.Tms
	unix.Times(&t)
	tick := float64(100)
	f := func(v int64) string {
		s := float64(v) / tick
		return fmt.Sprintf("%dm%.3fs", int(s)/60, s-float64(int(s)/60*60))
	}
	r.outf("%s %s\n%s %s\n", f(t.Utime), f(t.Stime), f(t.Cutime), f(t.Cstime))
	return exitStatus{}
}

// Begin prepares the runner to run a program statement by statement under
// the given $0, as a shell reading a script does.
func (r *Runner) Begin(ctx context.Context, name string) {
	if !r.didReset {
		r.Reset()
	}
	r.filename = name
	r.fillExpandConfig(ctx)
}

// RunStmt runs one statement of the program started with Begin. When tail is
// set and the statement is a simple command for an external program, the
// shell is replaced by it. It reports whether the shell should stop.
func (r *Runner) RunStmt(ctx context.Context, st *syntax.Stmt, tail bool) bool {
	r.exit = exitStatus{}
	r.tailStmt = nil
	if tail {
		if _, ok := st.Cmd.(*syntax.CallExpr); ok && !st.Background && !st.Disown && !st.Negated && !st.Coprocess {
			r.tailStmt = st
		}
	}
	r.stmt(ctx, st)
	r.tailStmt = nil
	return r.exit.exiting || r.exit.fatalExit
}

// Finish runs the traps still pending and the EXIT trap, and returns the
// status the shell exits with. An exit inside the EXIT trap sets the status.
func (r *Runner) Finish(ctx context.Context) int {
	r.runPendingTraps(ctx)
	code := int(r.exit.code)
	if r.exit.err != nil && code == 0 {
		code = 1
	}
	callback := r.callbackExit
	r.callbackExit = ""
	if callback == "" || r.handlingTrap {
		return code
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(callback), "exit trap")
	if err != nil {
		r.errf("trap: %v\n", err)
		return code
	}
	r.exit = exitStatus{code: uint8(code)}
	r.lastExit = r.exit
	r.handlingTrap = true
	r.stmts(ctx, file.Stmts)
	r.handlingTrap = false
	if r.exit.exiting {
		return int(r.exit.code)
	}
	return code
}

// InheritFDs makes the descriptors 3 to 9 the shell was started with
// available to redirections and to the commands it runs.
func InheritFDs() RunnerOption {
	return func(r *Runner) error {
		for fd := 3; fd <= 9; fd++ {
			flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
			if err != nil || flags&unix.FD_CLOEXEC != 0 {
				continue
			}
			r.fds[fd] = os.NewFile(uintptr(fd), "fd"+strconv.Itoa(fd))
		}
		return nil
	}
}

func (r *Runner) getFD(n int) any {
	switch n {
	case 0:
		if r.stdin == nil {
			return nil
		}
		return r.stdin
	case 1:
		return r.stdout
	case 2:
		return r.stderr
	}
	return r.fds[n]
}

func (r *Runner) setFD(n int, v any) error {
	switch n {
	case 0:
		switch v := v.(type) {
		case nil:
			r.stdin = nil
		case *os.File:
			r.stdin = v
		case io.Reader:
			f, err := stdinFile(v)
			if err != nil {
				return err
			}
			r.stdin = f
		default:
			return errors.New("descriptor is not readable")
		}
	case 1, 2:
		w, ok := v.(io.Writer)
		if v == nil {
			w, ok = io.Discard, true
		}
		if !ok {
			return errors.New("descriptor is not writable")
		}
		if n == 1 {
			r.stdout = w
		} else {
			r.stderr = w
		}
	default:
		r.fds[n] = v
	}
	return nil
}

// extraFiles lists descriptors 3 to 9 for a child process; descriptors the
// shell has closed or that are not files stay closed in the child.
func (r *Runner) extraFiles() []*os.File {
	last := 0
	for n := 3; n <= 9; n++ {
		if _, ok := r.fds[n].(*os.File); ok {
			last = n
		}
	}
	if last == 0 {
		return nil
	}
	files := make([]*os.File, last-2)
	for n := 3; n <= last; n++ {
		if f, ok := r.fds[n].(*os.File); ok {
			files[n-3] = f
		}
	}
	return files
}

func (r *Runner) redir(ctx context.Context, rd *syntax.Redirect) (io.Closer, error) {
	input := false
	switch rd.Op {
	case syntax.RdrIn, syntax.RdrInOut, syntax.DplIn, syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
		input = true
	}
	n := 1
	if input {
		n = 0
	}
	if rd.N != nil {
		v, err := strconv.Atoi(rd.N.Value)
		if err != nil || v < 0 || v > 9 {
			r.errf("%s: %s: bad file descriptor\n", r.shellName(), rd.N.Value)
			return nil, errors.New("bad fd")
		}
		n = v
	}
	if rd.Hdoc != nil {
		pr, err := r.hdocReader(rd)
		if err != nil {
			return nil, err
		}
		if err := r.setFD(n, pr); err != nil {
			pr.Close()
			return nil, err
		}
		return pr, nil
	}
	arg := r.literal(rd.Word)
	switch rd.Op {
	case syntax.WordHdoc:
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		go func() {
			pw.WriteString(arg)
			pw.WriteString("\n")
			pw.Close()
		}()
		if err := r.setFD(n, pr); err != nil {
			pr.Close()
			return nil, err
		}
		return pr, nil
	case syntax.DplOut, syntax.DplIn:
		if arg == "-" {
			return nil, r.setFD(n, nil)
		}
		m, err := strconv.Atoi(arg)
		if err != nil || m < 0 || m > 9 {
			if rd.Op == syntax.DplOut && rd.N == nil {
				f, err := r.open(ctx, arg, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644, true)
				if err != nil {
					return nil, err
				}
				r.setFD(1, f)
				r.setFD(2, f)
				return f, nil
			}
			r.errf("%s: %s: bad file descriptor\n", r.shellName(), arg)
			return nil, errors.New("bad fd")
		}
		v := r.getFD(m)
		if v == nil {
			r.errf("%s: %d: bad file descriptor\n", r.shellName(), m)
			return nil, errors.New("bad fd")
		}
		if err := r.setFD(n, v); err != nil {
			r.errf("%s: %d: %v\n", r.shellName(), m, err)
			return nil, err
		}
		return nil, nil
	}
	var mode int
	switch rd.Op {
	case syntax.RdrIn:
		mode = os.O_RDONLY
	case syntax.RdrInOut:
		mode = os.O_RDWR | os.O_CREATE
	case syntax.RdrOut, syntax.RdrClob, syntax.RdrAll:
		mode = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	case syntax.AppOut, syntax.AppAll:
		mode = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	default:
		r.errf("%s: unsupported redirection %v\n", r.shellName(), rd.Op)
		return nil, errors.New("unsupported redirection")
	}
	f, err := r.open(ctx, arg, mode, 0o644, true)
	if err != nil {
		return nil, err
	}
	if rd.Op == syntax.RdrAll || rd.Op == syntax.AppAll {
		r.setFD(1, f)
		r.setFD(2, f)
		return f, nil
	}
	if err := r.setFD(n, f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
