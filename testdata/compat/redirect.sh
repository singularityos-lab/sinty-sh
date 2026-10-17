d=${TMPDIR:-/tmp}/sinty-sh-compat.$$
mkdir -p "$d"
echo one > "$d/f"
echo two >> "$d/f"
cat < "$d/f"
cat "$d/missing" 2>/dev/null || echo missing
{ echo grouped; echo out; } > "$d/g"; cat "$d/g"
exec 3>"$d/fd3" 2>/dev/null && echo three >&3 && exec 3>&- && cat "$d/fd3" || echo nofd3
cat <<END
heredoc $USER_NOT_SET line
END
cat <<'END'
quoted $HOME
END
rm -rf "$d"
