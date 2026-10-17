echo $0 >/dev/null
type true >/dev/null && echo type
command -v sh >/dev/null && echo cmdv
umask 027; umask
x=a; eval "x=\${x}b"; echo $x
set -f; echo /*; set +f
getopts ab: o -b val && echo "$o $OPTARG"
echo ${PWD+pwd}
alias ll='echo aliased' 2>/dev/null
v=$(exit 3); echo $?
printf '%s\n' "$((0x10))"
