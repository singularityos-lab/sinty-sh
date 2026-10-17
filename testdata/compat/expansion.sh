x=hello.tar.gz
echo ${x%.*} ${x%%.*} ${x#*.} ${x##*.}
echo ${#x}
unset y
echo ${y:-def} ${y-def2} "${y:+alt}"
y=
echo "[${y:-empty}]" "[${y-set}]"
: ${z:=assigned}
echo $z
echo $((3 + 4 * 2)) $((10 / 3)) $((10 % 3)) $((1 << 4)) $(( (2+3) * 2 ))
i=5; i=$((i+1)); echo $i
echo "$(echo nested $(echo deep))"
echo `echo backtick`
