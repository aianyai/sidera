// Package setup is the Claude adapter's client setup: it brings Claude Code's own
// configuration in line with a Sidera instance, so that a session a person opens
// has the `sidera` tools, may call them without asking, and has the Skill that says
// what Sidera is. It implements adaptersetup.Handler; the contract, the record and
// the rules it follows are in the adapter-setup reference, and what it does to
// Claude Code and why is in the Claude adapter reference.
//
// Claude Code is only ever reached through its own command (`claude mcp`,
// `claude plugin`), behind the Client interface, except for the permission rule, which
// lives in a file the person also edits and is changed with adaptersetup.EditJSONFile.
package setup
