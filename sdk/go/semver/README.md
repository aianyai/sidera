# semver

Semantic Versioning 2.0.0 for the SDK: the syntax of a version and the order of
two versions. The adapter manifest and the protocol's patterns accept a version
only through this package, and any other reader of a version, in the daemon or
outside the repository, applies the same syntax and the same order because it
calls the same code. The package is public so that such a reader needs no
second copy, which would give the rules two homes.

## Entry points

| Name | What it is for |
| ---- | -------------- |
| `Parse(text)` | Parse a version; `ErrInvalid` for anything the syntax below refuses |
| `Compare(a, b)` | `-1`, `0` or `+1` as `a` has lower, equal or higher precedence than `b` |
| `Pattern`, `MaxLen` | The accepted syntax as a regular expression shared by RE2 and ECMAScript, so the JSON Schema states the same text, and the longest text accepted |

## Rules

- **Syntax.** `major.minor.patch`, each with no leading zero and at most nine
  digits so that no comparison overflows, then an optional `-` pre-release and
  an optional `+` build part. No `v` prefix, no whitespace, at most `MaxLen`
  characters.
- **Precedence** is section 11 of the specification. Major, minor and patch
  compare as numbers. A version with a pre-release part precedes the same
  version without one. Pre-release identifiers compare from left to right:
  numeric ones as numbers (however long, since they have no length limit) and
  before alphanumeric ones, alphanumeric ones in ASCII order, and a shorter list
  precedes a longer one that begins with it.
- **Build metadata** is dropped by `Parse`: it takes no part in precedence, so
  two versions that differ only in it compare equal.
- **`Compare` takes parsed versions**, so there is no unparsable input to order:
  a caller handles `ErrInvalid` from `Parse`.
