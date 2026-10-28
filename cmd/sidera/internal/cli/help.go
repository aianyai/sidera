package cli

import (
	"fmt"
	"strings"
)

type helpEntry struct{ usage, purpose, example string }

var helpEntries = map[string]helpEntry{
	"daemon stop":                {"daemon stop", "Ask the running node to stop after acknowledging the request.", "sidera daemon stop --json"},
	"alerts list":                {"alerts list [--all]", "List open alerts, optionally including acknowledged alerts.", "sidera alerts list --all --json"},
	"alerts ack":                 {"alerts ack <id>", "Acknowledge an alert.", "sidera alerts ack <id>"},
	"preset list":                {"preset list", "List presets and their collection version.", "sidera preset list --json"},
	"preset add":                 {"preset add <name> --file <preset.json|-> [--expected-version <n>]", "Add an explicit preset document; omitted version is read first.", "sidera preset add review --file preset.json"},
	"preset remove":              {"preset remove <name> [--expected-version <n>]", "Remove a preset.", "sidera preset remove review"},
	"adapter install":            {"adapter install <package-directory>", "Install an adapter from a local directory, resolved to an absolute path.", "sidera adapter install ./package"},
	"adapter list":               {"adapter list", "List installed adapters, instances and probes.", "sidera adapter list --json"},
	"adapter enable":             {"adapter enable <adapter-id> [--instance <name>] [--config <file|->] [--expected-version <n>]", "Enable an adapter instance, creating it if needed.", "sidera adapter enable claude --instance work"},
	"adapter disable":            {"adapter disable <instance> [--expected-version <n>]", "Disable an instance and close its endpoints.", "sidera adapter disable work"},
	"endpoint list":              {"endpoint list [--instance <name>]", "List this node's endpoints, including closed endpoints.", "sidera endpoint list --instance work --json"},
	"endpoint close":             {"endpoint close <address>", "Close an endpoint.", "sidera endpoint close <address>"},
	"endpoint set":               {"endpoint set <address> <field>=<json> ... [--expected-version <n>]", "Change endpoint settings using the instances collection version.", "sidera endpoint set <address> max_queue_s=1800 backlog_limit=200"},
	"node show":                  {"node show", "Show node settings and their version.", "sidera node show --json"},
	"node set":                   {"node set <field>=<json> ... [--expected-version <n>]", "Change node settings; non-JSON assignment values are strings.", "sidera node set node_queue_limit=20000 reply_permission=false"},
	"identity export-revocation": {"identity export-revocation", "Export the self-revocation for offline backup.", "sidera identity export-revocation"},
	"identity confirm-backup":    {"identity confirm-backup <digest>", "Confirm that the exported statement is kept outside this device.", "sidera identity confirm-backup <digest>"},
	"identity retire":            {"identity retire --node <id> --yes", "Permanently retire this node's identity and stop the daemon.", "sidera identity retire --node <id> --yes"},
	"clock clear-rollback":       {"clock clear-rollback", "Accept the current wall clock and clear the rollback pause.", "sidera clock clear-rollback"},
	"init":                       {"init", "Create the node's private data directory and identity.", "sidera init --home ~/.sidera"},
	"daemon":                     {"daemon", "Run the node in the foreground.", "sidera daemon"},
	"me":                         {"me", "Show this session's address, declaration and tool limits.", "sidera me --json"},
	"declare":                    {"declare [--paused=<bool>] [--refuse-multi-recipient=<bool>] [--restrict-senders=<bool>] [--accept-from <addr>,...] [--hidden=<bool>]", "Change only the declaration fields you provide.", "sidera declare --paused=false"},
	"peers":                      {"peers", "List visible nodes.", "sidera peers"},
	"directory":                  {"directory [--node <id>] [--limit <n>] [--cursor <cursor>]", "List one page of visible endpoints.", "sidera directory --limit 20 --json"},
	"send":                       {"send --to <addr> [--body <text> | --body-file <file>] [--content-type <type>] [--thread <id>] [--accept-before <seconds>] [--key <key>]", "Send one non-empty UTF-8 message; omitted body flags read stdin.", "sidera send --to <addr> --body hello"},
	"reply":                      {"reply <message-id> [--body <text> | --body-file <file>] [--content-type <type>] [--thread <id>] [--key <key>]", "Reply to a received message; omitted body flags read stdin.", "sidera reply <message-id> --body understood"},
	"recv":                       {"recv [--wait <seconds>] [--msg <id> --part <k>] [--ack <lease>]", "Receive an unchanged JSON page. A page's next.msg and next.part become --msg and --part. Receive never acknowledges the new page.", "sidera recv --wait 8"},
	"ack":                        {"ack <lease> [<message-id>:<part> ...]", "Acknowledge named visible parts, or the whole page when none are listed.", "sidera ack <lease> <message-id>:1"},
	"status":                     {"status [<message-id>]", "Read node status, or a sent message when an identifier is given.", "sidera status <message-id> --json"},
	"control":                    {"control <addr> <signal> [--preset <name>] [--body <text> | --body-file <file>] [--content-type <type>] [--accept-before <seconds>] [--key <key>]", "Send a control signal; session.create may omit its initial message.", "sidera control <addr> session.create --preset default --json"},
	"probe":                      {"probe", "Present SIDERA_PROBE_TOKEN for the CLI front end.", "sidera probe --json"},
}

var helpOrder = []string{"init", "daemon", "daemon stop", "me", "declare", "peers", "directory", "send", "reply", "recv", "ack", "status", "control", "probe", "alerts list", "alerts ack", "preset list", "preset add", "preset remove", "adapter install", "adapter list", "adapter enable", "adapter disable", "endpoint list", "endpoint close", "endpoint set", "node show", "node set", "identity export-revocation", "identity confirm-backup", "identity retire", "clock clear-rollback"}

func (c *command) printHelp(args []string) error {
	if len(args) == 0 {
		if _, err := fmt.Fprintln(c.Stdout, "Usage: sidera [--home <directory>] [--json] <command> [arguments]\nGlobal flags may precede or follow the command. Use <command> --help for arguments and an example."); err != nil {
			return err
		}
		for _, name := range helpOrder {
			h := helpEntries[name]
			if _, err := fmt.Fprintf(c.Stdout, "  %-28s %s\n", name, h.purpose); err != nil {
				return err
			}
		}
		return nil
	}
	key := args[0]
	if len(args) > 1 {
		if _, ok := helpEntries[key+" "+args[1]]; ok {
			key += " " + args[1]
		}
	}
	var selected []string
	if _, ok := helpEntries[key]; ok {
		selected = append(selected, key)
	}
	for _, name := range helpOrder {
		if strings.HasPrefix(name, key+" ") {
			selected = append(selected, name)
		}
	}
	if len(selected) == 0 {
		return usage("unknown command: " + strings.Join(args, " "))
	}
	for _, name := range selected {
		h := helpEntries[name]
		if _, err := fmt.Fprintf(c.Stdout, "Usage: sidera %s\n%s\nExample: %s\n", h.usage, h.purpose, h.example); err != nil {
			return err
		}
	}
	return nil
}
