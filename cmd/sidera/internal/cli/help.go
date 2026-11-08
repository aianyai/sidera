package cli

import (
	"io"
	"strings"

	"github.com/aianyai/sidera/sdk/go/termout"
)

// helpEntry is what `sidera <command> --help` prints for a command. The list in
// `sidera --help` shows the summary alone; the command's own help shows the summary
// as the first sentence of its paragraph, followed by details, so the two never
// repeat each other. A summary is one clause with no final period.
type helpEntry struct{ usage, summary, details, example string }

// paragraph is the summary and the details as the sentences they are.
func (h helpEntry) paragraph() string {
	if h.details == "" {
		return h.summary + "."
	}
	return h.summary + ". " + h.details
}

// helpEntries has one entry for every command the dispatcher accepts.
var helpEntries = map[string]helpEntry{
	"me":                         {"me", "Show this session's address, declaration and tool limits", "", "sidera me --json"},
	"declare":                    {"declare [--paused=<bool>] [--refuse-multi-recipient=<bool>] [--restrict-senders=<bool>] [--accept-from <addr>,...] [--hidden=<bool>]", "Change only the declaration fields you provide", "", "sidera declare --paused=false"},
	"peers":                      {"peers", "List visible nodes", "", "sidera peers"},
	"directory":                  {"directory [--node <id>] [--limit <n>] [--cursor <cursor>]", "List one page of visible endpoints", "", "sidera directory --limit 20 --json"},
	"send":                       {"send --to <addr> [--body <text> | --body-file <file>] [--content-type <type>] [--thread <id>] [--accept-before <seconds>] [--key <key>]", "Send one message to one address", "The message is non-empty UTF-8 text; omitted body flags read stdin.", "sidera send --to <addr> --body hello"},
	"reply":                      {"reply <message-id> [--body <text> | --body-file <file>] [--content-type <type>] [--thread <id>] [--key <key>]", "Reply to a received message", "Omitted body flags read stdin.", "sidera reply <message-id> --body understood"},
	"recv":                       {"recv [--wait <seconds>] [--msg <id> --part <k>] [--ack <lease>]", "Receive waiting messages as a JSON page", "The page is unchanged. A page's next.msg and next.part become --msg and --part. Receive never acknowledges the new page.", "sidera recv --wait 8"},
	"ack":                        {"ack <lease> [<message-id>:<part> ...]", "Acknowledge received message parts", "It acknowledges the named visible parts, or the whole page when none are listed.", "sidera ack <lease> <message-id>:1"},
	"control":                    {"control <addr> <signal> [--preset <name>] [--model <name>] [--effort <level>] [--body <text> | --body-file <file>] [--content-type <type>] [--accept-before <seconds>] [--key <key>]", "Send a control signal to an address", "session.create may omit its initial message. It may also choose the model and the effort of the new session, within what the preset allows; the status of the runtime endpoint lists each preset's defaults and allowed models.", "sidera control <addr> session.create --preset default --json"},
	"probe":                      {"probe", "Present SIDERA_PROBE_TOKEN for the CLI front end", "", "sidera probe --json"},
	"mcp":                        {"mcp", "Serve the Agent tools over MCP stdio", "Standard output contains protocol messages only, including with --json.", "sidera mcp"},
	"init":                       {"init [--port <n>]", "Create the node's data directory and identity", "The node will listen on the port (default 7744).", "sidera init --home ~/.sidera --port 7744"},
	"daemon":                     {"daemon", "Run the node in the foreground", "", "sidera daemon"},
	"daemon stop":                {"daemon stop", "Ask the running node to stop", "It stops after acknowledging the request.", "sidera daemon stop --json"},
	"daemon restart":             {"daemon restart [--expect-version <version>] [--wait <duration>]", "Restart the running node and wait for the new daemon", "The node shuts down and the program is started again as it was started (default --wait 2m; 0 returns once the restart is accepted). The daemon first runs the program, and refuses to restart, staying up, if it cannot be started again or does not report --expect-version.", "sidera daemon restart --expect-version 0.12.0"},
	"status":                     {"status [<message-id>]", "Show the node's status, or a sent message's delivery", "With a message identifier it reads that sent message instead of the node.", "sidera status <message-id> --json"},
	"node show":                  {"node show", "Show node settings and their version", "", "sidera node show --json"},
	"node set":                   {"node set <field>=<json> ... [--expected-version <n>]", "Change node settings", "Values that are not JSON are taken as strings.", "sidera node set node_queue_limit=20000 reply_permission=false"},
	"alerts list":                {"alerts list [--all]", "List open alerts", "--all includes acknowledged alerts.", "sidera alerts list --all --json"},
	"alerts ack":                 {"alerts ack <id>", "Acknowledge an alert", "", "sidera alerts ack <id>"},
	"endpoint list":              {"endpoint list [--instance <name>]", "List this node's endpoints", "Closed endpoints are included.", "sidera endpoint list --instance work --json"},
	"endpoint close":             {"endpoint close <address> [--allow-attached]", "Close an endpoint", "A session a person opened and is using (origin attached) is closed only with --allow-attached.", "sidera endpoint close <address>"},
	"endpoint set":               {"endpoint set <address> <field>=<json> ... [--expected-version <n>]", "Change endpoint settings", "It uses the instances collection version.", "sidera endpoint set <address> max_queue_s=1800 backlog_limit=200"},
	"preset list":                {"preset list", "List presets and their collection version", "", "sidera preset list --json"},
	"preset add":                 {"preset add <name> --file <preset.json|-> [--expected-version <n>]", "Add a preset from a document", "An omitted version is read first.", "sidera preset add review --file preset.json"},
	"preset remove":              {"preset remove <name> [--expected-version <n>]", "Remove a preset", "", "sidera preset remove review"},
	"network show":               {"network show", "Show where the node listens and who it is connected to", "It shows the line to give another owner so they can reach this node, the addresses kept for other nodes and which are connected.", "sidera network show --json"},
	"network address set":        {"network address set <node> <multiaddr> ... [--expected-version <n>]", "Replace the addresses kept for a node", "Until a certificate of the node is held, one address must end in /p2p/<peer id>; once held, every /p2p/ must be the certificate's.", "sidera network address set <node> /ip4/192.168.1.20/tcp/7744/p2p/<peer-id>"},
	"network address remove":     {"network address remove <node> [--expected-version <n>]", "Remove the addresses kept for a node", "", "sidera network address remove <node>"},
	"network listen set":         {"network listen set <multiaddr> ... [--expected-version <n>]", "Replace the addresses the node listens on", "The daemon has to be restarted for it to take effect.", "sidera network listen set /ip4/0.0.0.0/tcp/7745 /ip4/0.0.0.0/udp/7745/quic-v1"},
	"trust expect":               {"trust expect <node>", "Enter an expectation for a node", "The owner means to pair with it, which stands for a confirmation made in advance. The result says when the expectation ends.", "sidera trust expect <node>"},
	"trust forget":               {"trust forget <node>", "Delete the expectation for a node", "", "sidera trust forget <node>"},
	"trust reject":               {"trust reject <pairing_id>", "End a request for a pairing that is stuck or unwanted", "It rejects one this node holds, or withdraws one it started. Find the identifier with trust list.", "sidera trust reject <pairing_id>"},
	"trust request":              {"trust request <node> [<multiaddr> ...] [--wait <duration>]", "Ask a node for a pairing", "It dials the multiaddrs for this request only, or else the addresses kept for the node. The verification code is printed to compare with the other node's screen; then, for at most --wait (default 10s, 0 for none), the command waits for the pairing to complete.", "sidera trust request <node> /ip4/192.168.1.20/tcp/7744/p2p/<peer-id> --wait 30s"},
	"trust list":                 {"trust list", "List the nodes this node trusts", "It also lists the expectations that stand, the requests it holds and those it started, and the nodes removed from its trust.", "sidera trust list --json"},
	"trust remove":               {"trust remove <node>", "Remove a node from this node's trust", "It revokes the credentials held with the node; the other node is not told.", "sidera trust remove <node>"},
	"trust clear":                {"trust clear <node>", "Take a removed node back", "The node can then be paired with again; revoked credentials stay revoked.", "sidera trust clear <node>"},
	"identity export-revocation": {"identity export-revocation [--out <new-file>]", "Export the self-revocation for offline backup", "--out writes the verified raw record privately.", "sidera identity export-revocation --out self-revocation.rec"},
	"identity confirm-backup":    {"identity confirm-backup <digest>", "Confirm the self-revocation backup is kept offline", "", "sidera identity confirm-backup <digest>"},
	"identity retire":            {"identity retire --node <id> --yes", "Permanently retire this node's identity", "It also stops the daemon.", "sidera identity retire --node <id> --yes"},
	"clock clear-rollback":       {"clock clear-rollback", "Accept the current wall clock", "It clears the rollback pause.", "sidera clock clear-rollback"},
	"adapter install":            {"adapter install <package-directory>", "Install an adapter from a local directory", "The directory is resolved to an absolute path.", "sidera adapter install ./package"},
	"adapter list":               {"adapter list", "List installed adapters and their instances", "It shows probes, faults and availability.", "sidera adapter list --json"},
	"adapter enable":             {"adapter enable <adapter-id> [--instance <name>] [--config <file|->] [--expected-version <n>]", "Enable an adapter instance", "It creates the instance if needed.", "sidera adapter enable claude --instance work"},
	"adapter config":             {"adapter config <instance> --config <file|-> [--expected-version <n>]", "Replace the configuration of an instance", "The instance may be enabled or not; a running instance is restarted to take it.", "sidera adapter config codex --config codex.json"},
	"adapter disable":            {"adapter disable <instance> [--expected-version <n>]", "Disable an instance", "It closes the instance's endpoints.", "sidera adapter disable work"},
	"adapter remove":             {"adapter remove <instance> [--force] [--expected-version <n>]", "Remove a disabled instance and its settings", "It is refused while the instance is enabled or has open session endpoints, and while the adapter's setup has recorded something in a client (undo it with adapter setup --remove first). --force skips that check, for an instance whose setup cannot run; whatever the setup installed in the client is then left in place.", "sidera adapter remove work"},
	"adapter setup":              {"adapter setup <instance> [--check | --remove | --refresh] [--yes] [-- <adapter arguments>]", "Set up the client of an adapter instance", "It brings the client of the instance's adapter (its MCP registration, permissions, Skill) in line with the instance, by running the adapter's own setup. It shows what it would do and asks about each item that changes the client's configuration (a terminal, or --yes to accept every item). --check only reports what has drifted from what setup recorded (exit 0 with none, 1 with some); --remove undoes what setup recorded, asking about each item; --refresh brings the recorded items up to date without asking, which is what sidera update does after it installs the adapters. The words after -- are the adapter's own arguments. It needs the daemon, an adapter that declares a setup, and an instance that has run once.", "sidera adapter setup work --yes"},
	"update":                     {"update [--check] [--version <version>] [--yes]", "Bring this installation to a release", "It replaces the programs next to this one, restarts the running daemon into the new version and installs the new adapter packages, doing only the steps that are missing. Releases are checked against the signature built into the program. It asks before changing anything unless --yes is given, and only for programs installed from a release archive. --check only reports the release and whether this program may update to it (exit 0 once it has read a release it can believe, 1 when it cannot reach or verify one; --json adds update_available and admitted).", "sidera update --check --json"},
	"version":                    {"version", "Print the version of this program", "It also prints the commit it was built from and the Go version; with --json, the same as an object. It needs no running node.", "sidera version --json"},
}

// helpGroup is a heading of `sidera --help` and the commands listed under it. Every
// command is in exactly one group, in the order the list shows them.
type helpGroup struct {
	title    string
	commands []string
}

var helpGroups = []helpGroup{
	{"Messages", []string{"me", "declare", "peers", "directory", "send", "reply", "recv", "ack", "control", "probe", "mcp"}},
	{"Node", []string{"init", "daemon", "daemon stop", "daemon restart", "status", "node show", "node set", "alerts list", "alerts ack"}},
	{"Endpoints and presets", []string{"endpoint list", "endpoint close", "endpoint set", "preset list", "preset add", "preset remove"}},
	{"Network", []string{"network show", "network address set", "network address remove", "network listen set"}},
	{"Trust", []string{"trust expect", "trust forget", "trust reject", "trust request", "trust list", "trust remove", "trust clear"}},
	{"Identity and clock", []string{"identity export-revocation", "identity confirm-backup", "identity retire", "clock clear-rollback"}},
	{"Adapters", []string{"adapter install", "adapter list", "adapter enable", "adapter config", "adapter disable", "adapter remove", "adapter setup"}},
	{"Update and maintenance", []string{"update", "version"}},
}

func (c *command) printHelp(args []string) error {
	if len(args) == 0 {
		_, err := io.WriteString(c.out, c.helpList())
		return err
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
	for _, group := range helpGroups {
		for _, name := range group.commands {
			if strings.HasPrefix(name, key+" ") {
				selected = append(selected, name)
			}
		}
	}
	if len(selected) == 0 {
		return usage("unknown command: " + strings.Join(args, " "))
	}
	for i, name := range selected {
		if i > 0 {
			if _, err := io.WriteString(c.out, "\n"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(c.out, c.commandHelp(helpEntries[name])); err != nil {
			return err
		}
	}
	return nil
}

// helpList is `sidera --help`: the usage, then every command under its group with its
// summary, wrapped under the names.
func (c *command) helpList() string {
	const indent = 2
	width := c.out.TextWidth()
	var b strings.Builder
	b.WriteString(hanging("Usage: ", "sidera [--home <dir>] [--json] [--color <when>] <command> [arguments]", width))
	b.WriteString("\n" + termout.Wrap("Global flags may precede or follow the command; <when> is auto, always or never.", width) + "\n")
	for _, group := range helpGroups {
		fields := make([]termout.Field, len(group.commands))
		for i, name := range group.commands {
			fields[i] = termout.Field{Label: name, Value: helpEntries[name].summary}
		}
		b.WriteString("\n" + c.out.Bold(group.title) + "\n")
		b.WriteString(termout.Indent(c.out.List(fields, c.out.Cyan, width-indent), indent))
	}
	b.WriteString("\n" + termout.Wrap("Run "+c.out.Command("sidera <command> --help")+" for a command's arguments and an example.", width) + "\n")
	return b.String()
}

// commandHelp is `sidera <command> --help`: the usage, the command's purpose and an
// example. The example is a command to copy, so it is the one part that is never wrapped.
func (c *command) commandHelp(h helpEntry) string {
	width := c.out.TextWidth()
	return hanging("Usage: ", "sidera "+h.usage, width) + "\n" +
		termout.Wrap(h.paragraph(), width) + "\n" +
		"Example: " + h.example + "\n"
}

// hanging writes text after a label, wrapped to width, with the lines after the
// first lined up under the text. It ends in no line break.
func hanging(label, text string, width int) string {
	lines := strings.Split(termout.Wrap(text, max(width-len(label), 1)), "\n")
	rest := ""
	if len(lines) > 1 {
		rest = "\n" + termout.Indent(strings.Join(lines[1:], "\n"), len(label))
	}
	return label + lines[0] + rest
}
