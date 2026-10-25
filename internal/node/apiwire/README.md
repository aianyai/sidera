# apiwire

The words of the wire: how what the engine and the packages below it say becomes
the category and code of an error on the Agent and management interfaces, the body
that carries them, the status of the HTTP answer and the exit code of the command
line.

This is a runtime package of layer 6 of [`internal/node/README.md`](../README.md):
it imports `engine`, `config`, `ipc`, `admin` and `strictjson` to read their errors,
and does no I/O. `engine` does not import it: the words of the wire never enter the
engine.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `FromError(err)` | The one mapping: an error of the wire as it is, an `*engine.Refusal` by its reason, a `*config.ChangeError` by its reason, a `*strictjson.Error`, a failure of the way to the daemon (`ipc`), and any other error as an internal one. |
| `FromAdmin(reason)` | The refusal of a request by the administrative rules. |
| `Error`, `Body`, `New`, `Malformed` | An error as the wire carries it, `{"error":{"category","code","message","details?"}}`. |
| `Category`, `Categories`, `HTTPStatus`, `ExitCode`, `ExitUsage`, `ExitOutcomeUnknown` | The eight categories, the status of the answer for each, and the exit code of the command line for each, with the two exit codes no category gives. |

## Rules

- **One mapping.** A reason of the engine, a refused change and a failure of the way
  to the daemon each have one pair of words, written here and nowhere else. A test
  walks every reason the engine and `config` have, so one added without a row fails.
- **The text of an unknown error stays off the wire.** Anything that is none of the
  known errors is `internal/internal` with a fixed message; what it said is for the
  log. A client that is not the author of the code should not be handed a path or a
  name that an error happened to carry.
- **Details only where they are needed to act:** the current version of a conflict,
  the path of an invalid value, the limit that was hit, the field of an invalid
  argument. A message holds no message body, no token and no key.
- **Every category has its own exit code**, 0 is never one of them, and a category
  that is none of the known ones is an internal error, never a success.
- **A daemon that is not running is not a failed check.** `daemon_not_running` and
  `peer_check_failed` are both `unavailable` and never match each other; the second
  may mean that someone holds the name.

## What the caller must guarantee

- Map once, at the edge: an interface turns the error of a call into an `Error`
  with `FromError`, writes the status of its category and the body, and does not
  look at the reason itself.
- Do not put a body, a token or a key into the message of an `Error` it makes with
  `New` or `Malformed`.

## Where the tests are

`apiwire_test.go`: every reason of the engine and of `config` against a table
written out by hand, the details a refusal carries, the faults
in a request and the failures of the way to the daemon, the text of an unknown error
kept off the wire, the golden body of an error, and the status and exit code of each
category.
