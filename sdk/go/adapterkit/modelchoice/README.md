# adapterkit/modelchoice

The allowlist of a preset and the rules that decide which model and effort a
requester may choose for a new session. An adapter that declares `model_choice`
reads its preset's allowlist with `ParseAllowed`, reports it with `Entries`, and
turns a `session.create` into the pair the session runs with with `Resolve`. The
daemon does not read an adapter's preset keys, so only the adapter can apply the
rules, and every adapter must apply the same ones: they live here once. Read
[the adapter-protocol reference](../../../docs/references/adapter-protocol/README.md)
for the capability and the refusal it maps to.

It imports the standard library and `adapterproto`.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Key` | The preset field that holds the allowlist, `allowed_models`. An adapter declares it as a `map` whose value is a `string_list`. |
| `ParseAllowed(raw, supported)` | Reads the value of `Key` (the JSON of the map). `supported` is the efforts the adapter declares in `model_choice`. Every refusal is a problem of the preset. The zero `Allowed` is a preset with no allowlist. |
| `Allowed.Entries()` | The allowlist as `[]adapterproto.ModelAllowance`, sorted by model with the efforts of each in the order of the scale: what `PresetStatus.AllowedModels` requires. `nil` for the zero value. |
| `Defaults`, `Choice` | The preset's own model and effort, and the pair a session runs with. An empty field is left to the runtime. |
| `Resolve(allowed, defaults, model, effort)` | The fallback rules. Returns the `Choice`, whether the requester chose at all, and an error that wraps `ErrNotAllowed`. |
| `ErrNotAllowed` | The adapter answers `model_choice_not_allowed` for it; its text says which rule refused and goes to the log, not to the requester. |
| `MaxAllowedModels` | The bound on the models of one allowlist, the protocol's own (`adapterproto.MaxAllowedModels`). |

## Rules

- **An allowlist is never empty.** An empty map, a model without an effort and a
  value that is not a map of string lists are refused by `ParseAllowed`. An empty
  list is not read as "every effort": a runtime that adds a level later must not
  open it silently, so the owner lists what they allow.
- **An effort is listed only when the adapter can run it.** An effort off the
  scale, one `supported` lacks, or one listed twice is refused; the order the owner
  wrote them in does not matter.
- **`Resolve` by what the requester gave:**

  | Given | Result | Refused when |
  | ----- | ------ | ------------ |
  | neither | not chosen: the session follows the preset | never |
  | anything, and no allowlist | | always |
  | a model M | `{M, preset effort}`; no preset effort leaves the effort empty | M is not listed, or the preset's effort is set and not among M's |
  | an effort E, the preset's model is listed | `{preset model, E}` | E is not among that model's |
  | an effort E, the preset's model is empty or not listed | `{preset model, E}` | E is not the preset's effort; always when the preset has none |
  | both | `{M, E}` | M is not listed, or E is not among M's |

- **Nothing is downgraded or substituted.** What is not allowed is an error, never
  another value.
- **The allowlist checks only what Sidera knows:** what the requester gave and what
  the preset states. A model or effort that neither names stays empty, for the
  runtime to decide as it does when the preset does not state it. That is why an
  effort alone cannot be checked against a model that is empty or not listed: the
  runtime's default model is unknown, so only the preset's own effort is accepted.
- **Only a creation is checked.** The caller keeps the resolved `Choice` with the
  session; narrowing the allowlist later does not touch sessions that exist.
