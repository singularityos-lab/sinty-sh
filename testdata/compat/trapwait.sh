trap 'echo term; kill $c 2>/dev/null; exit 9' TERM
sleep 10 & c=$!
( sleep 0.3; kill -TERM $$ ) &
wait $c
echo notreached
