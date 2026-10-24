# reply

The reply-permission decision: whether a message that says it answers one of
this node's own messages is allowed back in even though the node's policy would
not admit its sender. Read [../README.md](../README.md) first for the conventions
every contract package follows, and
[../../docs/references/envelope/README.md](../../docs/references/envelope/README.md)
(section "Reply permission") for the reasons. The result feeds
`authz.Request.Reply`; the records it reads and applies come from
[`envelope`](../envelope/README.md).

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Admit(Request)` | Decides one reply and applies the evidence it carries. Returns an `Admission`. |
| `Request` | The reply that arrived: time, the owner's switch, the replier (from the connection identity), the local endpoint addressed, `reply_to`, the sender's `envelope.Outbound`, the evidence records and the network key accepted for the replier's node. |
| `Admission` | The `Decision`, the updated `Outbound`, the handling of a control reply, the effect of a report, and why the evidence was refused. |
| `Decision.Granted()`, `Reason` | The verdict, which is the value of `authz.Request.Reply`, and a typed reason. |
| `Window` | 24 hours. |

## Rules

- **Order.** The owner's switch; the node's record of the message and the reply
  being addressed to the endpoint that sent it; the replier's node being a
  recipient's node; the evidence; the replier being a recipient or the bound
  session endpoint; a start for the window; the window.
- **The zero request is off.** `Request.Enabled` is the owner's switch and its
  zero value refuses; a caller that forgets it gets refusals, not permission.
- **Who may reply.** An original recipient of the message, or the session
  endpoint that a verified control reply bound to a `session.create` with a
  non-empty body. Nobody else: not a node that is not a recipient's, not another
  endpoint of a recipient's node, not a session endpoint of a creation that
  failed or had no initial message.
- **Evidence is read only for a recipient's node, and it is checked, not
  skipped.** It must be signed by the replier's node, be about the message in
  `reply_to`, and a status report must be about the replier's own delivery. The
  control reply goes through `envelope.HandleControlReply` and the report through
  `envelope.Outbound.ApplyReport`, the same entries the pull path uses, before
  the replier is looked up: a reply from the new session that arrives before the
  control reply is judged on the state the control reply sets up. Evidence that
  fails makes the reply refused (`ReasonEvidenceInvalid`) and changes nothing.
- **Window.** 24 hours from the delivery time the target reported; else from the
  earliest signed report that proves arrival; else no permission. The window
  ends at the instant start plus 24 hours. A control reply only binds the
  session endpoint; it is never the start.
- **Facts from the pull path.** The decision reads the `Outbound` the caller
  holds, which already contains what earlier pulls and earlier replies taught
  it. The verdict does not depend on the order in which they arrived.

## What the caller must guarantee

- `Replier` comes from the connection identity and the sending node's
  endpoint, never from the message body. `Accepted` is the network key accepted
  for `Replier.Node`, with revocation filled in.
- `Request.Outbound` is the node's record of exactly `ReplyTo`, found by the
  caller, or nil.
- The `Admission.Outbound` and the outcomes of the evidence are committed in the
  same transaction as the decision. The policy is then evaluated with
  `Request.Reply` set to `Decision.Granted()`; `authz` reads it for
  `message.send` only. The evidence is applied even when the policy later
  refuses the reply: it is a true fact either way.
- Reply permission is a message-level verdict for one replier. For a message
  with several recipients, each replier is decided separately.
