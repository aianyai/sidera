// Package claudeargs builds what a Claude Code turn process is started with: its
// argument vector, its environment, the user message it reads and the MCP
// configuration it loads, and reads the preset that says how. It is pure: it
// reads no file and starts nothing, so the exact command line of every turn is
// decided here and testable without a Claude.
package claudeargs
