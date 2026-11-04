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
| `Plan`, `Step` | The steps for a set of profiles and platforms. Pure: the profile directories are an argument. |

## Invariants

- **No Hermes configuration file is written by this package.** Hermes's own commands
  change it; the only files written are the Skill in the state directory and the copy in
  a profile's `skills` directory (a temporary file and a rename).
- **What is written in the state directory is owner-only.** The daemon refuses to start
  an adapter whose state directory holds an entry others can reach; the copy in a profile's
  `skills` directory is Hermes's, with ordinary modes.
- **Nothing is restarted.** The output tells the owner to restart the gateways.
- **The Skill never contains an address.** A card names an alias.
- **The Skill names the adapter's board.** Hermes's kanban tools act on the board Hermes
  has selected unless a call passes `board`, so the agent is told to pass it.
- **A Hermes command gets the environment of the adapter's own** (`kanban.ChildEnv`): the
  owner's shell may point at another home or pin a profile, and `setup` changes the
  installation the configuration names.
- **A Hermes command gets no standard input.** The questions of `--apply` are read from it,
  and a child given the same stream would take the answers meant for the next question.
- The text of the Skill describes what the adapter does (`adapter` and `card`): a change
  to the card format, the modes or the inbound flow changes `skill.tmpl` in the same
  commit.
