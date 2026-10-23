# Handoff

The last mile into a native agent session: how an endpoint is woken, when
delivery to it counts as blocked, what one `recv` prints, and how an adapter
checks that the model saw it before acknowledging for the agent.

## What

### Prompts

- A prompt is a short text without a body ("You have 3 new messages. Read them
  with recv."). Sender and body reach the model only through `recv`.
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
  treated like a failure. Neither changes anything outside the endpoint: the
  message stays queued and readable with `recv`.
- Undecided, therefore parameters: the hourly cap and the minimum interval. The
  prompt wording is provisional.

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
  The page carries the ID of the lease the delivery side took for it. Sender
  fields (`id`, `from`) are separate fields from `body`; a message handed out
  again carries `possibly_duplicate`. Encoded with HTML escaping off and
  non-ASCII text as is; Go always escapes U+2028 and U+2029.
- The budget is in the unit of the runtime path (characters or tokens) and is
  checked against the encoded output.
- Whole messages are taken in queue order while they fit. A first message
  larger than the budget is handed out alone as `part k/n`; a plain `recv`
  hands out the part after the ones already acknowledged, so it resumes the
  message, and fetching part k by number returns that part. Every part fits. A
  part that is not the last carries `"next":{"msg":"<id>","part":k+1}`, a
  provisional hint naming the next part; the CLI and the MCP tool each map it to
  their own call.
- Only the messages and parts that fit completely are reported as handed out.
  Leasing and acknowledging them is the delivery side's.
- The exit code is 0 whenever messages are handed out.
- Budgets of the runtime paths: Claude Bash success 30,000 characters, Claude
  Bash failure 10,000 characters, Claude MCP 25,000 tokens, Codex 10,000 tokens.
  The default budget is 9,000 estimated tokens.
- The token estimate is the number of UTF-8 bytes of the output.

### Before acknowledging

An adapter acknowledges for the agent only if the observed tool result contains
the expected output in full. It refuses if Claude replaced the result with a
saved-to-file notice, if a Codex truncation marker appears (`Warning: truncated
output`, `…N tokens truncated…`, `...N tokens truncated...`, and the same with
`chars` or `bytes`), or if the output is simply not there. A marker counts only when the
observed result has more of it than the expected output.

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
  one in; this package only cuts.
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
- **JSON without escaping.** Escaping Chinese text as `\uXXXX` multiplies its
  encoded size by two or more and HTML escaping inflates `<`, `>` and `&`, both
  silently eating the budget. Go's encoder still escapes U+2028 and U+2029,
  which are rare; the budget is checked against the encoded output, so they cost
  size, not correctness.
- **Exit code 0.** Claude shows only about 10,000 characters of the output of a
  command that fails, so a delivery must never exit non-zero.
- **Acknowledge only after comparing.** Acknowledging a message the model did
  not read loses it: the sender is told it was delivered and nobody reads it. A
  refused acknowledgement costs a redelivery marked as possibly duplicate.
  Containment of the expected output, rather than equality, tolerates the
  wrappers runtimes add; a marker is judged by count so that a message that
  discusses truncation can still be delivered.

## Where

- `internal/handoff/prompt.go`: prompt text, parameters, `DecidePrompt`.
  Tests: `TestDecidePrompt`, `TestPromptScenario*`, `TestPromptParamsValidate`.
- `internal/handoff/blocked.go`: `AssessBlocked`. Tests: `TestAssessBlocked`,
  `TestAssessBlockedReportsOnceAndRecoversOnce`.
- `internal/handoff/budget.go`: units, the token estimate, runtime path limits,
  `DefaultBudget`. Tests: `TestEstimateTokens*`, `TestPathBudgets`,
  `TestDefaultBudget*`.
- `internal/handoff/pack.go`: `Pack`, `Part`, the JSON form. Tests:
  `TestPackPaging`, `TestPackEncoding`, `TestPackFiveLargeMessages`,
  `TestPackChineseAndEnglishAreMeasuredInTheirOwnUnits`, `TestPartErrors`,
  `TestPackPartsAreStableForTheSameBudget`,
  `TestPartsDoNotDependOnThePossiblyDuplicateFlag`,
  `TestPartsDoNotDependOnTheLease`, `TestLeaseLength`,
  `TestPackContinuesAChunkedMessage`, `TestPackProperties`,
  `TestExitCodeIsZeroWhenMessagesAreHandedOut`.
- `internal/handoff/preack.go`: `CheckBeforeAck`. Tests: `TestCheckBeforeAck`.
- `internal/handoff/README.md`: entry points and what the caller must guarantee.
