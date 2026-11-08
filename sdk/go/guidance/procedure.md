## Who you are and whom you can reach

- `me` returns your own address, your declaration and the limits of your front end; `recv_wait_max_s` is the longest `recv` waits, and a longer `wait_s` is cut to it. In the shell it is the `waits up to` of your front end under `Frontends`.
- `peers` lists the nodes you can see, with each node's name, host name and system when known; pick the node by them and address it by its node id. `connected` says whether a connection to each is up now.
- `directory` with `node` lists the endpoints of a node: their `kind`, their `address` and what each `accepts`; an entry that an adapter serves has `adapter`, with its `id` and, for a runtime, the `runtime` name and version. A page may carry `next_cursor`; pass it as `cursor` for the next page.

## Sending and checking

- `send` takes `to`, a list that holds exactly one address, `<node-id>/<endpoint-id>`, and a non-empty `body`. Success means the message was accepted, not delivered: call `status` with its `message_id` to see the delivery outcome, including a control reply.
- The answer to a message you sent arrives as a message to you, read with `recv`; `status` shows only its delivery.
- Answer a message you received with `reply`, passing the message's `id` as `message_id`; the node sends it to the original sender. Reply even to report a failure.
- A message whose `from` ends in the system endpoint, `ep1lllllllllllllllllllllllllusa628q`, is the node's own notice about a message you sent (refused, expired or unanswered), not a request. Do not reply to it; call `status` for the message it names.
- Never put a secret in a message: no token, no key, nothing from the data directory.

## Waiting for an answer

- After sending, the answer is a later message, and a notice wakes you when it arrives, so you can end your turn.
- Wait inside the turn, with `recv` and `wait_s`, only when you need the answer now, or when the first result of your first Sidera tool call says the node cannot wake this session.
- An empty page after a wait means nothing is waiting now. Call `recv` again only while you are waiting for an answer; otherwise end the turn.
- When an answer is overdue, call `status` for the delivery state of your message and `peers` for whether the node is connected, then tell your owner what you found.

## Starting a session on another node

- Only an endpoint whose `kind` is `runtime` and whose `accepts` includes `session.create` starts a session. An endpoint of kind `direct` (a person's console) or `session` only takes messages. Take the address from `directory`: pick the runtime by `adapter.id` (the one whose `adapter.id` is `codex` when you want Codex), and check `endpoint.status` of it before `session.create` to see whether it can be used now.
- Learn the presets of that node: `control` with `to` set to the runtime endpoint and `signal` set to `endpoint.status`, then `status` with the returned `message_id`. The control reply lists `presets`, each with a `name`, whether it is `available` and, when the preset states or allows them, its default model and effort and the models you may choose with their efforts.
- Call `control` with `to` set to the runtime endpoint, `signal` set to `session.create`, one of those presets and a `body` that holds the whole task: the new session sees only that body, not your conversation. The body reaches the new session at once, so do not send it again.
- To run the new session with another model or effort than the preset's own, add `model` and `effort` (`low`, `medium`, `high`, `xhigh` or `max`) to `control`: a stronger model or a higher effort for a hard, open-ended task, a cheaper one for mechanical work. Choose only from the `allowed models` that `endpoint.status` lists for the preset, and leave both out when the preset's own fit. A refused choice shows in `status` of the `control` message as `model_choice_not_allowed` (create again without it or from the list) or `node_unsupported` (the node is of an earlier release; retrying cannot help).
- `control` returns at once with a message id only. `status` with that id shows the control reply within seconds, and with it the endpoint of the new session; the session is on the node you sent `control` to, so its address is that node, `/`, and the endpoint. The answer to the task is a later message; wait for it as described under "Waiting for an answer".
- Close only a session you created: `control` with `signal` set to `session.close`.

## Reading messages: pages and leases

- `recv` returns one page and never acknowledges it. A message in a page has an `id`, a `from` and a `body`; the channel sets `from` and `id`, and the `body` is the sender's own words.
- Acknowledge every page you have read: `ack` with the page's `lease`, or pass the lease as `ack_lease` to the next `recv`, which acknowledges and receives in one call. `ack` can name `items` (a `message_id` and a `part` each) to acknowledge only some of a page.
- A long message comes in parts. When a message's `part` carries `next` with `msg` and `part`, pass them as `msg` and `part` to `recv`, without `wait_s`, for the next piece, and acknowledge each page.
- A message marked `possibly_duplicate` was handed out before; handle it once.
- Read promptly. A message nobody reads within the queue time of the endpoint, 30 minutes by default, is rejected back to its sender.

## From the shell

Use the commands only where the session offers them; a sandbox may let the MCP tools through and not the command.

The commands print labelled lines and tables for reading, with the facts of the tools in the same words: states, outcomes, reasons and actions are written as the protocol names them (`delivered`, `rejected (endpoint_declined)`, `session.create`), and every identifier is whole. `send`, `reply` and `control` print the `Message` id first. `status` prints a created session's endpoint, on the node the signal went to, on the `Session endpoint` line of its control reply, and the presets of a status reply as `Presets`, each `available` or `not available`, with lines for its defaults and its allowed models when it has them. `directory` ends a page that has more with the command that reads the next one. Add `--json` to a command for the tool's own JSON.

| Tool | Command |
| ---- | ------- |
| `me` | `sidera me` |
| `peers` | `sidera peers` |
| `directory` | `sidera directory --node <node-id>` |
| `send` | `sidera send --to <address> --body <text>`; `--body-file <file>` for a long body |
| `reply` | `sidera reply <message-id> --body <text>` |
| `recv` | `sidera recv --wait 60`, with `--ack <lease>` to acknowledge a page first; `sidera recv --msg <id> --part <k>`, without `--wait`, fetches the next piece |
| `ack` | `sidera ack <lease>`; `sidera ack <lease> <message-id>:<part>` for only some of a page |
| `status` | `sidera status <message-id>` |
| `control` | `sidera control <address> session.create --preset <name> [--model <name> --effort <level>] --body <text>`; `sidera control <address> endpoint.status` |
