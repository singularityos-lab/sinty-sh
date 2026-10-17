trap 'echo exit trap $?' EXIT
trap 'echo usr1' USR1
kill -USR1 $$
echo after
trap - USR1
trap '' USR2
kill -USR2 $$
echo ignored
false
