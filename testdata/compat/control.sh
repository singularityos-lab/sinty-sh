for i in 1 2 3; do
  case $i in
    1) echo one;;
    2|3) echo "two or three: $i";;
  esac
done
n=0
while [ $n -lt 3 ]; do n=$((n+1)); done; echo $n
until [ $n -eq 0 ]; do n=$((n-1)); done; echo $n
if false; then echo no; elif true; then echo elif; else echo else; fi
for i in 1 2 3 4; do [ $i = 2 ] && continue; [ $i = 4 ] && break; echo $i; done
true && echo and; false || echo or
! false && echo negated
