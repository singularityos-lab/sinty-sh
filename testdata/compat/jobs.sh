sleep 0.1 & p=$!
case $p in ''|*[!0-9]*) echo badpid;; *) echo numeric;; esac
kill -0 $p && echo alive
wait $p; echo rc=$?
sh -c 'exit 5' & wait $!; echo rc=$?
sleep 5 & k=$!; kill $k; wait $k; echo rc=$?
( sleep 0.1; echo sub ) & wait
echo done
