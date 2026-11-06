# guidance

What an agent is told about Sidera, written once. A session that has the `sidera`
tools does not know what the channel is, when to use it or that a page it read
must be acknowledged; this package holds the words that tell it. They are shared
by `sidera mcp`, which serves them as the instructions of the MCP server, by the
adapters that start sessions, and by the plugins that install the Skill. It is in
the SDK because the adapters may import nothing else of this repository, and
three places that each wrote the facts by hand had already drifted apart.

## Entry points

| Name | What it is |
| ---- | ---------- |
| `Short` | The instructions of the `sidera` MCP server: one line |
| `Full` | The statement an adapter gives the sessions it starts: facts and tools, as paragraphs. An adapter appends, after a blank line, the sentences true only of its client |
| `Skill()` | The bytes of one `SKILL.md`: front matter, `Full`, then the operating procedure |
| `SkillName` | The `name` in its front matter |

## Rules

- **One set of facts.** The four facts (what the channel is, when to use it, call
  `recv` first when woken, `ack` a page or pass `ack_lease`) are constants of the
  package and appear word for word in all three texts; a test fails when a text
  stops carrying one. Anything that belongs in only one text is written in that
  text.
- **No policy.** The texts say what the channel and its tools do. How a session
  treats a message is the owner's, written in a preset.
- **No full tool name.** A client composes the name of a tool from the server's
  name its own way (`mcp__sidera__send` in Claude Code), and it differs by how the
  server was registered. The texts name a tool only by its MCP name (`recv`), never
  by a client's composed name.
- **The Skill is one file.** A session an adapter starts is forbidden to read the
  data directory, so a file kept beside `SKILL.md` could not be read. The
  procedure is embedded from `procedure.md` and joined to `Full`; the front matter
  description has no colon followed by a space, which YAML would read as a mapping.

## Length limits of the instructions

| Client | Limit | Source |
| ------ | ----- | ------ |
| Claude Code | Server instructions are cut at 4096 characters; `CLAUDE_CODE_MAX_MCP_DESCRIPTION_LENGTH` changes the limit | [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp), "For MCP server authors" |
| Codex | The instructions become the description of the server's tool namespace, in full, when its tools are loaded. When Codex defers the tools (tool search), the listing of namespaces keeps only the first line of the description, cut to 250 characters, and the whole listing is held to 4 KiB | `codex-rs/codex-mcp/src/rmcp_client.rs` (`namespace_description`) and `codex-rs/core/src/context/world_state/tools.rs` (`MAX_NAMESPACE_DESCRIPTION_CHARS`, `MAX_RENDERED_FRAGMENT_BYTES`) in [openai/codex](https://github.com/openai/codex), read on 2026-11-06 |

`Short` is one line, so that the first-line rule loses nothing, and its facts come
in the order of `Full`: what the channel is, what to use it for, to call `recv`
first on a wake-up, and last the acknowledgement rule. Codex keeps the first 247
characters of the line and then writes `...`, so the first three facts are written to
end within 247 characters and the acknowledgement rule may be cut there. A test pins
that, and holds the whole line to Claude Code's 4096 characters.
