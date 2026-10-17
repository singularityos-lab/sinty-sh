d=${TMPDIR:-/tmp}/sinty-sh-dot.$$
printf 'sourced_var=yes\nsf() { echo sf; }\n' > "$d"
. "$d"
echo $sourced_var; sf
rm -f "$d"
