# card

The body of a card (`Decode`), its checks against the alias table
(`Envelope.Validate`), the assignee rule (`MatchesAssignee`) and the creator check
(`CheckCreator`).

- Every failure is an `*InvalidError` whose `Reason` is written on the card; it names
  the field and never repeats the card's text.
- `Validate` returns a `Request`: the alias and its address are resolved there, so no
  later step reads the alias table or trusts the card again.
- A follow-up message may leave `to` out only when the card has parents; the route is
  then `lane.RouteFollowUp`.
- `model` and `effort` belong to a `new_session` card alone and are checked with the adapter
  protocol's own syntax (`checkChoice`); whether the target preset allows them is the target
  node's to say, so nothing here knows a model list. A reason names the member, never its
  value.
