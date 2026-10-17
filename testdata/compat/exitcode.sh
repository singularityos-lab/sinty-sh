sh -c 'exit 42'; echo $?
nonexistent_command_xyz 2>/dev/null; echo $?
exit 3
