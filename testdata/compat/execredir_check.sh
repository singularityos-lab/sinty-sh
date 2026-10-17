d=${TMPDIR:-/tmp}/r.$$
sh_bin=${SH_UNDER_TEST:-sh}
"$sh_bin" -c 'exec 3>"$1"; echo three >&3; exec 3>&-; exec 5<"$1"; read l <&5; echo "read:$l"; exec 5<&-' sh "$d"
"$sh_bin" -c 'exec 3>&1; ( echo via-three >&3 ); echo done' 
"$sh_bin" -c '{ echo a; echo b >&2; } 2>&1 | sort'
"$sh_bin" -c 'x=$(echo out; echo err >&2) 2>/dev/null; echo "[$x]"'
"$sh_bin" -c 'f() { echo func-bg $1; }; f job & wait; g=1; ( echo g=$g ) & wait'
"$sh_bin" -c 'sleep 0.1 & a=$!; sleep 0.1 & b=$!; [ $a != $b ] && echo distinct; wait $a $b; echo all'
"$sh_bin" -c 'cat <<EOF2 &
bg-heredoc
EOF2
wait'
rm -f "$d"
