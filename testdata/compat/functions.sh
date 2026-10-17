f() { echo "f:$1:$#"; return 3; }
f x y; echo rc=$?
g() { local v=inner 2>/dev/null || v=inner; echo $v; }
v=outer; g
counter=0
inc() { counter=$((counter+1)); }
inc; inc; echo $counter
rec() { [ $1 -le 0 ] && return 0; echo $1; rec $(($1-1)); }
rec 3
