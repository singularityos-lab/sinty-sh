set -- a "b c" d
echo $#
for x in "$@"; do echo "[$x]"; done
for x in $*; do echo "<$x>"; done
shift
echo "$1|$2"
set --
echo "n=$#"
