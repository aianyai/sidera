# handoff

The last mile into a native agent session: the wake-up prompt, the decision that
delivery is blocked, what one `recv` prints, and the check that the model saw it.
Read [../README.md](../README.md) first for the conventions every contract
package follows, and [the handoff reference](../../docs/references/handoff/README.md)
for the reasons behind the rules.

The package decides and formats; it does not lease, acknowledge, queue or talk
to a runtime. Leases, acknowledgements and the feedback sent to senders belong
to the delivery side, which calls into this package.

## Entry points

| Function | Decides |
| -------- | ------- |
| `PromptText(count)`, `NewSessionPromptText` | The wake-up text for waiting messages, and the first prompt of a session created without an initial message. No message body, ever. Provisional wording. |
| `DecidePrompt(PromptInput, PromptParams, now)` | The per-endpoint rule that `DecideNodePrompts` applies: send or hold, with a typed reason, the prompt kind to record and the count for the text. A hold may carry `RetryAt`, when to evaluate again. A `PromptSend` from it alone never authorizes a prompt. |
| `DecideNodePrompts([]WaitingPrompt, running, PromptParams, now)` | Which waiting endpoints of the node are prompted now, so that prompt-started turns never exceed `MaxConcurrentTurns`: one `PromptDecision` per endpoint, ordered earliest waiter first, held ones with `PromptNodeFull`. It calls `DecidePrompt` for each endpoint and adds only the node-wide limit. |
| `DefaultPromptParams()`, `DefaultBlockedParams()` | The decided values of `PromptParams` and `BlockedParams`. |
| `TrimAttempts(attempts, now)` | The attempts of an endpoint that the rules still read, for the caller to keep: the last hour, the newest delivered first prompt with the delivered attempts after it, and the earliest failure of the current failing run. The caller trims through it and keeps nothing else back. |
| `AssessBlocked(BlockedInput, BlockedParams, now)` | Whether delivery to an endpoint is blocked: not blocked, newly blocked (with cause and since), still blocked, or recovered. A short run with an active cause carries `RetryAt`, the first instant the run is longer than the threshold. |
| `Pack(queue, delivery.LeaseID, Budget, cutFor ...Budget)` | The output of `recv`: whole messages that fit, or one part of an oversized first message (the first part not yet acknowledged). The page is bounded by the budget; a message is cut for it and for every budget of `cutFor`. |
| `Part(msg, k, delivery.LeaseID, Budget, cutFor ...Budget)` | The output of fetching part k of a message (`recv --msg <id> --part k` on the CLI). |
| `AckMarkers.Compile()` then `CheckBeforeAck(MarkerSet, observed, expected)` | Whether an adapter may acknowledge for the agent. The markers are the adapter's declaration of how its runtime shows a replaced or cut result; the package knows no runtime. |
| `Budget`, `DefaultBudget()`, `EstimateTokens`, `UTF16Len` | The output budget, the conservative default used when an adapter declares none, and the units budgets are measured in. |

## Rules

Prompts:

- A prompt says only that messages wait and how many. It carries no body.
- An endpoint has at most one unread prompt. A prompt is read when the endpoint
  calls `recv` after the prompt was delivered. Arrivals while a prompt is unread
  or the session is busy add no prompt, so five messages arriving together start
  one turn.
- When a turn ends with the prompt unread, the endpoint is prompted again
  `MaxReprompts` (1) time. A message that arrived after the last delivered
  prompt renews that allowance and wakes the endpoint once `MinInterval` has
  passed since that prompt. Until then the decision is a hold that carries
  `RetryAt`, the end of that interval.
- `PromptDecision.RetryAt` is set for `PromptHourlyCap` (when the cap frees a
  slot), for `PromptRetryAfterFailure` (when the retry is due) and for a hold on a
  message newer than the last delivered prompt that waits for the minimum
  interval (when it has passed). Nothing else brings the
  caller back at those moments, so it evaluates again at `RetryAt` with the same
  `Trigger` as the evaluation that returned it.
- `PromptInput.Pending` counts the messages that still have something not handed
  out: a message handed out in parts counts until its last part has been.
- At most `MaxPerHour` prompts that may have reached the session per hour.
- A prompt that would be sent is held until `RetryAfterFailure` (1 minute) has
  passed since a failed attempt that is the newest of all: reason
  `PromptRetryAfterFailure`, with `RetryAt`. The caller evaluates again then with
  the same `Trigger`.
- A failed attempt is otherwise invisible: it is not an unread prompt, spends no
  allowance and does not count toward the cap. An unknown outcome counts toward
  the cap only.

Node-wide concurrency of prompt-started turns:

- The turns that prompts started and that are still running on one node are
  capped (`PromptParams.MaxConcurrentTurns`). The cap protects the machine's
  CPU, memory and subscription use. It does not count turns a person or another
  tool started. `DecidePrompt` judges one endpoint and does not read it;
  `DecideNodePrompts` is the node-wide entry.
- Only an endpoint that `DecidePrompt` would prompt takes a slot. An endpoint
  that is busy, has nothing pending, is at its hourly cap or has a prompt unread
  keeps its own hold and leaves the slot to the next waiter.
- Of the endpoints that would be prompted, the one that has waited longest goes
  first, until the free slots (the cap less `running`) are used. Ties in
  `WaitingSince` go to the lower endpoint ID in byte order, so the outcome does
  not depend on the order of the input. The others are held with
  `PromptNodeFull`.
- Being held back is not an attempt. The caller records nothing for it, it does
  not count toward `MaxPerHour`, it is not a failure and so never makes delivery
  blocked. Messages stay queued and readable with `recv`.
- A freed slot is not an event of any waiting endpoint, and `RetryAt` covers only
  the hourly cap, the minimum interval and the retry of a failed prompt. The
  principle: whenever free slots may have grown (`running` falls or `MaxConcurrentTurns` rises), the caller
  calls `DecideNodePrompts` again for all waiting endpoints. That happens when
  a turn that a prompt started ends in any way, when a dispatched prompt is
  confirmed not to have started a turn, when an adapter whose turns were counted
  goes offline, and when the cap is raised.
- Each endpoint is passed with the `Trigger` it has pending. A later arrival for
  a held endpoint does not replace `TriggerTurnEnd`; it is kept until a prompt is
  sent to the endpoint.
- A turn whose adapter is offline is not counted in `running`: its state is
  unknown, and a turn nobody can observe must not stop the whole node. When the
  adapter returns and reports the turn still running it is counted again, so
  `running` can exceed the cap; nothing is released until it falls below it.
- An endpoint whose adapter is offline is not in `waiting`: it would take a slot
  for an attempt that is bound to fail, and the failure would feed `AssessBlocked`.

Delivery blocked:

- Blocked means prompts for the waiting messages have been failing for longer
  than the threshold (5 minutes) and a cause is active: adapter offline, runtime
  authentication invalid, or native service unavailable.
- The failing run starts at the first failed attempt after the last delivered
  prompt. While a cause is active it also starts when the cause did
  (`Signals.Since`), or when the oldest waiting message arrived if that was
  later or if the start is unknown, provided no prompt was delivered since: a
  busy agent is not prompted, so its adapter can go offline without any attempt
  failing. The earlier of the two starts the run.
- A busy agent whose adapter is online, or one that leaves a delivered prompt
  unread, is never blocked: no cause is active, or their prompts reach the
  session.
- The verdict is edge-triggered through `Reported`, so one blocked period gives
  one `BlockedNew` and, when it ends, one `BlockedRecovered`.

`recv` output:

- The output is one JSON document and a newline:
  `{"lease":"...","messages":[...],"more":N}`. `lease` is the page identifier
  the caller generated for this `recv` (`delivery.LeaseID`). Sender fields (`id`, `from`,
  and `possibly_duplicate` and `part` when they apply) are separate from `body`.
  It is encoded with `SetEscapeHTML(false)`: non-ASCII text is emitted as is,
  except U+2028 and U+2029, which Go always escapes.
- The budget is checked against the encoded output, in the unit of the budget.
- Messages are taken in queue order while they fit; the first that does not
  fit ends the page. An oversized first message is handed out alone as
  `part k/n`, where k is `Message.NextPart` (0 or 1 means 1): a plain `recv`
  resumes a chunked message at the first part not yet acknowledged, and `Part`
  returns any other. Every part fits. A part that is not the last carries
  `"next":{"msg":"<id>","part":k+1}`, a provisional hint that is neutral to the
  front end: the CLI and the MCP tool each map it to their own call.
- The cut points of a message depend only on its ID, sender and body and on the
  budget, never on the possibly-duplicate flag, the lease, the part number or the
  count of other queued messages. They are cut with the widest of each of those
  reserved, so a later call for another part, made when the flag or the queue
  differ, cuts where part 1 did.
- Only what `Page.HandedOut` lists fits completely and may be leased and
  acknowledged. `Page.ExitCode` is 0.
- `EstimateTokens` is the UTF-8 byte count, an upper bound on any tokenizer's
  count.
- `cutFor` are the budgets of the other front ends of an endpoint. A message is one
  part only when its page fits the budget and every one of them, and every part fits
  all of them, so a message is cut in the same parts whichever front end reads it;
  the page itself is bounded by the budget alone. The cut fits each budget, and does
  not follow the smaller limit, because limits in characters and in tokens have no
  order (a Chinese character is one character and three bytes).
- The package names no runtime and holds no per-runtime limit. Each adapter
  declares the budget of each front end it serves, and the caller passes it to
  `Pack` and `Part`. `DefaultBudget` is for a front end whose adapter declares
  none: the smallest visible limit among the output paths measured
  (`SmallestVisibleLimit`, 10,000 tokens or characters, comparable because the
  estimate never falls below the character count) less a safety margin of 10
  percent (`SafetyMarginPercent`): 9,000 tokens (`DefaultBudgetLimit`). A
  runtime that shows less than that must declare a smaller budget.

Before acknowledging:

- The adapter declares `AckMarkers`: `Replaced` markers for a notice that stands
  in for the result (for example "saved to a file") and `Truncated` markers for
  the place where the runtime cut it. A `Marker` is a literal substring
  (`Literal`) or an RE2 regular expression (`Regexp`, Go `regexp`, linear
  time); exactly one is set.
- `AckMarkers.Compile` validates the declaration once and returns a
  `MarkerSet`: an empty marker, both fields set, a regular expression that does
  not compile and one that matches the empty text are errors wrapping
  `ErrInvalidMarker`. The zero `MarkerSet` declares nothing.
- `CheckBeforeAck` refuses with `AckReplaced` or `AckTruncated` when the observed
  result has more occurrences of a marker of that list than the expected output
  (replaced markers are judged first; zero-width matches are not counted), and with `AckContentMismatch` when the
  expected output is not in the observed result in full. Otherwise it allows
  (`AckSeenInFull`). An expected output that is empty after trimming is always
  refused as a mismatch: a page's output is never empty, so it means the stored
  output is missing, and every text contains the empty text.
  Without declared markers only the content decides: the output seen in full is
  allowed, anything else is a mismatch. There is no
  "unknown runtime" refusal, because containment of the expected output is what
  proves that the model saw it; the markers only give a refusal its cause and
  also refuse a result that carries a notice beside the whole output.

## What the caller must guarantee

- `now` is UTC and passed in; attempts are in ascending time order.
- A message whose `recv`-stage authorization is currently Suspend is not a
  waiting message and counts in none of `PromptInput.Pending`,
  `PromptInput.LastArrivalAt` and `BlockedInput.OldestPendingAt`: suspension
  means no lease, no prompt and no feedback.
- `NewSessionPromptText` is not a prompt `Attempt`: the caller does not record it
  in `PromptInput.Attempts`, and it does not count toward `MaxPerHour`. It
  announces no unread message, so recording it would be read as an unread prompt
  and would hold back the first real message by `MinInterval`. There is one per
  created session, already bounded by the authorization and the rate of session
  creation.
- `Busy` is true from the moment a prompt is dispatched until its turn ends,
  and while the caller holds the session, such as during a close or interrupt.
- The caller records every prompt attempt, with the `Kind` that `DecidePrompt`
  returned, and stores the `BlockedReport` of each `BlockedNew`. It evaluates
  `AssessBlocked` whenever an attempt, a signal or the waiting messages change, and
  at `BlockedVerdict.RetryAt`: nothing else marks the moment a run passes the
  threshold.
- At `RetryAt`, and on any event of an endpoint (arrival, turn end, adapter back
  online), the caller evaluates all waiting endpoints with `DecideNodePrompts`,
  each with the `Trigger` it has pending; a `PromptSend` from `DecidePrompt` alone
  never authorizes a prompt.
- `running` is the number of turns that prompts started on the node, counted
  from dispatch until the turn ends or
  the prompt is confirmed not to have started one. Only prompt-started turns of
  online adapters count. The caller derives it; this package cannot tell an
  offline adapter's turn from a running one.
- `waiting` holds every endpoint with a pending message and an online adapter,
  once per endpoint. An endpoint whose adapter is offline is not put in it.
  `WaitingSince` is nonzero and is the same value as `BlockedInput.OldestPendingAt`:
  the arrival of the oldest pending message, suspended messages excluded. That is
  why earliest-waiter-first tracks the expiry clock. Duplicates, unset endpoints,
  a zero `WaitingSince` and a negative `running` are errors wrapping
  `ErrInvalidNodeInput`.
- The caller sends a prompt only to the endpoints with action `PromptSend` from
  `DecideNodePrompts`, and counts the turn each one starts in `running` from then
  on.
- A decision `PromptNodeFull` records no attempt. The caller evaluates again on
  the events listed under node-wide concurrency: whenever free slots may have
  grown.
- `Signals.Since`, when the active cause started being continuously true, is
  optional precision. Zero means unknown and the cause is then measured from the
  arrival of the oldest waiting message, which can only overstate its duration.
  Blocked is decided without the caller having to record an attempt that was
  never made.
- Because the node cap can make an endpoint wait long, the caller supplies
  `Signals.Since` when an adapter goes offline (the engine always observes that
  instant). Otherwise the time the endpoint was held back would be counted as
  failure time.
- `Message.From` is the sender the channel authenticated and `Body` is valid
  UTF-8.
- `observed` is the text of the tool result as the model received it, never a
  full output that the runtime saved elsewhere. An adapter that cannot obtain
  the model-visible text does not acknowledge on the agent's behalf and leaves
  acknowledgement to an explicit `ack`. Containment decides, so the fidelity of
  `observed` is what prevents a silent wrong acknowledgement; markers cannot
  catch a runtime that stores the full text while showing a preview.
- Marker and budget declarations come from the adapter's manifest; the caller
  compiles the markers once when the adapter is admitted and rejects the
  adapter if that fails.
- `Part` is called with the budget and the `cutFor` budgets `Pack` used for part 1.
  The cut points depend on the message and those budgets, so changing them between
  the two changes the parts.
- The order of one `recv` on the delivery side
  ([delivery](../delivery/README.md)):
  1. Generate one `delivery.LeaseID` for the `recv` (`delivery.NewLeaseID`).
     This is done even when the queue is empty: `Pack` refuses the zero
     `LeaseID` (`ErrLeaseUnset`) for every queue, so a page always carries an
     identifier and its shape does not vary.
  2. Fill `Message.PossiblyDuplicate` from `delivery.Record.PossibleDuplicate(part)`
     for the part the page will hand out (the `Message.NextPart` part for
     `Pack`, `k` for `Part`), and `Message.NextPart` from
     `delivery.Record.NextPart()`: the first part not yet acknowledged, 1 before
     the first hand-out.
  3. Call `Pack` or `Part` with that lease.
  4. Call `delivery.Record.Handout` with the same lease for every entry of
     `Page.HandedOut`. One `recv` hands a page out under one lease, so the
     acknowledgement that names the page's identifier is accepted for each of
     its entries.
- A page prints the lease as its text form, `MaxLeaseLen`
  (`delivery.LeaseIDTextLen`, 32) lowercase hex characters, which JSON does not
  escape.
- `DefaultPromptParams()` is the decided values: 60 prompts per endpoint and
  hour (`MaxPerHour`), 2 minutes between a prompt and the next wake-up by a new
  message (`MinInterval`) and 8 prompt-started turns per node
  (`MaxConcurrentTurns`). A caller may pass other values, for example a lower
  node cap; all three must be positive. An unset node cap is an error, never no
  cap.
