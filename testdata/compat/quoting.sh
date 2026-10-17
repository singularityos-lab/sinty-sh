a='single $x'
echo "$a"
echo 'it'"'"'s'
echo "tab	here"
printf '%s\n' "a\\b"
printf '%5s|%-5s|%d|%x\n' ab cd 42 255
printf '%s-' one two three; echo
echo "a\$b"
IFS=:; set -- $(echo "p:q:r"); echo $2; IFS=' '
