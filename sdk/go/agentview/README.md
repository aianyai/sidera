# agentview

The human-readable form of the results that every program showing an endpoint's view of the
network prints: the delivery state of a message and the control reply to it, a page of the
directory, the list of peer nodes and the answer to a send, a reply or a control signal.
`sidera` and the console adapter show the same results;
one implementation keeps them from drifting, and a third-party adapter with a command line of its
own gets the same words and layout by calling it. The package comment is the overview; this file
gives the rules a change has to keep. Imports: the standard library, `adapterproto` and
[`termout`](../termout/README.md) (so `golang.org/x/sys` and `golang.org/x/text` come with it).

## Entry points

| Name | What it is for |
| ---- | -------------- |
| `View{Out, Command}` | A layout over a `termout.Stream` for one program; `Command` spells a command of the program as the reader types it, which every hint is built with |
| `View.Status(now, result)` | A message's delivery, one row for each recipient, and its control reply as fields |
| `View.Directory(page, node)` | A page of endpoints, the sentence for an empty one, and the command for the next page |
| `View.Peers(result)` | The nodes the endpoint can address |
| `View.Sent(kind, id, to, replayed)` | The answer to a send, a reply (`SentReply`) or a control signal (`SentControl`) |

The methods take the results as the adapter protocol names them (`adapterproto.MessageStatusResult`,
`DirectoryResult`, `PeersResult`), so an adapter passes what the daemon returned; a program whose
own interface has other types, as the Agent interface of `sidera` has, maps them first.

## Rules

- **The caller supplies what is its own.** The only program-specific words are the commands in
  the hints, which are spelled by `View.Command` (`status <message-id>`, `directory [--node <id>]
  --cursor <cursor>`); a program that offers those two commands gets hints that work, and nothing
  else here knows who is calling.
- **A hint reaches what the command that printed it acted on.** A program is run on a data
  directory and perhaps an instance, and the command it tells the reader to run next goes to
  the default of both unless it says otherwise. So the hints are never assembled here from a
  bare program name: `Command` adds the flags that select them, where the program's own flags
  go. The words that can hold any text (a cursor) arrive quoted with `termout.ShellWord`; a
  program quotes the values it adds in the same way.
- **A directory page is a page that was given.** `Directory` does not look at the outcome; a
  directory that gave no page is a failure with a reason, which the caller reports. The cursor is
  quoted for the platform the program runs on (`termout.ShellWord`), and the command names the node that was asked,
  because a cursor belongs to it.
- **Peers carry no `Self` column.** The node itself is the row whose trust is `self`; when it
  is the only row, a sentence says that no other node is trusted directly.
- **The session endpoint is printed as the reply carries it.** A `session.create` reply names the
  endpoint without its node, which is the node of the runtime the signal went to; the field is
  labelled `Session endpoint` and is not completed here, since the node that sent the signal
  need not know it. A program that knows the node (it sent the signal) may print the full address
  itself.
- **Protocol words are the protocol's.** Delivery states, a rejection written after its state
  (`rejected (endpoint_declined)`), control outcomes, reasons, conditions and actions (`message.send`,
  `session.create`) appear as the protocol names them, because agents look them up and pass some
  back to a command. Booleans are words.
- **Nothing here is sanitized.** Every value printed is an identifier, an enumeration or a name
  that is checked where it enters the node. A result that one day carries text nobody validates
  goes through `termout.SanitizeLine` where it is printed.
- **Identifiers are whole**, the identifier is the last column of a table, and an empty cell is `-`.

Tests are black-box tables over the three states of a stream: identifiers whole, the text under
the decoration the same in all three, every field of every control reply, the empty sentences, the
command a program names.
