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
| `PromptText(count)` | The wake-up text. No message body, ever. Provisional wording. |
| `DecidePrompt(PromptInput, PromptParams, now)` | Whether to send a prompt now: send or hold, with a typed reason, the prompt kind to record and the count for the text. A hold may carry `RetryAt`, when to evaluate again. |
| `AssessBlocked(BlockedInput, BlockedParams, now)` | Whether delivery to an endpoint is blocked: not blocked, newly blocked (with cause and since), still blocked, or recovered. |
| `Pack(queue, lease, Budget)` | The output of `recv`: whole messages that fit, or one part of an oversized first message (the one after the parts already acknowledged). |
| `Part(msg, k, lease, Budget)` | The output of fetching part k of a message (`recv --msg <id> --part k` on the CLI). |
| `CheckBeforeAck(runtime, observed, expected)` | Whether an adapter may acknowledge for the agent. |
| `DefaultBudget()`, `PathBudgets()`, `EstimateTokens`, `UTF16Len` | The output budget and the units it is measured in. |

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
  slot) and for a hold on a message newer than the last delivered prompt that
  waits for the minimum interval (when it has passed). Nothing else brings the
  caller back at those moments, so it evaluates again at `RetryAt` with the same
  `Trigger` as the evaluation that returned it.
- `PromptInput.Pending` counts the messages that still have something not handed
  out: a message handed out in parts counts until its last part has been.
- At most `MaxPerHour` prompts that may have reached the session per hour.
- A failed attempt changes nothing: it is not an unread prompt, spends no
  allowance and does not count toward the cap. An unknown outcome counts toward
  the cap only.

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
  `{"lease":"...","messages":[...],"more":N}`. `lease` is the ID of the lease the
  delivery side took for what the page hands out. Sender fields (`id`, `from`,
  and `possibly_duplicate` and `part` when they apply) are separate from `body`.
  It is encoded with `SetEscapeHTML(false)`: non-ASCII text is emitted as is,
  except U+2028 and U+2029, which Go always escapes.
- The budget is checked against the encoded output, in the unit of the budget.
- Messages are taken in queue order while they fit; the first that does not
  fit ends the page. An oversized first message is handed out alone as
  `part k/n`, where k is `Message.NextPart` (0 or 1 means 1): a plain `recv`
  resumes a chunked message after the parts already acknowledged, and `Part`
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
  count. `DefaultBudget` is the smallest runtime limit less 10 percent, in that
  unit: 9,000.

Before acknowledging:

- Refuse when Claude replaced the result with a saved-to-file notice, when a
  Codex truncation marker appears, or when the expected output is not in the
  observed result in full. A marker counts only if the observed result has more
  of it than the expected output.

## What the caller must guarantee

- `now` is UTC and passed in; attempts are in ascending time order.
- `Busy` is true from the moment a prompt is dispatched until its turn ends.
- The caller records every prompt attempt, with the `Kind` that `DecidePrompt`
  returned, and stores the `BlockedReport` of each `BlockedNew`.
- The caller evaluates `DecidePrompt` again at `RetryAt`, when one is returned,
  with the `Trigger` it passed the first time.
- `Signals.Since`, when the active cause started being continuously true, is
  optional precision. Zero means unknown and the cause is then measured from the
  arrival of the oldest waiting message, which can only overstate its duration.
  Blocked is decided without the caller having to record an attempt that was
  never made.
- `Message.From` is the sender the channel authenticated and `Body` is valid
  UTF-8.
- `Part` is called with the budget `Pack` used for part 1. The cut points depend
  on the message and the budget, so changing the budget between the two changes
  the parts.
- The lease passed to `Pack` and `Part` is the ID the delivery side generated: at
  most `MaxLeaseLen` (32) bytes of plain text that JSON does not escape, as an
  identifier is. A longer one is rejected.
- `Message.NextPart` is filled by the delivery side from the parts of the message
  the model acknowledged: the number after the last one. It is 0 for a message
  never handed out in parts.
- `PromptParams` has no default: `MaxPerHour` and `MinInterval` are undecided and
  must be configured.
