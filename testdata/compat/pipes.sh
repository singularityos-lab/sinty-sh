printf 'b\na\nc\n' | sort | head -n 2
echo hi | tr a-z A-Z
false | true; echo $?
true | false; echo $?
x=$(printf 'l1\nl2\n' | wc -l); echo $x
echo abc | { read v; echo got $v; }
