# setup

The client setup of the Hermes adapter, in the contract every adapter's setup speaks
([`sdk/go/adaptersetup`](../../../../sdk/go/adaptersetup/README.md)): the Skill the Hermes
agent is given, placed in each profile whose agents delegate, and the advice on what else
Hermes needs. The program's `setup` command is `adaptersetup.Command` over `Handler`
(`internal/adapter/program.go`); `sidera adapter setup <instance>` runs it. What the Skill
says, which items there are, how a copy is adopted and why nothing here changes Hermes are in
[`docs/references/adapter-hermes/`](../../../../docs/references/adapter-hermes/README.md#the-skill-and-setup).

## Entry points

| Name | Holds |
| ---- | ----- |
| `Handler`, `NewHandler` | The six ops of the contract over one item per profile (`skill:<profile>`): plan, apply, remove, undo, refresh, check; and `Notes`, the advice a plan carries. `NewHermes` in the struct is how a test gives Hermes a script. |
| `Hermes`, `NewHermes` | What setup reads of Hermes: a profile's `config.yaml`, its toolsets, the notifier. Reads only. |
| `Skill`, `skill.tmpl` | The Skill text; the template holds the prose, the code fills in the board and the alias list. |
| `ToolsetGap`, `notifierAdvice` | The advice: the change to make by hand for a platform that lacks `kanban` or `skills`, and the notifier. Pure text; the reading is `kanban.Client`. |
| `platforms.go` | The platforms setup was run with, kept in `setup/platforms.json` beside the record so that a check with no options reads the same platforms. |

## Invariants

- **No Hermes configuration is changed by this package, and no Hermes command that
  changes it is run.** `hermes tools enable` writes a platform's whole toolset list (it dropped
  `no_mcp` from a list and added toolsets the owner had not chosen), so the missing toolsets
  are named for the owner to add by hand (`ToolsetGap.Advice`). The `Hermes` interface has no
  method that writes, so an item that edited a toolset cannot be written without changing it.
  The files written are the Skill in a profile's `skills` directory (`adaptersetup.WriteFile`), the
  record and the remembered platforms.
- **A finding is a note in a plan and drift in a check, never both.** A check is where a person
  looks for what silently stops delegation, so a toolset or notifier finding makes it exit 1; a
  plan has no drift to carry it, so it is a note, shown also when nothing is planned.
- **A record exists only when it holds a Skill.** An apply that did nothing writes none
  (`Record.Installed`), and the record is only read, changed and written back, so the declines
  the SDK keeps in it survive.
- **Setup touches only what it recorded, and replaces a file only after the person said
  yes to the item that says so.** A copy is deleted only while its digest is the recorded one;
  one that was changed stays and leaves the record.
- **A refresh asks Hermes nothing.** It runs from `sidera update`, which must not need a
  running Hermes; it rewrites recorded Skills whose content is out of date and reports the rest.
- **The profiles are those of the options and of the record.** A refresh gets no options and
  must keep every profile that was set up; with neither, the home profile. The platforms are
  those of the options and of the file setup left; with neither, the home platform. A check also
  reads the home profile and the home platform, and `cron` only when it was named.
- **The notifier setting is read, never changed.** The advice names the command that turns it
  on; no item runs it.
- **Nothing is restarted.** The outcome of an item that placed the Skill says to restart the
  gateway that serves the profile.
- **A Hermes command gets the environment of the adapter's own** (`kanban.ChildEnv`): the
  owner's shell may point at another home or pin a profile. It runs through
  `adaptersetup.ClientCommand`: no standard input, a time limit, and Hermes's own message on
  failure.
- **The list advised for a platform with no list names toolsets, never a composite.**
  It is the toolsets enabled now followed by the missing ones. A composite name such as
  `hermes-<platform>` means different things on different platforms (it opts in to
  platform-only toolsets on discord, and is not the platform's default on whatsapp_cloud or a
  plugin platform), so it is not a safe stand-in for "the defaults".
- **Toolsets are read the way the gate reads them** (`kanban.Client.Toolsets`), so that what
  setup reports and what the adapter's availability says are one answer.
- **The Skill never contains an address.** A card names an alias.
- **The Skill names the adapter's board.** Hermes's kanban tools act on the board Hermes has
  selected unless a call passes `board`, so the agent is told to pass it.
- The text of the Skill describes what the adapter does (`adapter` and `card`): a change
  to the card format, the modes or the inbound flow changes `skill.tmpl` in the same
  commit.
