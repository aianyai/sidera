# Handoff

The last mile into a native agent session: how an endpoint is woken, when
delivery to it counts as blocked, what one `recv` prints, and how an adapter
checks that the model saw it before acknowledging for the agent.

## What

### Prompts

- A prompt is a short text without a body ("You have 3 new messages. Read them
  with recv."). Sender and body reach the model only through `recv`.
- A session created without an initial message has nothing to count, so its
  first prompt is a fixed text that says how messages will reach the agent: it
  reads them with `recv` once it is told they have arrived
  (`NewSessionPromptText`). It is not a prompt attempt: the caller does not
  record it and it does not count toward the hourly cap. With an initial
  message the first prompt is the ordinary one for one message.
- A message whose `recv`-stage authorization is currently Suspend is not a
  waiting message: it counts toward no prompt, no blocked verdict and no
  arrival time, because suspension means no lease, no prompt and no feedback.
- Each endpoint has at most one unread prompt. A prompt is read when the
  endpoint calls `recv` after the prompt was delivered. While a prompt is unread
  or the session is busy, new messages add no prompt: the next prompt counts
  them all. Five messages arriving together start one turn.
- When a turn ends with the prompt unread, the endpoint is prompted again once.
- A message that arrives after the last delivered prompt renews that allowance
  and wakes the endpoint, provided the minimum interval since that prompt has
  passed. A message that arrives inside the interval is held with the instant
  the interval ends (`RetryAt`), and the caller evaluates again then; the
  hourly cap names the instant its slot frees in the same way.
- An hourly cap bounds the prompts per endpoint that may have reached the
  session.
- A failed prompt is not an unread prompt, spends no allowance and is not
  counted by the cap. An unknown outcome counts toward the cap and is otherwise
  treated like a failure.
- A prompt that would be sent is held for `RetryAfterFailure` (1 minute) after
  a failed attempt that is the newest of all: the hold carries the reason
  `PromptRetryAfterFailure` and `RetryAt`, the failure time plus that span, and
  the caller evaluates again then with the same trigger. Any other hold keeps
  its own reason. The wait is a rule of the prompt decision, not of its caller,
  so every caller retries at the same pace. Neither changes anything outside the endpoint: the
  message stays queued and readable with `recv`.
- The number of turns that prompts have started and that are still running on
  a node is capped. `DecidePrompt` judges one endpoint and knows nothing of the
  others; a prompt is sent only through `DecideNodePrompts`, which takes all
  waiting endpoints and the count of running prompt-started turns and returns one
  decision for each, ordered by the time the endpoint began to wait and then by
  endpoint ID. An endpoint that
  `DecidePrompt` would prompt is released while there are free slots (the cap
  less the running turns); the rest are held with `PromptNodeFull`. An endpoint
  that `DecidePrompt` would not prompt keeps that decision and takes no slot.
- Being held by the node cap is not an attempt: nothing is recorded, nothing
  counts toward the hourly cap, and no failure exists that could make delivery
  blocked. The message stays queued and readable with `recv`.
- No event of a waiting endpoint marks a slot being freed, and `RetryAt` names
  only the instants of the hourly cap, the minimum interval and the retry of a
  failed prompt. So whenever free
  slots may have grown (the running count falls or the cap rises), every waiting
  endpoint is evaluated again through `DecideNodePrompts`: when a prompt-started
  turn ends in any way, when a dispatched prompt is confirmed not to have started
  a turn, when an adapter whose turns were counted goes offline, and when the cap
  is raised. A later arrival for a held endpoint does not replace its turn-end
  trigger; it is kept until a prompt is sent to the endpoint.
- A prompt counts as running from its dispatch until its turn ends or it is
  confirmed not to have started one. A turn whose adapter is offline is not
  counted: its state is unknown. When the adapter returns and reports the turn
  running, it is counted again and the total may exceed the cap until it falls
  back. An endpoint whose adapter is offline is not among the waiting endpoints,
  or it would take a slot for an attempt bound to fail, and that failure would
  feed the blocked verdict.
- The time an endpoint waited is the arrival of its oldest pending message,
  suspended messages excluded, the same value the blocked rule reads. Because
  the node cap can make an endpoint wait long, the caller gives the instant an
  adapter went offline as the start of that cause; otherwise the time held back
  would be counted as failure time.
- The attempts the caller keeps are cut by `TrimAttempts`, not by the caller: it
  keeps every attempt of the last hour, the newest delivered first prompt and the
  delivered attempts after it however old, and the earliest failure of the
  current failing run. Failed and unknown attempts older than the hour that are
  none of these go. The record of an endpoint therefore stays small, and every
  rule above reads the same result before and after the cut.
- Decided values (`DefaultPromptParams`): 60 prompts per endpoint and hour, a
  re-prompt interval of 2 minutes, a retry a minute after a failed prompt, and 8
  prompt-started turns per node. The
  reasons are under Why. The prompt wording is provisional.

### Delivery blocked

- Blocked: prompts for the waiting messages have been failing for longer than 5
  minutes, and the adapter is offline, the runtime's authentication is invalid,
  or the native service is unavailable. When several apply the cause is chosen
  in that order.
- Failing starts at the first failed attempt after the last delivered prompt.
  While a cause is active it starts no later than the cause itself, or than the
  oldest waiting message if that came later (a cause whose start is unknown is
  measured from that message), unless a prompt was delivered since; so an agent that was busy, and so never prompted, is still blocked when its
  adapter goes offline and stays offline.
- Recovered: the cause cleared, a prompt was delivered, or nothing waits any
  more.
- A busy agent whose adapter is online, or an agent that leaves a delivered
  prompt unread, is never blocked.
- One blocked period yields one "new" verdict and one "recovered" verdict. How
  the sender is told, and how reports are merged and corrected, belongs to the
  delivery side.

### `recv` output

- One JSON document and a newline, `{"lease":...,"messages":[...],"more":N}`.
  The page carries one identifier, a `delivery.LeaseID` the caller generated
  for this `recv` and printed as 32 lowercase hex characters; the zero ID is
  refused, also for an empty queue, so a page always has one. Sender fields
  (`id`, `from`) are separate fields from `body`; a part handed out before
  carries `possibly_duplicate`. Encoded with HTML escaping off and
  non-ASCII text as is; Go always escapes U+2028 and U+2029.
- The budget is in the unit of the runtime path (characters or tokens) and is
  checked against the encoded output.
- Whole messages are taken in queue order while they fit. A first message
  larger than the budget is handed out alone as `part k/n`; a plain `recv`
  hands out the first part not yet acknowledged, so it resumes the
  message, and fetching part k by number returns that part. Every part fits. A
  part that is not the last carries `"next":{"msg":"<id>","part":k+1}`, a
  provisional hint naming the next part; the CLI and the MCP tool each map it to
  their own call.
- Only the messages and parts that fit completely are reported as handed out.
  Leasing and acknowledging them is the delivery side's: it generates the
  identifier before packing, fills the possibly-duplicate flag of each part from
  `delivery.Record.PossibleDuplicate(part)`, and passes the same identifier to
  `delivery.Record.Handout` for every entry of the page.
- The exit code is 0 whenever messages are handed out.
- The cut of a long message can follow more budgets than the page's. `Pack` and `Part`
  take the budget of the page and, beside it, the budgets of the other front ends of
  the endpoint: a message is one part only when its page fits them all, and every part
  fits them all, while the page is bounded by the first alone. The parts of a message
  are counted and acknowledged one by one, so an endpoint read through several front
  ends must get the same parts from each; the cut fits each budget and does not follow
  the smaller limit, because limits in characters and in tokens have no order (a Chinese
  character is one character and three bytes).
- The budget is not fixed by this package. Each adapter declares the budget of
  each front end it serves (a unit and a limit), and the caller passes it in.
  For a front end with no declaration there is a default of 9,000 estimated
  tokens: the smallest visible limit among the output paths measured, 10,000,
  less a margin of 10 percent. A runtime that shows less must declare less.
- The token estimate is the number of UTF-8 bytes of the output.

### Before acknowledging

An adapter acknowledges for the agent only if the observed tool result contains
the expected output in full. It declares, as data, how its runtime shows a
result that was replaced (a notice such as "saved to a file") and one that was
cut (a warning line, or an inserted "N tokens truncated" text). Each is a list
of markers, each marker a literal substring or an RE2 regular expression. The
check refuses with the replaced reason, then the truncated reason, when the
observed result has more occurrences of a marker than the expected output (a
zero-width match is not an occurrence), and with the mismatch reason when the
output is simply not there. Declarations are validated once: an empty marker, a
marker with both forms, a regular expression that does not compile and one that
matches the empty text are errors.

An expected output that is empty after trimming is refused as a mismatch before
anything else is compared.

The observed text must be what the model received as the tool result, never a
full output that the runtime saved elsewhere. An adapter that cannot obtain the
model-visible text does not acknowledge on the agent's behalf; the agent
acknowledges explicitly with `ack`. Containment is the deciding criterion, so
the fidelity of `observed` is what prevents a silent wrong acknowledgement: a
runtime that stores the full text while showing a preview would pass the
containment check against the stored text, and no marker could tell.

The delivery side that these rules call into is described in
[delivery](../delivery/README.md): `delivery.LeaseID` (the page identifier),
`delivery.Record.NextPart()` and `delivery.Record.PossibleDuplicate(part)`.

## Why

- **Prompts carry no body.** A body in the session would let a sender write
  into the model's context under the guise of the channel, and a prompt without
  a body can be repeated or merged at no cost: a duplicate wake-up costs one
  extra turn. That is what makes it safe for a failed or unknown prompt to
  change nothing.
- **One unread prompt, one repeat, a renewal.** A prompt for every message
  would start a turn per message and bury an agent that reads in batches. No
  repeat at all leaves an agent that missed the prompt asleep for good. Unlimited
  repeats loop on an agent that chose to ignore the prompt. One repeat answers
  the first case; the renewal by a new message, after an interval, answers the
  danger of an endpoint that is never woken again after ignoring one prompt.
- **The node cap is a separate function over all waiting endpoints.** Which
  endpoint is released first depends on the others, and a per-endpoint function
  sees only its own input, so putting the order inside `DecidePrompt` would have
  needed the whole node as its input and made every single-endpoint decision
  depend on unrelated endpoints. `DecideNodePrompts` leaves the per-endpoint rules
  where they are and adds only the limit and the order.
- **Earliest waiter first, ties by endpoint ID.** A waiting message expires a
  fixed time after it arrived, so the endpoint that has waited longest is the
  closest to losing its messages; releasing it first gives the held-back time
  the least chance of expiring them. A tie is broken by endpoint ID in byte
  order and not by the order of the input, so the outcome does not depend on how
  the caller happened to collect the endpoints. Rejected: a round robin
  and a random choice both ignore how close each endpoint's messages are to
  expiring, while earliest-waiter-first follows the expiry clock; a random choice
  also has no bound on how long one endpoint waits.
- **Only endpoints that would be prompted take a slot.** Giving a slot to an
  endpoint that is busy, or at its hourly cap, would leave it unused while a
  ready endpoint waits.
- **A held-back endpoint is re-evaluated on every turn end, not at an instant.**
  A hold for the hourly cap or the interval ends at a time that the function
  can name. A hold by the node cap ends when some other endpoint's turn ends,
  which is no instant and no event of this endpoint. Naming `RetryAt` would be a
  guess; the caller instead has the one rule of evaluating all waiting
  endpoints at each prompt-started turn end.
- **Being held back is not an attempt.** An attempt records a prompt that was
  sent. A hold sends nothing and wakes nobody, so counting it toward the hourly
  cap would penalise an endpoint for the node's load, and counting it as a
  failure would report delivery blocked while the adapter is fine.
- **Turns of offline adapters are not counted.** Their state is unknown and may
  never be learned if the adapter does not return; counting them would let a few
  dead adapters stop prompts on the whole node. The cost is that the node can
  briefly run more turns than the cap when they come back, which is why the free
  slots never go below zero and nothing is released until the count falls under
  the cap.
- **A node cap of 8.** The cap decides how many endpoints that wake together
  get a turn before their messages expire. The k-th waiting endpoint, earliest
  first, starts after (k-1) divided by the cap, rounded down, full turns, and
  has to start about a minute before the 30-minute queue time ends, so that its
  `recv` can still lease the message. For a cap C and turns of T minutes that
  admits C x (29 / T rounded down + 1) simultaneous waiters: with 15-minute turns
  2 turns admit 4 endpoints, 4 admit 8 and 8 admit 16; with 5-minute turns 8
  admit 48, close to the 50 endpoints of a node at the sizes the channel is
  designed for. A cap of 2 would let the node's own throttling expire messages,
  so a sender would be told `rejected` for a message the target never refused.
  No cap would let one burst of arrivals start a turn in every session on the
  machine at once. Not counting the throttled time in the queue time was
  rejected: it would need a contract change in `delivery` and a write for every
  entry into and exit from throttling, and a sender could wait about three hours
  without any news. The cap is a default; the owner can lower it, and real turn lengths and resource
  use should decide whether it stays.
- **60 prompts per endpoint and hour.** An endpoint is prompted only while its
  session is idle, so a turn of a minute or more cannot reach 60 in an hour. The
  cap is hit only by a loop of wake-ups that do next to nothing. A cap of 30
  would be hit by quick question and answer exchanges that take two minutes a
  round, and an endpoint at the cap waits up to the queue time for a slot; a cap
  of 120 would protect nothing. How much an agent converses is not what the cap
  limits: loops of `recv --wait` inside one turn are not prompts.
- **A re-prompt interval of 2 minutes.** An agent that responds normally starts
  reading about a minute after a prompt, so an interval of 2 minutes does not
  add a wake-up for it, and it merges a run of messages into one wake-up. A new
  message waits at most 2 minutes for its wake-up, leaving 28 of the 30 queue
  minutes. An endpoint that never reads is woken about 30 times an hour at most,
  below the hourly cap, so the two limits cover different cases: the interval
  the ones that do not read, the cap the ones that read fast. It equals the time
  after which a target counts as unreachable, so both timings of the channel
  run at the same pace. 5 minutes would make a late message wait too long; 1
  minute would wake the endpoint for every message of a run.
- **An unset cap is an error.** Zero meaning no limit would silently remove the
  protection on a configuration that forgot it. `DecidePrompt` does not read the
  cap, but it rejects the same params, so that one value is validated in one place.
- **The cut of the attempts is a rule of this package.** What may be dropped is
  exactly what `DecidePrompt` and `AssessBlocked` do not read: both belong here,
  so a caller that cut the list itself could drop an attempt one of them needs
  (for instance the first failure that blocked delivery is measured from) and the
  cut would have to follow every change of their rules. Keeping the earliest
  failure of the current run, and not all of them, is enough because the blocked
  rule takes only the start of the run from the failures.
- **A failed prompt is retried after a minute, and the rule lives in the
  decision.** A prompt fails mostly because the adapter or the native service is
  not ready yet, for instance a connection that is being put in service. Retrying
  at once repeats the failure and turns an outage into a loop; waiting a day-scale
  span would strand a message that has 30 queue minutes. A minute is long enough
  for a start to finish and costs a waiting message little. The decision carries
  the instant (`RetryAt`) as it does for the cap and the interval, so a caller
  needs no backoff of its own and no special case for the failure it saw; a
  caller that borrowed another subsystem's backoff would retry at that
  subsystem's pace.
- **Failed attempts are not counted by the cap.** The cap protects the agent
  from being woken too often. A prompt that never reached the session woke
  nobody, and counting it would keep a recovered adapter from waking the agent
  for up to an hour after a long outage.
- **Blocked needs a failing prompt and a live cause.** Counting time since the
  message arrived would blame a busy or inattentive agent for the daemon's
  wait. Counting failures without a cause would report passing glitches the
  sender cannot do anything about. The 5 minute threshold fits the pace of
  agent conversations, where a day-scale wait would hide a dead adapter.
- **Edge-triggered reporting.** The pure function is evaluated repeatedly; the
  stored `Reported` value is what turns a level ("still blocked") into the two
  events the sender is told about.
- **Pack in queue order and stop at the first message that does not fit.**
  Skipping ahead to smaller messages would let a stream of small messages starve
  a large one. Stopping costs one more `recv`.
- **Parts depend only on message and budget.** A call for part k must find the
  same cut points that part 1 used, without remembering them. The part label,
  the count of other queued messages, the lease ID and the possibly-duplicate
  flag all change the size of the output, and the last two change between the
  calls for the parts of one message (a lease expires and the message comes back
  flagged). The cut therefore reserves room for the widest of each rather than
  the actual one. Cutting with the actual flag was rejected: parts fetched with
  different flags would overlap or leave gaps, and the model would read a body
  with text missing.
- **A plain `recv` resumes a chunked message.** Otherwise every `recv` would hand
  out part 1 again, and a model that simply calls `recv` would never get past
  it. The delivery side knows which parts were acknowledged and passes the next
  one in (`Message.NextPart`, taken from `delivery.Record.NextPart()`); this
  package only cuts.
- **A retry instant on the holds that wait for the minimum interval.** A message
  that arrives inside the interval raises no event when the interval ends, so
  without the instant it would be woken only by the next unrelated arrival or
  turn end, possibly never.
- **Blocked is judged from the cause as well as from failed attempts.** A busy
  agent is not prompted, so a dead adapter leaves no failed attempt; relying on
  the caller to record attempts that were never made would leave exactly that
  case undetected. The cause's own start time closes it, and when the start is
  unknown the oldest waiting message stands in for it, which can only overstate
  the duration; a caller that forgets the start still detects the case. An
  online adapter still gives no cause and so no blocked verdict.
- **Front-end neutral continuation hint.** The CLI and the MCP tool call the
  next part differently, so the page names the message and part and each front
  end turns it into its own call. The shape is provisional.
- **Bytes as the token estimate.** No tokenizer spends more tokens than there
  are bytes, so the byte count cannot under-count: dense ASCII, Chinese
  characters and emoji are all covered, with no per-script table to maintain.
  It also never falls below the character count, which lets limits in characters
  and in tokens be compared and the smallest taken as one budget. Rejected: a
  quarter of a token per ASCII byte, the usual average, which under-counts
  digits, punctuation and code and would let a truncated result through, and a
  fixed per-script ratio for CJK text, which has no stable value across
  tokenizers. The cost is that English text is split into more parts than
  needed.
- **A margin of 10 percent.** Runtimes add their own text around a result
  (status lines, tags) and their limits are measured on one version and may
  move; a tenth is far more than the wrappers seen so far take.
- **A default budget from the smallest visible limit.** It applies where an
  adapter declares nothing, so it must be safe on every path measured: the
  smallest limit, less the margin. Limits in characters and in tokens compare
  because the estimate is never below the character count.
- **JSON without escaping.** Escaping Chinese text as `\uXXXX` multiplies its
  encoded size by two or more and HTML escaping inflates `<`, `>` and `&`, both
  silently eating the budget. Go's encoder still escapes U+2028 and U+2029,
  which are rare; the budget is checked against the encoded output, so they cost
  size, not correctness.
- **Exit code 0.** A runtime may show far less of the output of a command that
  fails (about 10,000 characters on one measured path), so a delivery must never
  exit non-zero.
- **Markers and budgets are the adapter's data, not the core's code.** The way a
  runtime marks a replaced or cut result, and how much of a result the model
  sees, belong to that runtime and change with its versions. Naming runtimes in
  the core would make every new runtime and every upstream change a change to a
  package that all adapters share. The adapter declares both and the core only
  compares. Rejected: a table of runtimes in the core (the situation this
  replaces), and one fixed set of markers for all runtimes (a marker of one
  runtime would be matched against another's output, and a message that
  discusses it would be refused).
- **No "unknown runtime" refusal.** With no markers declared the check cannot
  name a cause, but it can still tell whether the model saw the output: only if
  the output is contained in full. Refusing everything for lack of markers would
  turn a missing declaration into messages that are never acknowledged, and
  accepting without comparing would lose messages. Containment is the rule that
  protects the message; markers refine the cause. A result that holds the whole
  output beside a declared notice is still refused, as the notice says the
  model may not have seen it; an adapter that declares no markers loses that
  extra caution and relies on containment alone.
- **Regular expressions that match the empty text are rejected, and zero-width
  matches are not counted.** Such a marker is found at every position, so it
  would refuse every result. A pattern like `\b` does not match the empty
  string as a whole but matches empty at word boundaries, and a wrapped result
  has more of them than the output; counting only non-empty matches keeps a
  normal wrapped result accepted. RE2 is used
  because its matching time is linear in the text, which matters as the markers
  come from an adapter package.
- **A fixed first prompt for a session with no initial message.** The session
  needs a first input so that there is evidence it is ready, but there is no
  message to count. The text says how messages will be announced and asserts no
  state; it has no body, like every prompt. It is not recorded as an attempt: it
  announces no unread message, so a recorded attempt would look like an unread
  prompt and hold back the first real message by the minimum interval. One is
  sent per created session, which authorization and the rate of session
  creation already bound.
- **An empty expected output is refused.** The output of a page is never empty,
  so an empty value means the stored output is missing. Every text contains the
  empty text, so the containment test would accept anything, and acknowledging
  would silently lose a message that was never compared. Refusing costs a
  redelivery and makes the missing output visible.
- **Acknowledge only after comparing.** Acknowledging a message the model did
  not read loses it: the sender is told it was delivered and nobody reads it. A
  refused acknowledgement costs a redelivery marked as possibly duplicate.
  Containment of the expected output, rather than equality, tolerates the
  wrappers runtimes add; a marker is judged by count so that a message that
  discusses truncation can still be delivered.

## Where

- `internal/handoff/prompt.go`: prompt text, parameters and their decided
  values (`DefaultPromptParams`), `DecidePrompt`.
  Tests: `TestDefaultPromptParamsValidate`, `TestDefaultPromptParamsValues` (each
  value, named after its reason), `TestDefaultPromptParamsReasons` (the figures
  above recomputed from the defaults with a separate count), `TestPromptText`,
  `TestNewSessionPromptText`, `TestNewSessionPromptIsNotAnAttempt`,
  `TestDecidePrompt`, `TestPromptScenario*`, `TestPromptParamsValidate`.
- `internal/handoff/nodeprompt.go`: `DecideNodePrompts`, `WaitingPrompt`,
  `NodePromptDecision`. Tests: `TestDecideNodePrompts` (one row per rule),
  `TestDecideNodePromptsRejectsInvalidInput`,
  `TestDecideNodePromptsDoesNotModifyItsInput`,
  `TestNodePromptScenarioFreedSlotReleasesWaitersOldestFirst`,
  `TestNodeCapHoldIsNeitherAnAttemptNorBlocked`,
  `TestDecideNodePromptsProperties` (fixed seed, declarative assertions written
  in the test, coverage assertions), and the cap rows of `TestPromptParamsValidate`.
- `internal/handoff/attempts.go`: `TrimAttempts`. Tests:
  `TestTrimAttemptsKeepsOnlyWhatTheRulesStillRead`.
- `internal/handoff/blocked.go`: `AssessBlocked`. Tests: `TestAssessBlocked`,
  `TestAssessBlockedReportsOnceAndRecoversOnce`.
- `internal/handoff/budget.go`: units, the token estimate, `DefaultBudget` and
  the constants it derives from. Tests: `TestEstimateTokens*`,
  `TestDefaultBudget`, `TestDefaultBudgetFitsEveryPath` (the limits of the
  measured runtimes are test data there).
- `internal/handoff/pack.go`: `Pack`, `Part`, the JSON form. Tests:
  `TestPackPaging`, `TestPackEncoding`, `TestPackFiveLargeMessages`,
  `TestPackChineseAndEnglishAreMeasuredInTheirOwnUnits`, `TestPartErrors`,
  `TestPackPartsAreStableForTheSameBudget`,
  `TestACutFitsEveryBudgetItIsGivenWhicheverOneAsks`,
  `TestPartsDoNotDependOnThePossiblyDuplicateFlag`,
  `TestPartsDoNotDependOnTheLease`, `TestLeaseLength`,
  `TestPackContinuesAChunkedMessage`, `TestPackProperties`,
  `TestExitCodeIsZeroWhenMessagesAreHandedOut`.
- `internal/handoff/preack.go`: `AckMarkers`, `Marker`, `MarkerSet`,
  `CheckBeforeAck`. Tests: `TestCheckBeforeAck` (the markers of two runtimes
  are test data), `TestCheckBeforeAckZeroMarkerSet`, `TestCompileMarkers`,
  `TestCheckBeforeAckAcceptsPackedOutput`, `TestCheckBeforeAckProperties`.
- `internal/node/engine/prompts.go`: the caller of the prompt rules. It asks
  `DecideNodePrompts` for every waiting endpoint whenever free places may have grown or an
  endpoint has an event, counts `running` from the dispatch of a prompt, leaves out the
  endpoints and the turns of an adapter that is offline, and records each attempt before
  the adapter is called. See [the engine reference](../engine/README.md#what) ("Prompting")
  for the order and the tests.
- `internal/handoff/README.md`: entry points and what the caller must guarantee.
