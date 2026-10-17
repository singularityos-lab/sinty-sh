set -e
if false; then :; fi
false || true
echo survived
f() { false; echo inf; }
f || echo caught
false
echo notreached
