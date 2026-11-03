package cli

import (
	"fmt"
	"strings"
)

type helpEntry struct{ usage, purpose, example string }

var helpEntries = map[string]helpEntry{
	"setup controller":           {"setup controller", "Print the commands that add the sidera MCP server to Claude Code (user scope) and Codex, and the permission each needs so that its tools run without a prompt, so that sessions you open can attach themselves as controllers. It only prints; it runs nothing and changes no configuration.", "sidera setup controller"},
	"mcp":                        {"mcp", "Serve Agent tools over MCP stdio; stdout contains protocol messages only, including with --json.", "sidera mcp"},
	"daemon restart":             {"daemon restart [--expect-version <version>] [--wait <duration>]", "Shut the running node down and start the program again as it was started, then wait for the new daemon (default --wait 2m; 0 returns once the restart is accepted). The daemon first runs the program, and refuses to restart, staying up, if it cannot be started again or does not report --expect-version.", "sidera daemon restart --expect-version 0.12.0"},
	"daemon stop":                {"daemon stop", "Ask the running node to stop after acknowledging the request.", "sidera daemon stop --json"},
	"alerts list":                {"alerts list [--all]", "List open alerts, optionally including acknowledged alerts.", "sidera alerts list --all --json"},
	"alerts ack":                 {"alerts ack <id>", "Acknowledge an alert.", "sidera alerts ack <id>"},
	"preset list":                {"preset list", "List presets and their collection version.", "sidera preset list --json"},
	"preset add":                 {"preset add <name> --file <preset.json|-> [--expected-version <n>]", "Add an explicit preset document; omitted version is read first.", "sidera preset add review --file preset.json"},
	"preset remove":              {"preset remove <name> [--expected-version <n>]", "Remove a preset.", "sidera preset remove review"},
	"adapter install":            {"adapter install <package-directory>", "Install an adapter from a local directory, resolved to an absolute path.", "sidera adapter install ./package"},
	"adapter list":               {"adapter list", "List installed adapters, instances and probes.", "sidera adapter list --json"},
	"adapter enable":             {"adapter enable <adapter-id> [--instance <name>] [--config <file|->] [--expected-version <n>]", "Enable an adapter instance, creating it if needed.", "sidera adapter enable claude --instance work"},
	"adapter config":             {"adapter config <instance> --config <file|-> [--expected-version <n>]", "Replace the configuration of an instance, enabled or not; a running instance is restarted to take it.", "sidera adapter config codex --config codex.json"},
	"adapter disable":            {"adapter disable <instance> [--expected-version <n>]", "Disable an instance and close its endpoints.", "sidera adapter disable work"},
	"adapter remove":             {"adapter remove <instance> [--expected-version <n>]", "Remove a disabled instance and its settings. It is refused while the instance is enabled or has open session endpoints.", "sidera adapter remove work"},
	"endpoint list":              {"endpoint list [--instance <name>]", "List this node's endpoints, including closed endpoints.", "sidera endpoint list --instance work --json"},
	"endpoint close":             {"endpoint close <address>", "Close an endpoint.", "sidera endpoint close <address>"},
	"endpoint set":               {"endpoint set <address> <field>=<json> ... [--expected-version <n>]", "Change endpoint settings using the instances collection version.", "sidera endpoint set <address> max_queue_s=1800 backlog_limit=200"},
	"node show":                  {"node show", "Show node settings and their version.", "sidera node show --json"},
	"node set":                   {"node set <field>=<json> ... [--expected-version <n>]", "Change node settings; non-JSON assignment values are strings.", "sidera node set node_queue_limit=20000 reply_permission=false"},
	"network show":               {"network show", "Show where the node listens, the line to give another owner so they can reach this node, the addresses kept for other nodes and which are connected.", "sidera network show --json"},
	"network address set":        {"network address set <node> <multiaddr> ... [--expected-version <n>]", "Replace the addresses kept for a node. Until a certificate of the node is held, one address must end in /p2p/<peer id>; once held, every /p2p/ must be the certificate's.", "sidera network address set <node> /ip4/192.168.1.20/tcp/7744/p2p/<peer-id>"},
	"network address remove":     {"network address remove <node> [--expected-version <n>]", "Remove the addresses kept for a node.", "sidera network address remove <node>"},
	"network listen set":         {"network listen set <multiaddr> ... [--expected-version <n>]", "Replace the addresses the node listens on; the daemon has to be restarted for it to take effect.", "sidera network listen set /ip4/0.0.0.0/tcp/7745 /ip4/0.0.0.0/udp/7745/quic-v1"},
	"trust expect":               {"trust expect <node>", "Enter an expectation for a node: the owner means to pair with it, which stands for a confirmation made in advance. The result says when the expectation ends.", "sidera trust expect <node>"},
	"trust forget":               {"trust forget <node>", "Delete the expectation for a node.", "sidera trust forget <node>"},
	"trust reject":               {"trust reject <pairing_id>", "End a request for a pairing that is stuck or unwanted: reject one this node holds, or withdraw one it started. Find the identifier with trust list.", "sidera trust reject <pairing_id>"},
	"trust request":              {"trust request <node> [<multiaddr> ...] [--wait <duration>]", "Ask a node for a pairing, dialing the multiaddrs for this request only or else the addresses kept for the node. The verification code is printed to compare with the other node's screen; then, for at most --wait (default 10s, 0 for none), the command waits for the pairing to complete.", "sidera trust request <node> /ip4/192.168.1.20/tcp/7744/p2p/<peer-id> --wait 30s"},
	"trust list":                 {"trust list", "List the nodes this node trusts, the expectations that stand, the requests it holds and those it started, and the nodes removed from its trust.", "sidera trust list --json"},
	"trust remove":               {"trust remove <node>", "Remove a node from this node's trust and revoke the credentials held with it; the other node is not told.", "sidera trust remove <node>"},
	"trust clear":                {"trust clear <node>", "Take a removed node back, so that it can be paired with again; revoked credentials stay revoked.", "sidera trust clear <node>"},
	"identity export-revocation": {"identity export-revocation [--out <new-file>]", "Export the self-revocation for offline backup; --out writes the verified raw record privately.", "sidera identity export-revocation --out self-revocation.rec"},
	"identity confirm-backup":    {"identity confirm-backup <digest>", "Confirm that the exported statement is kept outside this device.", "sidera identity confirm-backup <digest>"},
	"identity retire":            {"identity retire --node <id> --yes", "Permanently retire this node's identity and stop the daemon.", "sidera identity retire --node <id> --yes"},
	"clock clear-rollback":       {"clock clear-rollback", "Accept the current wall clock and clear the rollback pause.", "sidera clock clear-rollback"},
	"init":                       {"init [--port <n>]", "Create the node's private data directory and identity; the node will listen on the port (default 7744).", "sidera init --home ~/.sidera --port 7744"},
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
	"version":                    {"version", "Print the version of this program, the commit it was built from and the Go version; with --json, the same as an object. It needs no running node.", "sidera version --json"},
	"probe":                      {"probe", "Present SIDERA_PROBE_TOKEN for the CLI front end.", "sidera probe --json"},
}

var helpOrder = []string{"init", "daemon", "daemon stop", "daemon restart", "me", "declare", "peers", "directory", "send", "reply", "recv", "ack", "status", "control", "probe", "version", "mcp", "setup controller", "alerts list", "alerts ack", "preset list", "preset add", "preset remove", "adapter install", "adapter list", "adapter enable", "adapter config", "adapter disable", "adapter remove", "endpoint list", "endpoint close", "endpoint set", "node show", "node set", "network show", "network address set", "network address remove", "network listen set", "trust expect", "trust forget", "trust reject", "trust request", "trust list", "trust remove", "trust clear", "identity export-revocation", "identity confirm-backup", "identity retire", "clock clear-rollback"}

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
	// The longest of the first one to three words that names a command.
	key := args[0]
	for n := 2; n <= min(len(args), 3); n++ {
		if _, ok := helpEntries[strings.Join(args[:n], " ")]; ok {
			key = strings.Join(args[:n], " ")
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
