printf 'a b c\nd e\n' | while read x y; do echo "x=$x y=$y"; done
printf 'k=v\n' | { IFS== read k v; echo $k $v; }
printf 'raw\\t\n' | { read -r l; printf '%s\n' "$l"; }
printf 'cooked\\t\n' | { read l; printf '%s\n' "$l"; }
