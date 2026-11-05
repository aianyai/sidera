# Hermes adapter

How a Hermes Agent Kanban board becomes a lane to other machines: Hermes creates a
card assigned to `sidera`, a Sidera adapter on the same machine picks it up, hands
the task to an agent on another node and writes the answer back to the card. This
document describes the part of `cmd/sidera-adapter-hermes/` that is committed: the
instance configuration, the card and its validation, the endpoint model, the state
directory, the decisions the adapter makes over them, and the program that talks to
the daemon, receives messages and polls the board. The client of the board, which
runs `hermes kanban` and decodes what it prints, has a document of its own:
[kanban-cli](kanban-cli.md). What the program does with a card it has read
(delegating it) and with a reply (completing the card) is described under
[Delegating, replying, timers](#delegating-replying-timers). What it does with a
message another node sends to it (a new request, or a continuation of a
conversation) is described under [Inbound requests](#inbound-requests). What the
Hermes agent is told, and the command that installs it, is described under
[The Skill and setup](#the-skill-and-setup).

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
| `hermes_path` | The Hermes executable: an absolute path, or a bare name looked up on the `PATH` of the adapter process, which is the `PATH` the daemon was started with (a Hermes installed later is not found until the daemon restarts) | `hermes` |
| `hermes_home` | `HERMES_HOME` given to every Hermes process; absolute, required | none |
| `kanban_home` | Optional `HERMES_KANBAN_HOME`; absolute | none |
| `board` | The slug of the one board served (`[a-z0-9][a-z0-9_-]{0,63}`) | none |
| `display_name` | What the instance endpoint is called in the directory | `Hermes (<host name>)` |
| `home_profile`, `home_platform`, `home_chat_id` | Where a wake-up goes when a card has no conversation; required. A profile name follows Hermes's rule (`[a-z0-9][a-z0-9_-]{0,63}` after lowercasing), since it is the value of `-p` and a name beginning with a dash would be read as an option | none |
| `home_chat_type`, `home_thread_id`, `home_user_id`, `home_user_id_alt`, `home_parent_chat_id`, `home_guild_id` | The other fields of that notify subscription | none |
| `aliases` | The allow-list of names a card may address, one entry each (below); at least one | none |
| `creator_profiles` | If not empty, only cards created by these profiles are served; each name follows the profile rule above | any |
| `no_reply_timeout` | How long a card waits for the peer before it is blocked once | 24h |
| `claim_ttl` | How long a claim on a card lasts; longer than `no_reply_timeout` | 168h |
| `session_retention` | How long a conversation's remote session is kept after its last card | 168h |
| `close_window` | How long a cron or orphan card's remote session is kept after the card finished | 1h |
| `endpoint_idle` | How long a conversation endpoint may go unused before it is retired | 720h |
| `poll_interval` | The pause between looks at the board | 10s |
| `max_endpoints` | Endpoints held, the instance endpoint and the orphan endpoint included (4 to 256) | 64 |
| `max_open_sessions` | Remote sessions kept open | 32 |

Durations are Go durations (`90m`, `168h`). The package manifest declares these
settings with the same keys and defaults, so `sidera adapter enable` and `config`
can set them; a test decodes the declared defaults, so the two cannot differ.

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
(below), and only when nothing depends on it (`lane.InUse`): no unfinished card sends
from it; no open remote session was created by it, since a session can be closed only
from its creator; no request that arrived on it is unanswered, since the answer is
sent from the endpoint that received the request and may come in a later turn; and no
answer sent from it is still watched (`state.Reply.Watched`), since the node's notice
about a failed delivery of the answer comes to the endpoint it was sent from. A request
is answered once its answer is sent or its reply card is over (`state.Reply.Answered`),
and is forgotten after `RequestRetention` (30 days) whether answered or not. So a
request nobody answers keeps its conversation endpoint for those 30 days, and with
`max_endpoints` of them reached nothing can be retired to make room: new conversations
are then sent from the orphan endpoint (below) until a request is answered or its
record is forgotten. The same key opens the same address again, so retiring loses
nothing but messages sent in the meantime. `orphan` is never retired.

**Capacity** (`lane.PlaceEndpoint`). The adapter holds at most `max_endpoints`; two
slots are kept for the instance endpoint and the orphan. A card whose key is
registered, or is `orphan`, uses it. A new key takes a free slot; when none is free,
the conversation endpoint idle longest that can be retired is retired; when none can
be, the card is sent from the orphan endpoint and the placement carries a reason for a
comment on the card. The reserved orphan slot is why that always works.

### The program

`sidera-adapter-hermes` has four commands: `serve`, which the daemon starts,
`package --goos --goarch --binary --out`, which writes a package for
`sidera adapter install`, `setup`, which prepares Hermes for the lane (see
[The Skill and setup](#the-skill-and-setup)), and `version`. Exit codes are the
console client's: 0 done, 1 failed, 2 usage.

**Manifest.** The adapter declares only direct endpoints, up to 256 (the upper bound
of `max_endpoints`; the node's own cap on endpoints applies as well), and no runtime
and no presets: it creates and drives no Hermes process. It requires the `hermes`
program and declares the settings above.

**Start.** `adapter.start` is answered at once. The configuration is decoded, the state
directory (`instance.state_dir`) opened, the board client built and the endpoints the
daemon lists adopted (below); then the loops start. Every call to the daemon that
needs it running (registering and hiding endpoints, reading, acknowledging) happens
from the loops, after the answer: the daemon holds an `endpoint.*` request until it is
running, and waiting for one inside the answer would stall the daemon's own start
until the handshake times out. Registration is retried with a growing pause.

**The daemon capability.** The adapter needs `direct_endpoint_access`: without it a
conversation endpoint could not be hidden from the directory and the adapter could
not tell a sender where Hermes is. On a daemon that does not declare it, `start`
attaches nothing and starts no loop, and the probe reports the dependency `daemon` as
missing with the reason (this daemon cannot give a direct endpoint the directory and
its own declaration; update the node).

**Probe.** The dependency `daemon` (the capability) and the dependency `hermes`,
which is the client's check: the program resolves and is not a Windows batch shim,
its version is in the supported range, `hermes kanban` runs, the board exists, and no
Hermes profile is named `sidera`, `sidera-inbox` or `sidera:*`. When that holds, a
further dependency, `hermes-notifier`, says whether the Kanban notifier is on for the
profiles that wake conversations (below, under availability): `missing` with the reason
when it is off in a profile or cannot be read in any (a profile known to be off is named even
when another could not be read). A last dependency, `hermes-toolsets`, says whether the agent of
the home conversation (`home_profile`, `home_platform`) has the `kanban` and `skills` toolsets
(below, under availability): `missing` with the toolsets that are not enabled, or with the
reason they could not be read.

**Shutdown.** The loops are stopped, whatever the intent. The adapter starts no process
that outlives it (a Hermes command ends with its call, or is killed with its
context), so the intent changes nothing the adapter does; the state needs no cleanup,
since every transition was written before it was acted on.

**Availability.** Before every round of reading, of polling and of the timers the
adapter asks the gate whether Hermes can be used. The gate runs the client's whole
probe (the release, the profiles, the board) the first time, whenever the last check
failed, and every five minutes; in between it runs the one cheap command that needs the
board (listing the boards). It is the probe and not a lighter check because the
adapter must not claim, block or complete cards under a release nobody checked, nor
next to a profile that makes the dispatcher start workers for the lane's cards; a
Hermes upgraded while the adapter runs is found within the interval. When a check
fails, the loop does not touch Sidera or the board, the daemon is told with
`adapter.availability` (condition `dependency_missing`, the cause in the detail) and
the loop tries again after a pause that doubles from one second to two minutes. A
success clears the condition. This adapter declares no runtime endpoint, so the owner reads
the condition in the `availability` of its instance in `sidera adapter list`. Only the change is reported, and the reports reach the
daemon in the order of the changes (the gate holds its lock until the daemon has been
told). There is one full verification at a time: the three loops ask together, and a check
that arrives while one runs waits for it and takes its result, so the notifier is read by one
set of Hermes processes and a slower, older reading never replaces a newer one. A Windows batch shim is not a check of the gate: the client refuses to run it
(`kanban-cli`), and the gate reports that refusal like any other.

**The notifier.** What wakes a Hermes conversation when a card is completed is the
Kanban notifier of a gateway. It is a task of the gateway process, it runs only in a running
gateway, and it does not start when the configuration it reads has
`kanban.notify_in_gateway: false` (Hermes 0.22.2, `gateway/kanban_watchers.py`; the
default is on; the setting is read when the gateway starts). With it off every card is
still written, so nothing is lost, but no agent is woken, which without a message would
look like a peer that never answers. The gate (`kanban.NotifierDisabled`, `hermes -p
<profile> config get kanban.notify_in_gateway`) reads the setting in the same
verification as the probe, for `home_profile`, the `creator_profiles` and the default
profile (below, for why), and reports the condition `dependency_missing` with the detail
"the Hermes gateway's Kanban notifier is disabled in the configuration of profile P
(kanban.notify_in_gateway is false): a gateway started from it wakes no conversation, so
replies are written to cards but nothing wakes the agent". It does not pause receiving or
polling: the condition describes a degraded lane, not an unusable one. It is cleared when a
later verification finds the notifier on everywhere that is read, and replaced by the "not
usable" condition while Hermes itself fails (the notifier is read again when Hermes is
back). A profile whose setting cannot be read is logged and does not hide the others: the
profiles known to be off are reported even then, and when none is known off the last answer
stands. The check does
not see a gateway that is not running, a host started from a profile other than the
default, or the profile of a card from a creator it was not told of (served when
`creator_profiles` is empty; its profile is not known before the card exists).

**The toolsets.** An agent delegates with the Kanban tools (toolset `kanban`) and learns
how to address the lane from the Skill (toolset `skills`, which lets it list and load a
skill). Hermes 0.22.2 leaves `kanban` off by default on every platform
(`hermes_cli/tools_config.py`, `_DEFAULT_OFF_TOOLSETS`) and `skills` on, so a platform needs
`kanban` added, and an owner who picked a platform's list by hand may have left either out.
With `skills` off the agent still has the Kanban tools and no instructions: one created a
plain card on the current board with an assignee of its own choosing, which this adapter never
saw, and the lane looked like a peer that never answers. The gate
(`kanban.ToolsetsMissing`, `hermes -p <home_profile> tools list --platform=<home_platform>`)
reads the two toolsets in the same verification as the notifier, for the conversation the
adapter wakes by default, and reports the condition `dependency_missing` with the detail "the
Hermes agent of profile P on platform X has not enabled the toolset(s) T: it cannot delegate
over Sidera or load the Sidera Skill". It is built like the notifier's: it pauses nothing, it
is cleared when a later verification finds both enabled and replaced by the "not usable"
condition while Hermes itself fails, and a reading that fails is logged and leaves the last
answer. When the notifier and the toolsets are both found wanting the daemon gets two
conditions. The profiles of `creator_profiles` and the other platforms a card may come from
are not read: the home conversation is the one the adapter knows before a card exists, and
`setup` reads every profile and platform the owner names. The answer is the configuration as
Hermes resolves it (a saved list, or the platform's defaults, with `agent.disabled_toolsets`
applied last); it does not show that the gateway was restarted since the file changed.

**Gateway modes.** Hermes runs either as one host gateway that serves every profile (the
default; `gateway.multiplex_profiles` is on unless something prevents it) or as one
gateway per profile (a profile with `gateway.standalone: true`, or a profile whose own
gateway keeps the host from taking it over). The adapter works in both without being told
which, because what it does depends on three things that are the same in both, and where
they differ it covers both (Hermes 0.22.2):

1. *Which gateway delivers a wake.* A gateway's notifier selects the subscriptions whose
   `notifier_profile` is the profile it was started from, a profile that has adapters of its
   own in it, or, in the host, a profile that a `profile_routes` entry pins a chat to
   (`gateway/kanban_watchers_notifier.py`, `_Collector`; `hermes_cli/kanban_db_notify.py`,
   `_notify_profile_filter`), and delivers through an adapter of the profile the
   subscription names (`_adapter_for_subscription`; `gateway/authz_mixin.py`,
   `_adapters_for_profile`): the default profile's bot, a served profile's own bot, or the
   default's bot for a routed chat. With a gateway per profile that is the profile's own
   gateway. The adapter stamps the profile that owns the chat: `home_profile` for the home
   target and, for a conversation, the profile recorded on the subscription the agent's
   own `kanban_create` made (Hermes stamps the session's profile, `tools/kanban_tools.py`),
   which Hermes copies unchanged to a card created with `--parent` (`kanban_db.py`). That is
   what each mode selects on, so the stamp is right in both. `home_profile` must be the
   profile that owns the bot of the home chat.
2. *Which configuration governs the notifier.* The process reads `kanban.notify_in_gateway`
   from the profile it was started from: in the host that is normally the default profile,
   whatever profile a wake is for; in a gateway per profile it is that profile. The command
   line cannot say which arrangement runs (it is process state, not configuration), so the
   check reads the profile's setting and the default profile's, and reports either one being
   off, naming the profile. A setting that is off in a profile the host does not read is a
   warning that does not apply; a host started from a third profile is not seen.
3. *Where the tools and the Skill are read.* In the host each turn runs in the scope of the
   profile that owns the conversation: the profile's `config.yaml` supplies its
   `platform_toolsets` and its `skills` directory supplies the Skill
   (`gateway/run_turn.py`, `_profile_scope_for_source`, `_run_agent`;
   `gateway/run.py`, `_profile_runtime_scope`, `_load_gateway_config`;
   `hermes_constants.get_hermes_home`). In a gateway per profile they are the profile's own.
   `setup` therefore prepares each profile it is given (the Skill file, and a reading of its
   toolsets), and in both modes the gateway that serves the profile has to be restarted
   afterwards.

A wake for an `api_server` chat of a profile other than the host's default is delivered
by a host gateway only when that chat already has a session in that profile's `state.db`:
the chat of that platform is a raw session id and no route can anchor it, so the session
store is Hermes' proof of ownership (`gateway/wake.py`, `session_owned_by_profile`, used
by `_adapter_for_subscription` in `gateway/kanban_watchers_notifier.py`). Until the chat
has one, the host skips the wake, keeps the event and delivers it later; a gateway per
profile delivers without the condition. A home target of that platform in a non-default
profile should therefore be a chat that has had a conversation.

### Endpoints in the program

The instance endpoint, key `hermes`, is registered after start, with the name from
`display_name` and visible in the directory. A message to it is a new request. It is
not recorded in the state: registering the same key returns the same endpoint.

A conversation endpoint is registered the first time a card of its source sends
(`Env.Place`, over `lane.PlaceEndpoint`), and the shared orphan endpoint likewise. The
record of key and endpoint reference is written to the state before the endpoint is
used, and the endpoint is declared hidden from the directory right after it is
registered (`endpoint.declare`, `hidden_from_directory`), so the only address a
stranger learns is the one Hermes sent from. A placement that retires an endpoint to
make room writes the removal first and closes the endpoint second; the endpoint that
stays open after a failed close is listed by the daemon at the next start and recorded
again. A retired key opens the same endpoint when it is registered again.

**After a restart** `adapter.start` lists the direct endpoints that are open. They are
taken as the truth: a listed endpoint the state does not know (the process ended
between registering and recording) is recorded; one whose reference differs is
corrected; a recorded endpoint that is not listed is forgotten and registered again on
its next use. The recorded endpoints are declared hidden again after start, since the
process may have ended between registering and declaring.

**Idle retirement.** Every ten minutes `lane.IdleEndpoints` names the conversation
endpoints unused for `endpoint_idle` that nothing depends on (above); each is recorded as removed and closed with `endpoint.unregister`. The time
of use is the last card placed on the endpoint or message read from it.

### Receiving

One loop reads all the adapter's endpoints. A round reads the endpoints a
`endpoint.pending` notification named, or all of them when the poll is due (every 30
seconds, and in the first round): the notification is best effort. A read never
waits (`wait_s` 0): the adapter reads several endpoints from one loop, and a waiting
read holds one of the connection's concurrent requests for every endpoint.

Rules, and why:

- **Not reading when the card cannot be written.** A lease arises only on a read, and
  every lease that expires without an acknowledgement is one of the message's
  redeliveries; after the third the sender is told the delivery's outcome is unknown.
  A loop that does not read while Hermes is unusable leaves the messages in the node's
  durable queue and uses none of them up.
- **A message is acknowledged only when it is written.** The acknowledgement is what
  tells the sender the message arrived. The message handler decides, with one of three
  verdicts. `Written`: the id is recorded as processed and the message acknowledged.
  `Retry`: a failure of Hermes that may pass; the message is not acknowledged and
  receiving pauses with growing pauses (one second to two minutes). `Skip`: a failure
  that will not pass and that the handler could not route anywhere; the message is not
  acknowledged and the next message is handled. Its lease runs out, it is handed out
  again up to three times, and the sender is then told the outcome is unknown. A
  message that was not written is never acknowledged, since that would be a false
  report of delivery.
- **A message cut into parts is handled when every part is in hand.** The parts are
  read in order from the first (a page that starts at another part is left, loudly),
  put together, and handled once; each part is acknowledged with the lease of the page
  it came on.
- **A processed message is not handled again.** An id recorded as processed (kept for
  `adapterproto.ReceivedRedeliveryWindow` from its first receipt) is acknowledged
  without the handler. The id is recorded before the acknowledgement is sent, so a lost
  acknowledgement is a redelivery that is answered by the table.
- A message that was handled may be handed to the handler again (a lease that ran out
  after the card was written and before the id was recorded), so a handler's effects
  are repeatable.

A message that matches no card the delegation flows can complete, and every message to
the instance endpoint, is handed to the inbound half (`Inbound`; see
[Inbound requests](#inbound-requests)). Where no inbound half is built (a test), a
stand-in logs the message and returns `Skip`, so nothing is acknowledged that was not
written.

### Polling the board

Every `poll_interval` the adapter lists the ready cards, keeps those whose assignee
matches the lane (`card.MatchesAssignee`) and hands each to the card handler. The poll
itself claims nothing: the same card is listed again until the handler claims it. The loop
pauses under the same rule as receiving when Hermes cannot be used.

### Sending

Every send goes through `Env.Send`, which never sets `accept_before_s`.
`adapterproto.SentKeyWindow`, the time during which an idempotency key is certainly
still held and so a resend is recognised, is computed from the node's default delivery
deadline. A send that names another deadline has another key lifetime, and the rule
that an attempt older than the window is not sent again would be wrong for it. A
send that needs a deadline has to derive the window from it first.

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
- the request cards made of inbound messages, with the endpoint that received each
  message, its Sidera id and its sender;
- the cards that answer a request, with the attempt to send, its start and the blocks
  they have had;
- the ids of processed inbound messages with the time each may be forgotten.

A card also records whether the node reported its latest attempt delivered (the adapter
then stops asking), and a session the message id of a close whose reply has not been
read.

`Update` applies a function to a copy of the data, writes the result and returns
only then, so the caller writes a transition before it does what the transition
says, and an update that fails leaves nothing behind. The file is replaced through a
temporary file in the same directory and a rename, owner only. It is not flushed to
disk: surviving a power cut is deliberately out of scope, because the channel
guarantees that a failure is reported to the sender, and the rare loss of the last
transitions appears there as a delivery of unknown outcome. A file the adapter cannot
read, or of another version, makes `Open` fail.

A card's phase is `claiming` (the intent to claim is written; see below), `new` (claimed, nothing sent), `attempting` (attempt number and
start persisted, the send may or may not have happened), `sent` (the message id is
known; waiting for the reply), `blocked` or `finished`. Whether a card holds its peer
(`Waiting`) is true in `attempting` and `sent` only: a blocked card does not, or one
stuck card would stall every card behind it for as long as its creator leaves it.
Whether a reply would still answer the card (`AwaitsReply`) is wider: it also holds
for a card blocked for no reply or by a transient failure, since the peer may only
have been late.
`Prune` forgets a finished card after the retention, except one whose remote session
is still open, because a follow-up needs its parent's peer.

### Delegating, replying, timers

The flows are `LaneFlows` in `cmd/sidera-adapter-hermes/internal/adapter`
(`laneflows.go`, `delegate.go`, `reply.go`, `upkeep.go`). One mutex serializes the
card handler, the message handler and the timers: all three read and move the same
cards, and each is a short run of commands.

**A card seen ready.** A card the state does not know is read (`show`), decoded and
validated against the alias table and the creator list, then placed, claimed,
recorded and started. A known card goes by `OnReady`. Everything that only reads or
can be repeated comes before the claim: a card claimed and then left on a transient
failure would stay claimed, unrecorded, for the whole claim time, and the poll would
never show it again. So the order is: read the card; resolve the source (below);
register the endpoint (`Env.Place`); write the intent to claim (a record in phase
`claiming` that holds nothing but the card id); `claim --ttl` with the configured claim
time; complete the record; send. An invalid card is claimed and blocked as `capability` with
the reason; a card whose mode is `reply` is the inbound half's and is not claimed
(see [Reply cards](#reply-cards)).

*A claim the adapter lost track of.* The intent record exists because a crash between
the claim and the rest of the record would leave the card running under the adapter's
claim for the whole claim time (seven days by default) while the poll, which lists only
ready cards, never shows it again: the card would sit there unnoticed. At a start, and
on every tick for as long as a record is in that phase, the card is read. Running: the
adapter claimed it (nothing else claims a card of this lane), so it is placed and
started as if the claim had just been made; if it cannot be served (it cannot be read
any more, or it turns out to be a reply card, which the adapter never claims) it is
blocked as `capability` with the reason, so its creator knows, and ends in the record
when Hermes no longer has it. Still ready: the claim never happened, and the poll takes
the card like a new one. Any other status: the record is dropped. Only a card the adapter did not know is covered; a known
card that comes back and is claimed again keeps its earlier record until the record
is completed.

*Source.* The first row of `notify-list` names the conversation (profile, platform,
chat, thread). A card with none is subscribed to the home target (`notify-subscribe`)
before it is claimed: the subscription starts caught up on the card's events, so one
made after the card was completed would never wake anybody. A cron job's card is
recognised by its session id (`cron_<job>_<date>_<time>`), anything else with no
subscription sends from the shared orphan endpoint. What was learned is recorded with
the card (`state.Card.Origin`: the source, the target and whether the home target was
the card's only subscriber) before the subscription is made, and a card planned again
(unblocked after a failure, read again after a crash) takes it from the record: the
board then holds the subscription of the home target, which is the adapter's and not
the card's own, and reading it again would move a cron job's or orphan card to the
home chat's endpoint, keep its session as long as a conversation's and send a late
reply to an endpoint the first attempt never used.

*Sending.* The record is written first (attempt number and start), then the message is
sent with the key `<card>:<attempt>` (`lane.IdempotencyKey`). The key is built from the
persisted number, so a send repeated after a crash is the same message to the node and
cannot create a second remote session; a number written only after the send would give
the repeat a new key. A `new_session` card sends `session.create` with the preset and
the task as body to the alias's address; a `message` card sends the task to the
alias's address. A follow-up (the card has parents and names no alias) goes to the
peer of its parent from the parent's endpoint (`lane.RouteFollowUp`), because a remote
session answers only the endpoint that created it. A card whose peer is already
awaited by another card on the endpoint waits (`lane.ShouldQueue`), is commented as
queued once, and goes when the earlier card ends.

A send the node refuses with an application error (invalid address, refused) fails the
card as delivery failed. Any other error leaves the attempt as it is: the next tick
sends the same attempt with the same key while the window lasts
(`adapterproto.SentKeyWindow`), then the outcome is unknown. A card is always sent
through `Env.Send`, which never sets a delivery deadline, which the window relies on.

*What became of the message.* Until the node reports the message delivered, each tick
reads `endpoint.message_status`. The control reply of a `session.create` names the
session: `created` and `unconfirmed` both make it the card's peer and record it as an
open session (an unconfirmed one is commented, and its replies must still match the
card); `failed` fails the card as delivery failed. A rejected or expired delivery
fails the card as delivery failed, a delivery of unknown outcome as outcome unknown.

*Failing a card.* `fail` blocks the card with the Hermes kind of the category
(`lane.BlockKindOf`) and the reason as the block's reason, the first time, and
completes it with the reason as result the second time (`lane.OnFailure`). When Hermes
refuses the block, the card is read: if it is blocked already (the adapter blocked it
just before it ended, or its creator did) the record catches up, if it is finished the
record ends. A block that failed transiently is simply tried again at the next tick
since the record has not changed.

**A reply.** The message handler takes messages of conversation and orphan endpoints
(messages to the instance endpoint are the inbound half's). `MatchReply` finds the
card; if it asks to read again, the pending session creations of that endpoint are
read first (their control replies name the sessions) and it is asked once more. A
message that matches no card goes to the inbound half. For the card:

1. `show` the card. Finished or archived by Hermes: the message is a new message of the
   conversation and goes to the inbound half, unless a run of the card carries this
   message's id in its metadata, in which case this very reply completed it and only
   the acknowledgement was lost: the verdict is `Written`. The same message also
   matches a card that the record shows finished when it names this message as its
   reply (step 3): the card was completed and the adapter ended before it recorded the
   message as processed, and the next start's recovery found the card done and
   finished it in the record before the message came back. Without that match the
   redelivered reply would answer no waiting card and become a card of the inbound
   half, waking the conversation a second time with the same answer.
2. The reply is written to a file under `<state dir>/attach/` and attached, always,
   under a name derived from the Sidera message id (`reply-<16 hex digits of the
   id's digest>.txt`), after `attachments` showed no file of that name (Hermes stores
   a second file of one name as `name (1).ext`, so a replay that attached again would
   leave the reply twice). The name is a digest and not the id because the id is
   chosen by the sender and may be any visible ASCII, a path among it. The file holds
   the remote text until the client removes it after the command, so a start removes
   the `attach-*` files in that directory that a process which died in between left.
3. The message id is recorded on the card (`state.Card.ReplyID`), and then `complete`
   with a summary, a result and metadata holding the message id. The
   summary ("Sidera reply from <alias>; the full text is in the attachment <name>")
   and the result ("The reply of <alias> is attached as <name>.") are the adapter's
   words and hold no remote text and no path. Why: on a completed card Hermes looks
   in the summary and in the result for paths of local files and uploads each file
   that exists to the chat of every subscriber of the card, so a peer that wrote a
   path in its reply could make Hermes send the owner's file to a chat. Hermes also
   wakes the creator with the first line of the summary, which a peer must not be
   able to put its words into. The remote text is only in the attachment, where the
   creator reads it as external content.
4. The record ends; the card queued behind it goes.

A failure of Hermes in these steps that may pass is `Retry`: the message stays
unacknowledged, receiving pauses, Hermes is asked again. One that will not (the card is
gone, its state does not allow the completion) ends the card here and hands the message
to the inbound half. A message is never acknowledged for a card that was not written.

*Why a blocked card takes its late reply.* The peer was late, not wrong, and the
creator is already being asked about the card. Handing the reply to the conversation as
a new message would answer a request with something nobody asked for. Hermes completes
a blocked card directly, so no unblock and no claim come first (see above).

**Timers.** `Recover` runs once after start (and again after a failure): every
unfinished card is read; one Hermes has finished or no longer knows ends in the record.
Cards listed ready again go through the poll. `Tick` runs every ten seconds while Hermes
is usable, under the same pause rule as the other loops: it moves every card on
(`advance`: send what is due, resend an interrupted attempt or declare its outcome
unknown, read what became of a message, block a card whose wait ran out), closes
remote sessions and forgets finished records after `session_retention`. Every five
minutes it also reads the cards it has blocked (`reconcileBlocked`), delegated and
reply cards alike, and ends in the record those Hermes has completed or archived
meanwhile: a person or an agent may complete a blocked card instead of unblocking it,
and nothing else tells the adapter, which would hold the card's remote session and its
endpoint for as long as it runs, or the endpoint that received the request a reply card
was to answer, since the request counts as answered once its reply card is over. The
interval is a command per blocked card and the news is not urgent, since a session is
kept for an hour at least.

*No reply.* A card whose wait has reached `no_reply_timeout` is blocked as
`needs_input`; unblocked it waits again with the timer reset and nothing is resent; the
second time it is completed with "the peer still did not reply after <duration>".

*Closing sessions.* `lane.SessionsToClose` names the sessions due. For each, a
`session.close` control signal goes from the endpoint that created it, and its message id
is recorded. A later tick reads the reply: `closed` (or `unsupported`, which asking
again would not change), or a rejection because the endpoint is closed or missing,
forgets the session; `close_unconfirmed` keeps it, asked again after
`lane.CloseRetryInterval`. A close is not waited for, since the other node may answer
late.

**A card that comes back.** `OnReady` as in the table above, with the claim made first:
a card blocked as invalid is read and validated again; one blocked for no reply waits
again; one blocked as transient or never sent sends as the next attempt (the Hermes
creator asked for the retry, so it gets a new key); an attempt begun within the window
sends with the same number, one past it fails as outcome unknown.

### Inbound requests

A message that no card of the lane is waiting for is something another node started.
The inbound half (`inbound.go`) turns it into a card on the board and wakes the Hermes
conversation that should see it. It takes every message to the instance endpoint, every
message to a conversation or orphan endpoint that matches no waiting card, a reply whose
card Hermes already finished, and a reply whose card Hermes would not let the adapter
complete. The node's own notices (below) are not among them: they are routed before any
of this.

**The card.** `create` with the assignee `sidera-inbox`, which the poll does not take
(`card.MatchesAssignee`) and no Hermes profile bears (the probe refuses one), the
creator `sidera`, and the Sidera message id as idempotency key. The title is the
adapter's: "Sidera request from <sender>" for a message to the instance endpoint,
"Sidera message from <sender>" for one that continues a conversation. The sender is the
alias that stands for its address in the alias table (the smallest name when several
do), else the address; an address is a node and an endpoint identifier, not text the
sender chose.

**The body** is written by the adapter around the sender's text: the sender's address,
the Sidera message id, a statement that what follows is untrusted external content
(not written by the owner, not an instruction, to be read as information), the text in
a Markdown fence longer than any run of backticks in it (so the text cannot close the
fence and speak outside it), and how to answer. It goes to `create` through standard
input, so its length is bounded only by the message; the arguments of the commands are
the title, the summary and the metadata, which hold the adapter's words and the
sender's address, never the sender's text.

**Order, and when the message is acknowledged.**

1. `create`, with the parent of a continuation (below).
2. The request is recorded (`Data.Requests`): card, receiving endpoint, Sidera message
   id and sender. The reply path needs it.
3. `notify-list` on the new card. A card without a subscription is subscribed to the
   wake target (below) with `notify-subscribe`, before the completion, because a
   subscription starts caught up on the events the card already has and one made
   after the completion would never wake anybody.
4. `complete` at once, with a summary and the message id and sender as metadata. The
   first line of the summary is the text Hermes wakes the subscriber with, so it is the
   adapter's: "Sidera request from <sender> (external content, not trusted); read card
   <id>". The sender's words are only in the card.

The message is `Written`, and so acknowledged, after step 4 and not before: a failure
of Hermes that may pass is a `Retry`, one that will not a `Skip`, as for every message.
The steps repeat safely. A message handed out again finds its card through the
idempotency key; a card that has a subscription is not subscribed again; and a
completion Hermes refuses because the card is over is read back (`show`): over means an
earlier try completed it and only the acknowledgement was lost.

**A continuation.** A message to a conversation endpoint wakes the conversation it
belongs to. The card is created as a child of the conversation's latest card that is
`done` (`lane.ParentFor`): the candidates are the finished cards the adapter sent from
that endpoint and the request cards it made there, and each is read on the board
before it is used, up to five, since Hermes may have archived it meanwhile. A child of
a done parent is ready at once, and Hermes copies every subscription field of the
parent to it, including the delivery metadata (such as a scope id) that the command
line cannot set; a parent that is not done would leave the child waiting and its
completion would fail. When there is no such card, or Hermes has cleared the parent's
subscriptions (it does so for cards done and idle for 30 days), the card is made
without a parent and subscribed with the notify fields recorded when the conversation
endpoint was registered (`state.Endpoint.Target`, the source of the first card sent
from it). A message to a `cron:` or the orphan endpoint, and to the instance endpoint,
has no parent and wakes the home target.

**Retention of the requests.** A request record is kept for 30 days
(`state.RequestRetention`) and dropped by the tick that forgets finished cards. The
node admits a reply as a reply only for a day after the delivery and forgets the
message a few hours later, so for most of that time an answer goes out as a plain
message, which needs the sender's address and the id of the message it answers; Hermes
clears the subscriptions of cards done and idle for 30 days, after which nobody could be
woken about an answer anyway.

### The node's own notices

The node tells an endpoint, from its reserved system endpoint, that a message the
endpoint sent was refused, expired or went unanswered, or that a recipient is blocked or
recovered. A page marks such a message `feedback: true` (the adapter's kit declares the
capability that asks for it), and `Message.Feedback` carries it, on a message cut into
parts if any part has it. The text of the notice is the node's, not another agent's, and
it names no message, so the adapter asks `endpoint.message_status` what happened.

The message handler routes a notice before everything else (`laneFlows.HandleMessage`):
it is never matched as the reply of a card, even when its sender is the peer a card
waits for, and never made a request, whose text would be fenced as untrusted. Three steps
follow, in this order, each for the endpoint that received the notice:

1. **Cards in flight are settled** (`laneFlows.settleEndpoint`). Every card of the
   endpoint that was sent and is not yet known to be delivered has its message's status
   read, and the result is applied by the code the tick uses (`settle`): a rejected or
   expired message fails the card as a delivery failure, a delivery that is unknown or a
   close that is unconfirmed as an outcome unknown. The tick would find it too, later;
   the notice is the moment the node says it, so the owner's agent is woken at once. A
   status the node refuses to give (it no longer knows the message) is left to the tick.
   The tick usually comes first, since it reads the status every ten seconds: a card it
   ended for a failed delivery (`state.Endpoint.FailedAt`) explains the first notice
   handled on the endpoint after it (`NoticedAt` is when one was last handled), so that
   a notice that finds nothing in flight is not relayed to the conversation, which the
   blocked card has already woken about the same failure. The mark is made only where a
   notice follows: a session the node could not create is no failed delivery (the signal
   was delivered, through its committed reply) and the node sends nothing for it. A mark
   explains one notice, and only for 30 minutes (`noticeFollowsWithin`): the node merges
   the notices of an endpoint for a minute and holds those over its cap of ten in ten
   minutes, so a notice is due within about eleven minutes of the failure, and one that
   comes later, or a mark no notice used, must not swallow a notice about something
   else. One notice explains all the failures before it; the notice of a second failure
   that follows within the same interval is relayed, which wakes the conversation once
   more and loses nothing.
2. **Answers that did not arrive are reported** (`inbound.reportLostAnswers`). A reply
   card is completed as soon as its answer is sent, so a failure of the answer afterwards
   has no card to land on. The reply record keeps the Sidera message id of the answer, the
   request card it answers and when it was sent, and for `adapterproto.SentKeyWindow` from
   the send (`state.Reply.Watched`) a notice makes the adapter read the status of every
   such answer sent from the endpoint. A message that did not arrive becomes a card of
   the conversation, made and completed like a continuation (below): "Sidera answer not
   delivered", whose first line says that the answer to request card X could not be
   delivered to the sender, and in which state. Its idempotency key is derived from the
   answer's message id, and the record is marked reported (`Reply.Reported`), so a failure
   is reported once however many notices follow and however often a notice comes again.
3. **A notice nothing explained is relayed** (`inbound.HandleNotice`). When steps 1 and 2
   changed nothing and no failure is waiting for its notice, the notice becomes a card of the conversation of the endpoint,
   assigned to `sidera-inbox` like every card the adapter makes of a message, with the
   continuation rules (the latest done card of the conversation is the parent; a `cron:`,
   orphan or instance endpoint wakes the home target) and the Sidera message id of the
   notice as idempotency key. Title and first line of the summary are the adapter's:
   "Sidera channel notice". The body says that the notice comes from this node itself, is
   trusted and is not external content, that it is not a request and must not be answered
   with a reply card, and then quotes the text in a fence. Nothing is recorded as a
   request, so no reply card can name it.

The notice is `Written`, and so acknowledged, when every step that had to write to the
board has succeeded; a failure of Hermes that may pass is a `Retry` and one that will not
a `Skip`, as for every message, so a notice that was not written is never acknowledged.
Every step repeats safely: a card ended in step 1 is no longer in flight when the notice
comes again, step 2 reports a failure once, and step 3 finds its card by the key.

### Reply cards

A card whose mode is `reply` answers a request. The delegation flows list it, see its
mode and leave it to the inbound half (`answer.go`), which validates it, sends its task
to the sender of the request and completes it.

**Validation.** As for any card, the creator list applies (a reply makes the node send
a message) and the envelope must be well formed: `reply_to`, no `to`, no `preset`, a
task. In addition `reply_to` must name a request card the adapter recorded. A card
that fails is blocked as `capability` with the reason, and completed with it the
second time (`lane.OnFailure`).

**Sending.** The answer goes to the sender of the request, from the endpoint that
received the request, with `reply_to` set to the Sidera message id of the request and
through `Env.Send`, so never with a delivery deadline. The idempotency key is the card
id and the persisted attempt number, as for a delegated card, and the attempt is written
before the send. The endpoint is the one that received the message because the node
admits a reply only from the endpoint the message was delivered to. A reply card that
is listed again resumes from the record (`state.Reply`), as `lane.OnReplyCard` decides,
beside `OnReady` and with the same rule for the window (`lane.AttemptExpired`): an answer
recorded as sent is only completed; an attempt under way is sent again with the same
key and in the same form within `adapterproto.SentKeyWindow` and, past it, not at all,
and the card is blocked (`transient`, outcome unknown); a card its creator unblocked is
a new attempt, with a new key. After the send the card is completed with the summary "Sent over Sidera to
<sender>", whose first line is the adapter's.

**The card is not claimed.** Hermes completes and blocks a card from `ready` as it does
from `running`, and a reply card is settled within one poll. Everything the flow
does is repeatable from its record, so a card the flow left halfway is simply listed
again. A claim would add a state that nothing would watch: the flow has no timer, so a
card claimed and then left by a crash or a failing Hermes would stay running for the
whole claim time while the poll, which lists ready cards only, never showed it again.

**When the permission to reply has ended.** The node keeps the record of a received
message only for a while after it arrived (about 27 hours), and the recipient admits a
reply as a reply only within 24 hours of the delivery. A send that names a message the
node no longer holds is refused with `send_refused` and the reason `message_unknown`
(`adapterproto.SendRefusedMessageUnknown`). The flow then sends the same text once as a plain message
to the same address, as a new attempt (the next number, so a new key), with a first line
that says which message it answers ("Reply to your message <id>:"), and comments on the
card that the permission had expired. An answer that arrives late is better than none,
and the line is what lets the sender tell which request it belongs to. The plain
message is not sent when the refusal is any other: the card is blocked as `transient`
with the node's reason, and a second failure completes it with the reason.

**What is recorded, and for how long.** A request record (card, receiving endpoint,
message id, sender) is written when the request card is made and kept for
`state.RequestRetention` (30 days). A reply record (attempt, its start, whether it is
the plain form, the message id, the request card it is for, the blocks per category) is
written with the first attempt, or with the failure that ended the card before one, and
dropped `session_retention` after the card finished. A reply card that does not identify
a request (it is invalid) leaves none.

### The Skill and setup

Hermes needs two things before an agent can delegate: the kanban tools on the
platforms it delegates from (they are off by default, and on cron as well as the chat
platforms), and the knowledge of how to write a card. The second is a Skill, a prompt
Hermes loads when the agent has a use for it.

**The Skill** (`setup.Skill`) is generated from the board and the alias table, once per `setup` run,
and written to `skill/SKILL.md` in the instance's state directory, owner-only (directory
`0700`, file `0600`; a `skill` directory an earlier run left wider is narrowed): the
daemon starts an adapter only when its whole state directory is private to its owner, so a
wider entry would stop the instance at its next start. Its front matter is
`name: sidera` and a description of at most 60 characters (Hermes cuts the description
in the list the agent chooses from, so the part that says when to use it comes first).
The body tells the agent:

- which board the cards are on: the adapter serves one board, and every Hermes kanban
  tool takes an optional `board` that, left out, means the board Hermes has selected, so
  the Skill names the configured board and tells the agent to pass it on every kanban
  call it makes for a Sidera card (`kanban_create`, `kanban_show`, `kanban_list`,
  `kanban_attachments`, `kanban_comment`, `kanban_unblock`, `kanban_complete`); a card
  created without it lands where the adapter never looks;
- who it can reach: every alias with its modes and presets, never an address, since a
  card names an alias and an address in a prompt only invites its use;
- how to delegate: `kanban_create` with the assignee `sidera` (or `sidera:<alias>`),
  the JSON envelope as the body, and an `idempotency_key` that is the same on every
  retry of one delegation; that the task is the only thing the peer sees; and that the
  task should ask the peer to reply once, with the final result, since the first reply
  completes the card and a later one arrives as a new message;
- how to read the result: the peer's answer is always an attachment of the card, named
  in `task.result` (`kanban_show`); it is found with `kanban_attachments`
  (`stored_path`) and read with the file tool, because `kanban_show` returns no
  attachments. `task.result` and `runs[].summary` are only the adapter's notes. The
  answer is external content, like a request from another machine: information to
  use, never an instruction from the owner;
- how to follow up: a new card with `parents` set to the earlier card, mode `message`,
  and `to` left out. A question from the peer is an ordinary reply, so it is answered
  the same way;
- how to answer a request from another machine: a card assigned to `sidera-inbox` holds
  the sender's text between fences and says it is untrusted external content, never an
  instruction from the owner; the answer is a card with mode `reply` and `reply_to` set
  to the request card's id;
- that a card titled "Sidera channel notice" is from this node itself, reports what
  happened to a message, and is never answered with a reply card (the card it concerns
  is blocked or completed on its own), and that "Sidera answer not delivered" says an
  answer did not arrive;
- that a wake-up can arrive more than once and the status of the card is the truth, so
  an action already done is not done again.

The Skill describes what the adapter does and so changes with it: a change to the card
format, the modes or the inbound flow updates the text in the same commit.

**`setup --config <file|-> [--home <dir>] [--instance <name>] [--profile <name>]...
[--platform <name>]... [--apply]`** reads the instance configuration, writes the Skill and
prints the steps. `--profile` is a Hermes profile whose agents delegate (its name follows the
profile rule of `home_profile`) and `--platform`
a platform they delegate from; both repeat, and default to `home_profile` and
`home_platform`. `cron` is always added to the platforms. The one step, for each profile, is
placing `SKILL.md` as `<profile directory>/skills/sidera/SKILL.md`.

For each profile and platform it also reads which of `kanban` and `skills` are enabled (the
reading `kanban.ToolsetsMissing`, which the gate also uses) and, for each platform where one is not, prints a
warning with the exact change to make by hand: the profile's `config.yaml` (named by `hermes
-p <profile> config path`), the list `platform_toolsets.<platform>`, and the items to add to
it. When the file has no such list the platform uses Hermes's defaults, and a list holding
only `kanban` would switch them off; the warning says to write `[hermes-<platform>, kanban]`
instead (Hermes expands the platform's composite toolset and keeps the explicit item:
`tools list` showed 18 toolsets enabled for that list and 1 for `[kanban]`; the composite is
named `hermes-<platform>`, with `-` for `_`). A toolset named in `agent.disabled_toolsets`
stays off whatever the list says, and the warning says so. A platform that cannot be read is
reported on standard error and left out; the run goes on.

*Where the configuration comes from.* The document is the one given to `sidera adapter
enable` or `sidera adapter config` and is decoded with `config.Decode`, so the checks
are the adapter's own. The daemon has no method that returns an instance's
configuration and its data files are not an interface; reading them would repeat the
daemon's file layout and strict decoding. The cost is that the file may differ from
what is running, which is why the owner names it. The state directory is
`<data directory>/adapter-state/<instance>`, the data directory resolved as the `sidera`
command resolves it (`--home`, `SIDERA_HOME`, the platform default); it must exist, so
an instance that was never enabled is refused instead of a directory made for a
misspelled name.

*Why the Skill is a file placed in the skills directory.* `hermes skills install` takes
a registry identifier or an HTTP(S) URL to a `SKILL.md` and has no local path (Hermes
0.22.2 hands a path to the registries as a name and ends with "Could not download",
exit 1). Hermes finds a skill in any directory under a profile's `skills` directory that
holds a `SKILL.md`, and lists one it did not install as `local`, so the file is
placed there. The profile's directory is learned from Hermes (`hermes -p <profile>
config path` names `config.yaml`, which lies in it) rather than computed, so the layout
of profiles stays Hermes's. A run replaces the file of the same name.

*`--apply`.* Without it `setup` only prints, apart from writing the Skill into the
state directory and asking Hermes where a profile lives and what it has enabled (with
`HERMES_HOME` and `HERMES_KANBAN_HOME` from the configuration, the profile pin and the
Kanban worker variables of the owner's shell removed: the environment the adapter gives
its own Hermes commands, `kanban.ChildEnv`; and without standard input). With it each step
is shown and asked (`y/N`; anything but yes skips it), then done: the placing of the Skill
file. A failing step stops the run. `--config -` is refused with `--apply`, which asks on
standard input.

*Why setup does not change toolsets.* `hermes tools enable kanban --platform <platform>`
writes the platform's whole list, not one entry. On Hermes 0.22.2 a profile whose
`platform_toolsets.feishu` was `[image_gen, no_mcp]` held `[feishu_doc, feishu_drive,
image_gen, kanban]` afterwards (`hermes_cli/tools_config.py`, `_save_platform_tools`): the
`no_mcp` entry, which switches off every MCP server on the platform (`_merge_mcp_servers`),
was dropped, so the servers would load there, and two toolsets the owner had not chosen
were added. For a platform with no list (`--platform cron`) it wrote the whole default list
as an explicit one, which no longer follows the defaults of Hermes. What a hand-picked list
means is the owner's to say, so `setup` reads the state and prints the one entry to add; it
does not run a command that rewrites the list, and `--apply` does not either.

*What it never does.* It never edits `config.yaml` and never runs a Hermes command that
does (above). It never restarts a gateway: a restart interrupts the
conversations the gateway is serving, which is the owner's call; the output says the
owner must restart the gateway that serves each profile for the tools and the Skill to
take effect: by default the one host gateway that serves every profile, and for a profile
with its own gateway (`gateway.standalone`) that one.
For each profile it prepares it also reads `kanban.notify_in_gateway` and, when it is
false, prints a warning with the command that turns it on (`hermes -p <profile> config set
kanban.notify_in_gateway true`); the command is advice and `--apply` does not run it, since
the setting may be off on purpose. It reads the profiles it prepares and the default
profile, as the gate does (Gateway modes), and names each one whose configuration has it
off. A profile whose setting cannot be read is reported on
standard error and left out, the others are still named, and the run goes on.
To find the address behind an alias, the output points to `sidera-adapter-console
directory --node <id>`; no directory client is built here. When the alias table changes
the owner runs `setup` again, since the Skill lists the aliases. The output also says that
the Skill names the board, so that the agent and the adapter use the same one; a change of
`board` needs `setup` again too.

### Platforms and operating notes

**Verified.** The adapter has run the whole lane on macOS against Hermes 0.22.2, on
Linux against Hermes 0.22.7 and on Windows against Hermes 0.22.7, on a cluster of three
nodes: delegations to Codex and Claude Code on other nodes with `new_session` and with
follow-ups (in the same remote session too), requests from other nodes, and a delegation
that the agent made itself; on macOS and Linux also a channel notice relayed as a card.
On Windows the Hermes installer put `hermes.exe` under `%LOCALAPPDATA%\hermes\bin`, an
executable the client accepts, and that directory on the user `PATH`.

**A running host serves a new profile.** The host gateway serves every profile, so a profile
created while it runs is served at once, with its own credentials and configuration, unless
it opts out with `gateway.standalone: true` in its configuration (`hermes -p <profile>
config set gateway.standalone true`; Hermes calls it a temporary compatibility setting).
Whoever creates a profile for the adapter or for a test of it should set that first, so
that the host does not start serving it. The adapter neither creates profiles nor sets this.

**A message already sent is not recalled.** Disabling or removing a Hermes instance, or
the adapter's, stops what it does next; it does not call back what it sent. A
`session.create` that is queued for a node that cannot be reached is delivered when the
node is reachable again, as long as the message's queue time lasts (30 minutes by
default), and creates a session there. That session belongs to no card any more: nothing
on this side holds a record of it, no endpoint of ours is left to reply to, and the
answer to its first message has nowhere to go. The owner of the node it was created on
closes it (`sidera endpoint close <address>`); the node's own limit on endpoints counts
it until then.

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

A reply card that is listed again is resumed the same way (`OnReplyCard`):

| The record says | Action |
| --------------- | ------ |
| None | Send the first attempt |
| Finished | Ignore |
| Sent | Only complete the card |
| Blocked | Send as the next attempt number, as a reply |
| Attempt begun, within the window | Send with the same attempt number and the same form |
| Attempt begun, past the window | Outcome unknown |

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

Hermes stores the kind a card was last blocked with, keeps it through an unblock and
clears it only when the card is completed, and sends the card to triage, where nobody
is woken, when it is blocked again with the stored kind (`_route_block`, limit 2). Two
categories sharing a kind would use up the one block of the other. Blocking with
another kind in between would reset Hermes' count, but the adapter does not rely on
that: a card is blocked once per category. The second time it is completed, saying
why, because a repeat of the stored kind would put it where nobody is woken. So: a first timeout blocks the card; once
unblocked it waits again, and a second timeout completes it saying the peer still
did not reply. A first delivery failure blocks it; once unblocked it is sent again,
and a second failure completes it with the reason.

**A follow-up's route** (`RouteFollowUp`). A message card that names no alias goes to
the peer of its first parent that has one, from the endpoint that parent used. If that
peer is a remote session the adapter has closed, the reason is session closed; if the
alias the parent was sent to is no longer in the alias table, no longer names the address
the parent was sent to (for a remote session, its node) or no longer allows `message`,
the card is invalid, blocked as `capability` with a reason that names none of those
values (the table is the allow-list of what a card can make this node do, and a
follow-up reaches the peer through its parent without naming it); if no parent has a
peer, the card is invalid.

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
an endpoint that is not a conversation (the shared orphan endpoint, whose cards come
from unrelated places, and a cron job's, whose messages wake the home target), there
is no parent, and the caller subscribes the notify fields recorded with the endpoint,
or the home target.

## Why

- **Aliases, not addresses.** A card the board holds is not trusted, so the owner lists
  what it may ask for.
- **Strict decoding.** A card or a setting that is slightly wrong is refused with the
  reason, not guessed at; the creator of a card is an agent that can read the reason
  and correct it.
- **Block once, then complete.** Hermes sends a card blocked again with the kind it was
  last blocked with to a state no agent is woken from. The adapter spends the one block it
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
| The program: commands, manifest, endpoints, receiving, polling, probe, and the delegation, reply, inbound and timer flows | `cmd/sidera-adapter-hermes/main.go`, `cmd/sidera-adapter-hermes/internal/adapter` | `adapter_test.go`, `flows_test.go`, `inbound_test.go` and `notice_test.go` (the real adapter against a stub daemon and a fake `hermes`), `gate_test.go` (what the gate tells the daemon over a series of checks, and the probe's notifier dependency), `TestMissingSkillsToolsetIsReportedAndReceivingGoesOn` in `adapter_test.go` (a missing toolset of the home conversation's agent: the condition, the probe's dependency, and receiving going on), `manifest_test.go` |
| Kanban client | `cmd/sidera-adapter-hermes/internal/kanban` (see [kanban-cli](kanban-cli.md)) | `*_test.go`, `testdata/`, `kanbantest/` |
| Settings, aliases | `cmd/sidera-adapter-hermes/internal/config` | `config_test.go` |
| The Skill and `setup` | `cmd/sidera-adapter-hermes/internal/setup` | `setup_test.go` |
| Card envelope, validation, assignee, creator | `cmd/sidera-adapter-hermes/internal/card` | `card_test.go` |
| State directory | `cmd/sidera-adapter-hermes/internal/state` | `state_test.go` |
| Endpoint keys and capacity, matching, queueing, recovery, failures, sessions, parent | `cmd/sidera-adapter-hermes/internal/lane` | `lane_test.go` |
