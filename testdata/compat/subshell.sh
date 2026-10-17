x=1
( x=2; echo in $x )
echo out $x
y=$(x=3; echo $x); echo $y $x
( exit 4 ); echo $?
cd /; ( cd /usr && pwd ); pwd
echo $(( $(echo 2) + 3 ))
