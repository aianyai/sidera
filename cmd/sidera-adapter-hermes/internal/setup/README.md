# setup

`sidera-adapter-hermes setup`: the Skill the Hermes agent is given and the steps that
prepare Hermes for the lane. What the Skill says, where the configuration comes from and
why `--apply` does only what it does are in
[`docs/references/adapter-hermes/`](../../../../docs/references/adapter-hermes/README.md#the-skill-and-setup).

## Entry points

| Name | Holds |
| ---- | ----- |
| `Run`, `Env` | The command: flags, reading the configuration, writing the Skill, printing the plan, `--apply`. Exit codes 0, 1, 2. |
| `Skill`, `skill.tmpl` | The Skill text; the template holds the prose, the code fills in the alias list. |
| `Plan`, `Step`, `Platforms` | The steps `--apply` does, placing the Skill in each profile (pure: the profile directories are an argument), and the platforms checked. |
| `ToolsetGap`, `ToolsetAdvice` | The platforms whose agents lack `kanban` or `skills`, and the change to make by hand: the toolset to add, or for a platform with no list the whole list. Pure text; the reading is `kanban.Client.Toolsets`. |

## Invariants

- **No Hermes configuration is changed by this package, and no Hermes command that
  changes it is run.** `hermes tools enable` writes a platform's whole toolset list
  (it dropped `no_mcp` from a list and added toolsets the owner had not chosen), so the
  missing toolsets are named for the owner to add by hand (`ToolsetAdvice`); the only files written are the Skill
  in the state directory and the copy in a profile's `skills` directory (a temporary
  file and a rename).
- **What is written in the state directory is owner-only.** The daemon refuses to start
  an adapter whose state directory holds an entry others can reach; the copy in a profile's
  `skills` directory is Hermes's, with ordinary modes.
- **The notifier setting is read, never changed.** The plan warns of a profile that has
  `kanban.notify_in_gateway` off and prints the command that turns it on; neither the plan
  nor `--apply` runs it.
- **Nothing is restarted.** The output tells the owner to restart the gateways.
- **The Skill never contains an address.** A card names an alias.
- **The Skill names the adapter's board.** Hermes's kanban tools act on the board Hermes
  has selected unless a call passes `board`, so the agent is told to pass it.
- **A Hermes command gets the environment of the adapter's own** (`kanban.ChildEnv`): the
  owner's shell may point at another home or pin a profile, and `setup` reads the
  installation the configuration names.
- **A Hermes command gets no standard input.** The questions of `--apply` are read from it,
  and a child given the same stream would take the answers meant for the next question.
- **The list advised for a platform with no list names toolsets, never a composite.**
  It is the toolsets enabled now followed by the missing ones. A composite name such as
  `hermes-<platform>` means different things on different platforms (it opts in to
  platform-only toolsets on discord, and is not the platform's default on whatsapp_cloud or a
  plugin platform), so it is not a safe stand-in for "the defaults".
- **Toolsets are read the way the gate reads them** (`kanban.Client.Toolsets`), so
  that what `setup` reports and what the adapter's availability says are one answer. A
  platform that cannot be read is said on standard error and left out.
- The text of the Skill describes what the adapter does (`adapter` and `card`): a change
  to the card format, the modes or the inbound flow changes `skill.tmpl` in the same
  commit.
