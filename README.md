# sinty-sh

The `/bin/sh` of SintyOS. A POSIX shell for scripts, written in Go on the same
interpreter as ush, the user shell of SintyOS. Boot scripts, service wrappers,
udev `RUN` programs and `system()` calls from C programs all run through it.

## Why it exists

SintyOS used two unrelated shells: ush in the terminal and a C shell from the
base system for everything else. sinty-sh puts the scripts on the same
memory-safe interpreter as the terminal, maintained in one place, and lets the
system decide what a script running as root may inherit.

## What it implements

The language is the one of `mvdan.cc/sh`, the interpreter ush is built on,
which parses POSIX sh and the common Bash extensions. On top of it sinty-sh
behaves as a process-level shell:

- **Background jobs are real processes.** `cmd &` starts a child shell that
  receives the variables, functions, options and positional parameters through
  a pipe, so `$!` is a real pid that `kill`, `wait` and other programs
  understand. The child replaces itself with the job when the job is a single
  command, so `$!` is the pid of the program itself.
- **`exec` replaces the shell**, with its redirections, and the last simple
  command of `sh -c` or of a script replaces the shell as well, so a service
  wrapper hands its pid to the program it starts.
- **Signals.** `trap` handles signals, `''` ignores them, `-` restores them.
  `wait` returns early when a trapped signal arrives, as POSIX requires.
- **File descriptors 0 to 9**, with `n>`, `n>>`, `n<`, `n<>`, `n>&m`, `n<&m`
  and `n>&-`, which persist after `exec` and are passed to the commands run.
- **Builtins** a system shell needs and the interpreter did not have: `kill`,
  `umask` (octal and symbolic), `ulimit`, `times`, plus C notation for
  arithmetic constants (`0x1f`, `017`, `base#digits`).
- **Scripts are run while they are read**, so the commands before a syntax
  error still run and a script can read the rest of its own input.

## Policies for root

When the shell runs with root privileges:

- `LD_PRELOAD`, `LD_AUDIT` and `LD_LIBRARY_PATH` inherited from the caller are
  dropped. A script may still set them for the programs it starts.
- a script, or a file read with `.` or `source`, must be owned by root and not
  writable by group or others, otherwise it is refused.
- a script started by PID 1 is recorded in the kernel log at debug level.

## Usage

    sinty-sh [-aefnux] [-o option] -c command [name [argument ...]]
    sinty-sh [-aefnux] [-o option] file [argument ...]
    sinty-sh [-aefnux] [-o option] [-s] [argument ...]

Installed as `/bin/sh`. It is not an interactive shell; ush is.

## Compatibility

`go test ./...` runs every script in `testdata/compat` under dash and under
sinty-sh and requires the same output and exit status. On the SintyOS image
all of the shell scripts it ships parse with `sinty-sh -n`.

`echo` follows Bash and BusyBox: backslashes are printed as they are unless
`-e` is given. Subshells in parentheses and command substitutions run inside
the shell process, so `umask` and `ulimit` set in one also apply to the
shell itself.

## Build

    make build            # ./sinty-sh
    make static           # stripped static binary for the image
    make test
    go build ./...        # plain build

Requires Go >= 1.25 and Linux. The non-stdlib dependencies are
`mvdan.cc/sh/v3` (the parser), `golang.org/x/sys/unix` and `golang.org/x/term`.

## License

GPL-3.0-only. The interpreter in `internal/interp`, `internal/expand` and
`internal/shpattern` comes from mvdan.cc/sh and keeps its BSD-3-Clause license.
