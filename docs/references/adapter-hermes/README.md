# Hermes adapter

How a Hermes Agent Kanban board becomes a lane to other machines: Hermes creates a
card assigned to `sidera`, a Sidera adapter on the same machine picks it up, hands
the task to an agent on another node and writes the answer back to the card. This
document describes the part of `cmd/sidera-adapter-hermes/` that is committed: the
instance configuration, the card and its validation, the endpoint model, the state
directory and the decisions the adapter makes over them. They are pure functions
and local persistence. The client of the board, which runs `hermes kanban` and
decodes what it prints, has a document of its own: [kanban-cli](kanban-cli.md). The
program that reads the board, talks to the daemon and carries the decisions out is
not in the directory yet, and this document grows with it.

The adapter imports only the `sdk/go` module, as a third-party adapter would.

## What

### Instance configuration

The daemon hands an adapter its configuration as flat values: a string, an integer,
a boolean or a list of strings. `config.Decode` reads them, applies the defaults
and refuses everything it does not understand, a key that is not a setting
included: the alias table bounds what a card can make this node do, and a
misspelled key must not fall back to a default unseen. Every problem is reported
at once.

| Setting | Meaning | Default |
| ------- | ------- | ------- |
| `hermes_path` | The Hermes executable | `hermes` |
| `hermes_home` | `HERMES_HOME` given to every Hermes process; absolute, required | none |
| `kanban_home` | Optional `HERMES_KANBAN_HOME`; absolute | none |
| `board` | The slug of the one board served (`[a-z0-9][a-z0-9_-]{0,63}`) | none |
| `display_name` | What the instance endpoint is called in the directory | `Hermes (<host name>)` |
| `home_profile`, `home_platform`, `home_chat_id` | Where a wake-up goes when a card has no conversation; required | none |
| `home_chat_type`, `home_thread_id`, `home_user_id`, `home_user_id_alt`, `home_parent_chat_id`, `home_guild_id` | The other fields of that notify subscription | none |
| `aliases` | The allow-list of names a card may address, one entry each (below); at least one | none |
| `creator_profiles` | If not empty, only cards created by these profiles are served | any |
| `no_reply_timeout` | How long a card waits for the peer before it is blocked once | 24h |
| `claim_ttl` | How long a claim on a card lasts; longer than `no_reply_timeout` | 168h |
| `session_retention` | How long a conversation's remote session is kept after its last card | 168h |
| `close_window` | How long a cron or orphan card's remote session is kept after the card finished | 1h |
| `endpoint_idle` | How long a conversation endpoint may go unused before it is retired | 720h |
| `poll_interval` | The pause between looks at the board | 10s |
| `max_endpoints` | Endpoints held, the instance endpoint and the orphan endpoint included (4 to 256) | 64 |
| `max_open_sessions` | Remote sessions kept open | 32 |

Durations are Go durations (`90m`, `168h`).

Two timings are not settings: how long an attempt can be sent again as the same
message, and how long the id of a processed inbound message is remembered. They follow
from the delivery timing of the protocol, which the SDK exports
(`adapterproto.SentKeyWindow`, `adapterproto.ReceivedRedeliveryWindow`), so a copy
kept here would drift silently when that timing changes.

An alias entry is `<name>=<address>;modes=<mode>[,<mode>];presets=<preset>[,<preset>]`.
The modes are `new_session` and `message`. Presets are required with `new_session`
and refused without it. The name is lowercased, since Hermes lowercases the assignee
and a card's alias is compared the same way; two entries that differ only in letter
case are a duplicate.

### The card

A card is for the lane when its assignee is exactly `sidera` or starts with
`sidera:` (the label after the colon is only shown), compared without regard to
letter case. `sidera-inbox` is not the lane.

The body is one JSON object, decoded strictly (no unknown member, one value, version
1):

```json
{"sidera": 1, "to": "<alias>", "mode": "new_session" | "message" | "reply",
 "preset": "<preset>", "reply_to": "<request card id>", "task": "<the text for the peer>"}
```

| Mode | Rules |
| ---- | ----- |
| `new_session` | `to` and `preset` required; the alias allows `new_session` and that preset. Starts a session on the alias's runtime endpoint; the task is its first message. |
| `message` | `to` required, the alias allows `message`, no `preset`. A card that continues earlier cards (it has parents) may leave `to` out and goes to the peer of its parent. |
| `reply` | `reply_to` required, no `to`, no `preset`. Answers a request someone sent to this node, back to its sender. |

`reply_to` belongs only to `reply`, `preset` only to `new_session`, `task` is never
empty. A card that fails a rule is invalid, and the reason (`card.InvalidError`) is
written to the card so its creator can fix it. If `creator_profiles` is set, a card
from any other profile is invalid too, a `reply` included, since a reply makes the
node send a message.

### Threat model

Any process of the same operating system user can write the board, so a card is an
untrusted request. What a card can make this node do is bounded by the alias table:
it names an alias and never an address, and only the modes and presets listed for
that alias. The creator list narrows who the agents are, but it is reliable only for
cards the Hermes agent made with its own tool, which fills in the creator itself;
the command line accepts any creator. It is a second line of defense, not a
boundary.

### Endpoints

The adapter holds one direct endpoint per Hermes conversation, besides the instance
endpoint (`hermes`) that is the node's address for new requests. A conversation
endpoint is the key that ties a reply to its card: a remote session answers only the
endpoint that created it, so each conversation has an address of its own and its
cards never meet another conversation's replies.

The key comes from where the card came from (`lane.EndpointKey`):

| Source | Key |
| ------ | --- |
| A chat (platform and chat id) | `chat:<profile>:<platform>:<chat id>[:<thread>]` |
| A cron job | `cron:<profile>:<job id>` |
| Neither | `orphan`, one endpoint shared by all |

Segments have `%` and `:` escaped, so two sources never make one key. A key over the
protocol's bound (`adapterproto.MaxIdentLen`, 128 characters) is cut and ended with
`~` and 32 hex digits of the SHA-256 of the whole key: the same source always gets
the same key. A card does not get an endpoint of its own because an endpoint id is
never released, and a key per card would make the node's configuration grow without
bound.

An endpoint is retired when it has been idle for `endpoint_idle`, or to make room
(below), and only when no unfinished card sends from it and no open remote session
was created by it, since a session can be closed only from its creator. The same key
opens the same address again, so retiring loses nothing but messages sent in the
meantime. `orphan` is never retired.

**Capacity** (`lane.PlaceEndpoint`). The adapter holds at most `max_endpoints`; two
slots are kept for the instance endpoint and the orphan. A card whose key is
registered, or is `orphan`, uses it. A new key takes a free slot; when none is free,
the conversation endpoint idle longest that can be retired is retired; when none can
be, the card is sent from the orphan endpoint and the placement carries a reason for a
comment on the card. The reserved orphan slot is why that always works.

### The state directory

`state.Store` keeps one file, `state.json`, in the instance's state directory (the
directory the daemon names in `initialize`). It holds:

- every claimed card: the conversation endpoint it sends from, its peer address, the
  attempt number and the start of the attempt, the id of the Sidera message the
  attempt created, its phase, the blocks it has had per category, and when it began
  to wait;
- the conversation endpoints with their last activity and the notify fields of the
  card that first opened them;
- the open remote sessions with their last use and when a close was last tried;
- the ids of processed inbound messages with the time each may be forgotten.

`Update` applies a function to a copy of the data, writes the result and returns
only then, so the caller writes a transition before it does what the transition
says, and an update that fails leaves nothing behind. The file is replaced through a
temporary file in the same directory and a rename, owner only. It is not flushed to
disk: surviving a power cut is deliberately out of scope, because the channel
guarantees that a failure is reported to the sender, and the rare loss of the last
transitions appears there as a delivery of unknown outcome. A file the adapter cannot
read, or of another version, makes `Open` fail.

A card's phase is `new` (claimed, nothing sent), `attempting` (attempt number and
start persisted, the send may or may not have happened), `sent` (the message id is
known; waiting for the reply), `blocked` or `finished`. Whether a card holds its peer
(`Waiting`) is true in `attempting` and `sent` only: a blocked card does not, or one
stuck card would stall every card behind it for as long as its creator leaves it.
Whether a reply would still answer the card (`AwaitsReply`) is wider: it also holds
for a card blocked for no reply or by a transient failure, since the peer may only
have been late.
`Prune` forgets a finished card after the retention, except one whose remote session
is still open, because a follow-up needs its parent's peer.

### Decisions

All of them are in `lane` and take the records and the time as arguments.

**Matching a received message to a card** (`MatchReply`). The card is the one at the
endpoint that received the message whose peer is the sender and that awaits a reply:
a card waiting, or blocked for no reply or by a transient failure. A reply that
arrives after the card was blocked is the answer the card was waiting for, so the
adapter completes the card with it, as it would a waiting card; handing it to the
conversation as a new message would answer a request the creator is already being
asked about with something it did not ask. The card stays out of the queue while
blocked, so a late reply and a stuck card never cost the cards behind it their turn.
A card blocked as invalid sent nothing and is never matched. If two cards of one peer
await a reply, the oldest answers, because a peer answers in the order it was asked.
If none matches and some waiting card has no peer yet, the verdict is to read again:
the first reply of a new session can arrive before the adapter has read the control
reply that names the session. The caller reads the pending creations of that
endpoint, records the addresses and asks again with `reread` set; a second miss is a
new message, not a reply. The inbound message id is checked against the table of
processed messages first, so a redelivery after the card was completed is not taken
for a new request.

*Completing a card that is blocked.* `hermes kanban complete` takes a card from
`blocked` straight to `done` (Hermes 0.22.2, `hermes_cli/kanban_db.py`: the update
that completes a card matches `status IN ('running', 'ready', 'blocked', 'review')`).
So the matched card needs no unblock and no new claim first, and the same call works
if its creator unblocked it meanwhile (`ready`) or the adapter claimed it again
(`running`): the fence against a live worker looks for a worker process, and a claim
made from the command line has none. The completion clears the block kind and its
recurrence count, appends no `blocked` event and so is not counted as a block. The
creator, who was woken when the card was blocked, is woken once more by the
`completed` event with the reply as the handoff; that is the same as blocking,
unblocking and completing in turn. A card that its creator moved to `todo` or
`triage` cannot be completed; the client reports that as a permanent failure
(`cannot complete <id> (unknown id or terminal state)`).

**Queueing** (`ShouldQueue`, `NextQueued`). Every send, follow-ups included, waits
while another card at the same endpoint still waits for a reply from the same peer,
because nothing in a reply says which outstanding message it answers. A new session
has no peer and never waits. The oldest queued card goes when the peer is free.

**The idempotency window.** An attempt's idempotency key is the card id and the
persisted attempt number (`IdempotencyKey`), written before the send, so a resend
after a crash is the same message to the channel and a deliberate retry is a new
one. The sender's node keeps a key only until the message's `report_by`. A key that is
forgotten no longer recognizes the resend, which would create a second remote session.
So on recovery an attempt that began longer ago than `adapterproto.SentKeyWindow` is
not sent again: its outcome is unknown. The window is the SDK's, since a value copied
here would drift silently when the protocol timing changes.

The window counts the node's default delivery deadline. It therefore holds only while
the adapter's sends name no `accept_before_s`; a send that names one has a different
key lifetime, and the window would have to be derived from it.

**The redelivery window.** The id of a processed inbound message is remembered from
its first receipt for `adapterproto.ReceivedRedeliveryWindow` (`MarkProcessed`), the
longest time the node may still hand the message out again. After it nothing can
redeliver the message, and the record is dropped.

**A card that comes back** (`OnReady`). A card the adapter already has is listed as
ready again because its creator unblocked it, or because its claim lapsed or was
released:

| The record says | Action |
| --------------- | ------ |
| Not known | Treat as new |
| Finished | Ignore |
| Blocked as an invalid card | Read it again and validate it again |
| Blocked for no reply | Claim and keep waiting without sending; the timer starts again |
| Blocked as transient (failed, unknown outcome, session closed) | Send as the next attempt number |
| Not sent yet | Send as the next attempt number |
| Attempt begun, within the window | Send with the same attempt number |
| Attempt begun, past the window | Outcome unknown |
| Sent | Keep waiting; the timer as it was |

**A card that cannot go on** (`OnFailure`). The reasons are an invalid card,
delivery failed, outcome unknown, remote session closed and no reply (`no reply` is
`NoReplyDue`, true once the wait reaches the timeout). They fall into three
categories: invalid card; transient, which holds delivery failed, outcome unknown and
remote session closed; no reply. Each category has its own Hermes block kind
(`lane.BlockKindOf`, the one mapping), and the three kinds differ on purpose:

| Category | Hermes block kind |
| -------- | ----------------- |
| Invalid card | `capability`: the card cannot be done as written |
| Transient | `transient`: a failure that may be gone on the next try |
| No reply | `needs_input`: the card waits for an answer that has not come |

Hermes counts blocks per kind and sends a card to triage on the second block of one
kind, so two categories sharing a kind would use up the one block of the other. A
card is blocked once per category. The second
time it is completed, saying why, because Hermes puts a card blocked twice for the same
kind into a state where nobody is woken. So: a first timeout blocks the card; once
unblocked it waits again, and a second timeout completes it saying the peer still
did not reply. A first delivery failure blocks it; once unblocked it is sent again,
and a second failure completes it with the reason.

**A follow-up's route** (`RouteFollowUp`). A message card that names no alias goes to
the peer of its first parent that has one, from the endpoint that parent used. If that
peer is a remote session the adapter has closed, the reason is session closed; if no
parent has a peer, the card is invalid.

**Closing remote sessions** (`SessionsToClose`). Each `new_session` leaves a session
endpoint on the other node, which the node's own cap on endpoints would eventually
fill. A session with no unfinished card on it is closed when it has been idle for
`close_window` if it came from a cron or orphan card, which have no follow-ups, or for
`session_retention` if it came from a conversation, whose follow-up may still come.
Beyond that, if more than `max_open_sessions` are open, the least recently used idle
ones are closed. A session in use is never closed to make room, since that would fail
a waiting card. A close answered `endpoint_closed` counts as closed and the session is
forgotten; `close_unconfirmed` keeps it, counted as open, and it is asked again after
`CloseRetryInterval` (10 minutes).

**The parent of an inbound continuation** (`ParentFor`). A message sent to a
conversation's endpoint becomes a request card, and Hermes copies the subscription of
a card's parent onto it, every routing field of the conversation included, which the
command line cannot set. Only a parent that is done makes the child ready at once, so
the parent is the conversation's card that finished most recently. With none, and for
the shared orphan endpoint whose cards come from unrelated places, there is no parent,
and the caller subscribes the notify fields recorded with the endpoint.

## Why

- **Aliases, not addresses.** A card the board holds is not trusted, so the owner lists
  what it may ask for.
- **Strict decoding.** A card or a setting that is slightly wrong is refused with the
  reason, not guessed at; the creator of a card is an agent that can read the reason
  and correct it.
- **Block once, then complete.** Hermes counts blocks of one kind per card and a
  second goes to a state no agent is woken from. The adapter spends the one block it
  has on each category and finishes the card the second time, which wakes the creator.
- **No fsync.** The adapter is a link in a chain whose ends already report failure. A
  transition lost with a power cut shows to the sender as a delivery of unknown
  outcome; a flush on every transition buys nothing the channel does not already say.
- **Decisions apart from effects.** Everything above is a function of records and the
  time, so the rules that decide whether a message is sent twice are tested without a
  Hermes or a daemon.

## Where

| What | Code | Tests |
| ---- | ---- | ----- |
| Kanban client | `cmd/sidera-adapter-hermes/internal/kanban` (see [kanban-cli](kanban-cli.md)) | `*_test.go`, `testdata/` |
| Settings, aliases | `cmd/sidera-adapter-hermes/internal/config` | `config_test.go` |
| Card envelope, validation, assignee, creator | `cmd/sidera-adapter-hermes/internal/card` | `card_test.go` |
| State directory | `cmd/sidera-adapter-hermes/internal/state` | `state_test.go` |
| Endpoint keys and capacity, matching, queueing, recovery, failures, sessions, parent | `cmd/sidera-adapter-hermes/internal/lane` | `lane_test.go` |
