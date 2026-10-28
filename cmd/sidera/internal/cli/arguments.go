package cli

import (
	"flag"
	"strings"
)

// commandArguments registers a command's flags once, before global scanning.
// Both scans then consult that same FlagSet and its bound values.
type commandArguments struct {
	flags      *flag.FlagSet
	message    messageFlags
	query      queryFlags
	recv       receiveFlags
	management managementFlags
}

func arguments(args []string) *commandArguments {
	path := commandPath(args)
	name := strings.Join(path, " ")
	out := &commandArguments{flags: flags(name)}
	if len(path) == 0 {
		return out
	}
	switch path[0] {
	case "send", "reply", "control":
		out.message.add(out.flags, path[0])
	case "me", "peers", "directory", "declare", "probe":
		out.query.add(out.flags, path[0])
	case "recv":
		out.recv.add(out.flags)
	default:
		out.management.add(out.flags, name)
	}
	return out
}

// Only global flags can precede a command or separate a group from its action.
// Discovery does not parse values or perform I/O; globals reports any errors.
func commandPath(args []string) []string {
	var path []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if option, ok := globalOption(a); ok {
			if option.takesNext {
				i++
			}
			continue
		}
		path = append(path, a)
		if len(path) == 2 {
			break
		}
		switch a {
		case "daemon", "alerts", "preset", "adapter", "endpoint", "node", "identity", "clock":
		default:
			return path
		}
	}
	return path
}

func takesValue(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return !ok || !b.IsBoolFlag()
}

type globalFlag struct {
	name, value string
	takesNext   bool
}

// Global spelling and arity have one definition for discovery and scanning.
func globalOption(arg string) (globalFlag, bool) {
	_, value, has := strings.Cut(arg, "=")
	switch {
	case arg == "-h" || arg == "--help":
		return globalFlag{name: "help"}, true
	case arg == "--home" || strings.HasPrefix(arg, "--home="):
		return globalFlag{name: "home", value: value, takesNext: !has}, true
	case arg == "--json" || strings.HasPrefix(arg, "--json="):
		if !has {
			value = "true"
		}
		return globalFlag{name: "json", value: value}, true
	default:
		return globalFlag{}, false
	}
}
