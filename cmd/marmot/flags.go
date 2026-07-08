package main

import "strings"

// reorderInterspersedFlags moves flag arguments ahead of positional arguments
// so subcommands accept flags before or after positionals, even though Go's
// flag package stops parsing at the first non-flag argument. valueFlags names
// flags that consume the following argument as their value; boolFlags names
// flags that take no value. Flags written as --name=value need no entry in
// either map.
func reorderInterspersedFlags(args []string, valueFlags, boolFlags map[string]bool) []string {
	if len(args) == 0 {
		return args
	}
	var flags []string
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			// flag.Parse treats "--" as the end of flags; everything after
			// it is positional. Preserve it verbatim and stop classifying.
			positionals = append(positionals, args[i:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}
		name := strings.TrimLeft(arg, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			flags = append(flags, arg)
			continue
		}
		if boolFlags[name] {
			flags = append(flags, arg)
			continue
		}
		if valueFlags[name] && i+1 < len(args) {
			flags = append(flags, arg, args[i+1])
			i++
			continue
		}
		flags = append(flags, arg)
	}
	return append(flags, positionals...)
}
