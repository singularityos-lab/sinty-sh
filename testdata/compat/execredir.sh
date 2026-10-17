d=${TMPDIR:-/tmp}/sinty-sh-execredir.$$
exec 4>"$d.log"
echo to-four >&4
exec >>"$d.out" 2>&1
echo first
echo err >&2
ls /nonexistent-path-xyz 2>/dev/null || echo nols
exec >&4
echo back-on-four
exec 4>&-
exec >/dev/null
cat "$d.log" "$d.out" > "$d.all"
exec >"$d.final"
cat "$d.all"
exec >/dev/stderr
