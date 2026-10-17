[ -d / ] && echo dir
[ -f /nonexistent ] || echo nofile
[ abc = abc ] && echo eq
[ 3 -gt 2 -a 1 -lt 2 ] && echo arith
[ -z "" ] && [ -n x ] && echo strings
test ! -e /nonexistent && echo neg
[ "a" \< "b" ] && echo lt
