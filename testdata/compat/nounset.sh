set -u
x=1
echo $x
echo ${missing:-ok}
( echo $missing ) 2>/dev/null || echo failed
