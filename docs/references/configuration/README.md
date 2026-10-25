# Configuration

What the owner of a node decides, and where it is kept: the node settings, the
access policy, the presets, the adapter instances with the settings of their
endpoints, the endpoint ids that must outlive the run-time state, the revocation
records the node keeps, and the installed adapter packages. All of it is JSON
(or the bytes of a signed record) under `config/` of the data directory, owned
by one package, [`internal/node/config`](../../../internal/node/config/README.md),
which reads it strictly at start and writes it only through transactions.

Platform paragraphs that say "not exercised by CI" describe behaviour that no CI
job can observe, for the reason given where it appears (for example, no job loses
power).

## What

### The files

```
config/
  node.json                  node settings; version, last_operation
  policy.json                the access policy; version, last_operation
  presets.json               every preset; one version for the whole set
  instances.json             every adapter instance and its endpoint settings; one version
  endpoint-ids/<instance>.json   the ids of an instance's runtime and direct endpoints
  revocation-backup.json     the owner's confirmation that the self-revocation is kept offline
  revocations/<digest>.rec   revocation records the node keeps (record wire bytes)
  adapters/<id>/current                 {"format": 1, "version": "1.2.3"}
  adapters/<id>/versions/<version>/     one installed package, never changed
  adapters/<id>/versions/.stage-<hex>/  a package being installed
```

| File | Written by | Version |
| ---- | ---------- | ------- |
| `node.json`, `presets.json`, `instances.json` | `init`; then only management requests (`node set`, `preset add|remove`, `adapter enable|disable`, `endpoint set`) | one integer each, from 1, for the whole file |
| `policy.json` | `init`; the owner by hand while the node is stopped (no command changes it yet) | one integer, from 1 |
| `endpoint-ids/<instance>.json` | the daemon, when an instance's runtime endpoint or a direct endpoint key is first seen | none |
| `revocation-backup.json` | `identity confirm-backup` | none |
| `revocations/<digest>.rec` | `identity retire` | none |
| `adapters/` | `adapter install` | the package's own semantic version |

The owner edits a file by hand only while the node is stopped; a running node
changes `config/` only through the management interface and does not watch the
files. `endpoint-ids/` is not for editing at all: the node writes it, and an
edit would change the address of an endpoint.

### Rules every file follows

- **Strict decoding.** Keys match exactly, a required field must be present,
  `null` is never a value, an unknown key and a repeated key are errors, and in
  an optional field an explicit empty value or zero is an error (absence is the
  only way to say "not set"). Trailing data after the document is an error.
- **`format`** is `1` in every file. It is not the version a request names; it
  is what a future change of the layout reads to know what to migrate.
- **`version`** (versioned files) starts at 1 and counts changes of the whole
  collection: adding, changing or removing any member raises it by one, and the
  new version is written in the same replacement as the change. After a removal
  the version is still readable. The largest version has no successor: a
  change on it is refused (`invalid`, at `version`), because the next version
  would wrap to 0, which the decoder refuses.
- **`last_operation`** (versioned files) is the management operation that made
  the current version: `{"id": "<32 hex>", "person": "local" | "<person id>",
  "fingerprint": "<64 hex>", "result": {…}}`. It is written in the same
  replacement as the change, so the effect and its result cannot be told apart
  by a crash. A file edited by hand has none. It is overwritten by the next
  change; an older operation id is then no longer answered from the file, and a
  request that repeats it meets the changed version and is a conflict, so it is
  never executed twice. The operation id is never zero: a change whose request
  carries a zero id is refused (`invalid`, at `id`) before anything is written.
- **No file is written that its own decoder refuses.** The encoders of the
  versioned files refuse version 0 and a last operation with a zero id, with
  the decoder's own check, so a value that no start could read never reaches
  the disk even if a caller passes one.
- **Canonical text.** The node writes two-space indentation, fixed field order,
  sorted map keys and a final newline. What the node writes decodes to the value
  it came from and encodes to the same bytes; what an owner wrote in another
  layout decodes to a value that encodes to this canonical form.

### `node.json`

```json
{
  "format": 1,
  "version": 3,
  "settings": {
    "reply_permission": true,
    "node_queue_limit": 10000,
    "disk_reserve_bytes": 268435456,
    "node_prompt_turns": 8,
    "archive_retention_days": 30,
    "log_total_bytes": 157286400,
    "endpoint_defaults": {"max_queue_s": 1800, "backlog_limit": 200}
  }
}
```

Every setting is required. A request changes settings with a patch, a JSON
object with the settings it names (`{"node_queue_limit": 20000,
"endpoint_defaults": {"backlog_limit": 99}}`); what it does not name stays. The
patch is merged into the current settings and the result is validated whole.

| Setting | Meaning | Default (from) | Valid when |
| ------- | ------- | -------------- | ---------- |
| `reply_permission` | whether a reply with evidence may reach an endpoint that did not ask for it | on | a boolean; it is written explicitly because the zero value of the rule it feeds is "off" |
| `node_queue_limit` | unfinished deliveries the node holds before new sends are refused | 10,000 (`config`) | at least 1, and the reserve rule below |
| `disk_reserve_bytes` | free space below which new work is refused | 256 MiB (`config`) | at least 1, and the reserve rule below |
| `node_prompt_turns` | turns started by prompts that may run at once on the node | `handoff.DefaultMaxConcurrentTurns` | `handoff.PromptParams.Validate` accepts it (positive) |
| `archive_retention_days` | days the archive keeps its summaries | 30 (`config`) | from 1 to 106,751, the days a `time.Duration` holds |
| `log_total_bytes` | space all logs together may take | 150 MiB (`config`) | at least 1, and the reserve rule below |
| `endpoint_defaults.max_queue_s` | longest an endpoint keeps a message queued, in seconds | `delivery.DefaultEndpointMaxQueue` | `delivery.ValidateEndpointMaxQueue` accepts it |
| `endpoint_defaults.backlog_limit` | messages an endpoint holds queued | `delivery.DefaultEndpointBacklogLimit` | `delivery.ValidateEndpointBacklogLimit` accepts it |

**The reserve rule.** `disk_reserve_bytes ≥ log_total_bytes + 4 KiB ×
node_queue_limit` (`config.OpenDeliveryDiskGrowth`). An unfinished delivery adds
its archive summary and events, and the feedback notification it may cause,
before it is finished; with the logs that is what can still be written after the
reserve is reached, and the reserve must hold it. With the defaults the sum is
about 190 MiB, under the 256 MiB reserve; with the default logs and reserve the
queue limit can go up to 27,136. The arithmetic does not overflow: a limit too
large for it is refused as too large. The archive itself is not counted: it
grows with the number of deliveries kept for the retention period, and its size
is documented with the archive.

The retention reaches its consumers only as `NodeSettings.ArchiveRetention()`,
the one conversion of days to a duration. The upper bound exists because above
it that conversion wraps around to a negative or short duration and the archive
would discard its summaries at once, silently; it is almost three centuries, so
no owner's retention is refused for it.

The reserve rule is an estimate, not a proof. The 4 KiB is rounded from about
4.15 KiB per unfinished delivery, and the blocks a file system allocates for
small files are not counted. No margin term is added, because the quantities
that would need one are already counted at their maximum: logs at their cap,
the archive by the bytes appended to it, and one feedback notice merges many
deliveries instead of adding one each. With the defaults the reserve leaves about 66 MiB over the sum.

A setting that another package limits is validated by that package, and the
error the owner gets is that package's error: the rule has one home.

### `policy.json`

The node's access rules, as `authz` reads them. Nothing applies them to local
sources (an endpoint on the same node is judged by endpoint declarations only);
the file is parsed and validated at every start so that a rule that would be
refused is found when it is written. `init` writes the default policy of
`authz`.

```json
{
  "format": 1,
  "version": 2,
  "rules": [
    {"effect": "allow", "subject": {"kind": "member"},
     "object": {"kind": "all_endpoints"}, "action": "see"},
    {"effect": "deny", "subject": {"kind": "node", "node": "node1…"},
     "object": {"kind": "endpoint", "endpoint": "ep1…"}, "action": "message.send",
     "conditions": {"not_after": "2027-01-01T00:00:00Z"}},
    {"effect": "restrict", "object": {"kind": "node"}, "action": "relay.reserve",
     "nodes": ["node1…", "node1…"]}
  ]
}
```

| Part | Values |
| ---- | ------ |
| `effect` | `allow`, `deny`, `restrict` |
| `subject.kind` | `member`, `direct`, `node` (with `node`), `endpoint` (with `address`, `node/endpoint`); absent for `restrict` |
| `object.kind` | `endpoint`, `runtime` (with `endpoint`), `all_endpoints`, `node` |
| `action` | the names of `authz.Action`: `see`, `message.send`, `session.create`, `session.close`, `session.interrupt`, `endpoint.status`, `relay.reserve`, `relay.connect` |
| `conditions` | `not_before`, `not_after` (RFC 3339), `rate` (`count`, `window_s`), `max_body_bytes`, `presets`; each only where `authz` allows it |
| `nodes` | the nodes a `restrict` admits |

`rules` is required and may be empty, which allows nothing. Every rule goes
through `authz.NewPolicy`, so a combination `authz` refuses (a rate cap on a
deny, an action that cannot be aimed at the object) refuses the file, with the
index of the rule. A subject that gives both `node` and `address` is refused,
since an address already names its node and one of the two would be dropped
silently.

### `presets.json`

```json
{
  "format": 1,
  "version": 2,
  "presets": {
    "default": {"local_sources": "allow", "description": "Created by sidera init."},
    "sandboxed": {"local_sources": "deny", "config": {"example": {"model": "small"}}}
  }
}
```

A preset name has the syntax `adapterproto.ValidatePresetName` checks.
`local_sources` is **required, with no default**, and is exactly `"allow"` or
`"deny"`: whether an endpoint on this node may start a session with the preset.
It decodes to `authz.PresetLocalAccess`; a missing field, another word, a
boolean, `null` or the field twice makes the file (or the `preset add` request)
invalid, and no decoder returns the zero statement. The enumeration, not a
boolean, keeps "not written" and "written false" apart for an owner who edits by
hand. `init` writes one preset, `default`, which allows local sources: nothing
adapter specific can be validated before an adapter is installed, and a general
preset lets a local agent create a session at once. An owner declares `deny` for
a preset that an agent on the node must not use, for example one without a
sandbox.

`config` groups the values of the manifest's preset fields by adapter id. Only
the shape is checked here (an adapter id, configuration keys, values that are
not `null`): the adapter may not be installed when the file is read, and the
manifest says what a value may be, which is checked when an instance starts.
A preset cannot be changed, only removed and added again.

### `instances.json`

```json
{
  "format": 1,
  "version": 4,
  "instances": {
    "example": {
      "adapter": "example",
      "enabled": true,
      "config": {"workspace": "/Users/me/work"},
      "endpoints": {"ep1…": {"max_queue_s": 3600, "backlog_limit": 400}}
    }
  }
}
```

An instance name has the syntax of an adapter id (it becomes a file name and a
lock name). `adapter enable` creates an instance when it is new (a request that
names no instance gets the adapter's id, which the management layer passes) and
turns an existing one on, keeping what the owner set; `adapter disable` only sets `enabled` to false and never deletes the
instance, because its stable endpoint ids belong to it.

`enable` refuses an adapter that is not installed, an existing instance of
another adapter, and an existing instance together with a configuration, which
would be silently ignored or would replace the owner's.

`endpoints` holds the settings an owner gave single endpoints: `max_queue_s` and
`backlog_limit`, each optional, at least one present. A field that is absent is
not set, and the node default applies; `Config.EndpointSettings` returns what
applies. The endpoint may be a runtime, direct or session endpoint.

**Settings of closed session endpoints.** A session endpoint's id is random and
never used again, so the settings of one that has closed can never apply, and
they are not removed when it closes: that would make the engine change a file
whose version the owner holds. They are dropped by the owner's next write of
`instances.json`, whatever it is, together with that write. The ones dropped
are those of endpoints that are neither a stable id of the instance (those come
back when the endpoint is registered again, and keep their settings) nor open
now; the caller says which are open. A setting for an endpoint that is neither
is refused when it is made, because the next write would drop it.

### `endpoint-ids/<instance>.json`

```json
{"format": 1, "runtime": "ep1…", "direct": {"<key>": "ep1…"}}
```

The runtime endpoint id of an instance (when its adapter declares a runtime) and
the id of each direct endpoint by the key the adapter registered it under. An id
is minted the first time it is needed, from an injected source of randomness, and
never changes or goes away: the adapter protocol promises that the same key
gives the same endpoint, and another address would invalidate everything that
remembered the old one. The file is in `config/` because it must survive the
deletion of `state/`; it is not a secret and not an owner's setting. A key is 1
to 128 characters; ids are unique across all files, and the system endpoint is
never written. The file grows with the number of keys an adapter uses and is
replaced whole each time; at the limit of 1,000 direct endpoints per instance it
is about 150 KB.

### `revocation-backup.json` and `revocations/`

```json
{"format": 1, "confirmed_at": "2026-10-24T06:00:00Z", "digest": "<64 hex>"}
```

The confirmation holds only for the self-revocation whose digest it names
(`Backup.Confirms`): after a new identity the old confirmation no longer matches
and the owner is reminded again, with no special case and no refusal to start.
Confirming is a state transition, so it does not change the version of
`node.json`.

A revocation record is kept under `revocations/<digest>.rec`, where the digest
is the record's digest, and the file is the record's own wire bytes. At every
start each file is decoded with `record.Decode`, verified with
`revocation.Verify`, and its name must be its digest; a file that fails any of
these refuses startup. `RevocationSet` returns the verified revocations as the
set the judging packages read. The records are in `config/` and not in `state/`
because deleting `state/` must not make a retired identity usable again.

### Adapter packages

```
adapters/example/current                 {"format": 1, "version": "1.2.3"}
adapters/example/versions/1.2.3/         sidera-adapter.json, bin/…, any files
adapters/example/versions/.stage-<hex>/  a copy being installed
```

At start every adapter directory that has a `current` pointer is read: the
pointer must name a version whose `sidera-adapter.json` exists and parses, and
whose id and version are those of the directory. A package never changes after
it is installed.

**Installing** (`adapter install` of a package directory):

1. Staging, outside the single writer loop and holding none of its locks: the
   package is read through a reader
   rooted at its directory (its manifest must parse, its entry program must be in
   it, and no name in it may begin with `.tmp-`, `.trash-` or `.stage-`, which
   the cleanup would remove silently), then copied to
   `versions/.stage-<hex>` beside the versions, because a rename needs one
   parent. The copy's manifest is parsed again and must name the same adapter
   and version as the source did, so the package cannot change between the check
   and the install. Everything staging writes is preparation. A copy can take as
   long as the package is big and touches only a directory that no change reads,
   so the versioned changes of the loop never wait for it.
2. In the loop, `PlanInstall` and the transaction it plans in the same command,
   so that nothing changes between the plan and its writes. The transaction:
   discard a leftover directory of the version if
   there is one; rename the staging directory to `versions/<version>`; replace
   `current` (the commit point); the caller's own result write; discard the
   previous version. An adapter that has an enabled instance is not upgraded
   (`adapter_in_use`): it runs the version that is current, and the owner
   disables it first. The same version that is already current is "already
   installed" and writes nothing.
3. An installation that did not install is unstaged by the install worker: when
   the result is "already installed", when it is refused, and when it failed
   with an error that is not fatal. The staging directory, a version directory
   that was renamed in and did not become current, and, for an adapter never
   installed, its whole directory are dropped, so that no `adapters/<id>/`
   without a `current` is left. After a fatal error the next start removes them.

**The repair.** An adapter directory without `current`, a staging directory, and
a version directory the pointer does not name are garbage. `AdapterGCWork(r)`
lists exactly this, and loading removes it in one recovery transaction after
every file has been found valid. An adapter directory without `current` is an
installation that did not finish: it is never a reason to refuse startup.

### Changing the configuration

A request that changes a setting names the version it was based on. Every change
begins with the same steps, in this order: the version is compared with the
current one by `admin.CheckVersion` (any difference is a conflict, whose error
carries the current version), then the content is decoded and validated, then
the operation and its result are recorded and the file is replaced. A refused
change writes nothing and builds no result.

| Reason | When |
| ------ | ---- |
| `version_conflict` | the version is not the current one (with the current version) |
| `invalid` | a value is malformed or breaks a rule (with its path) |
| `nothing_to_change` | a patch that names no setting |
| `preset_exists`, `preset_unknown` | adding a name that is taken, removing one that is not there |
| `instance_unknown`, `instance_exists`, `instance_adapter_mismatch` | the instance named is not there; an existing instance given a configuration; an existing instance of another adapter |
| `adapter_not_installed`, `adapter_in_use` | enabling an adapter with no package; upgrading an adapter with an enabled instance |
| `endpoint_unknown` | a setting for an endpoint that is neither stable nor open |

The management layer owns the shape of a result. Because the version it
contains is known only once the change is made, the change takes a function that
builds the result from the state the change produces, and writes what it
returns into `last_operation`.

### Start

`Load` checks, in this fixed order: the directories of `config/` (a missing one
is named), `node.json`, `policy.json`, `presets.json`, `instances.json`, the
endpoint-id files by name, the confirmation, the revocation records by name, and
the adapters by id. The first file that is missing where it must exist, cannot
be read, is not a valid document of its kind, breaks a rule or has a name that
does not match what it holds ends the load with an error that names the file
and, when the fault is in a value, the path of the value; the node does not
start and nothing is written. Only if everything is valid does the repair of the
adapter directory run. Three directories are strict about what they hold: in `endpoint-ids/` an entry
must be `<instance>.json`, in `revocations/` a record named by its digest, in
`adapters/` the directory of an adapter named by its id. Any other entry (a
stray file in `adapters/`, an editor's backup `example.json.bak` beside a file
in `endpoint-ids/`) is named and refused, since an entry that is none of them is
a sign that something other than the node wrote there. Revocations and stable
ids fail closed for a reason: a revocation record that was not read would let a
retired identity be used again, and an endpoint-id file that was not read would
make the node mint a new id, and so a new address, for an endpoint that has one.
A name that begins with a dot is ignored in all three: it is either a leftover
that the cleanup at start removes (`.tmp-`, `.trash-`) or a file an operating
system or editor puts into a directory by itself (`.DS_Store`), and none of
those is ever configuration. The root of `config/` and the package directories
under `adapters/` are not checked for extra files: nothing there is read as
configuration, so a stray file cannot be taken for it.

### `init`

`init` writes the directories of `config/` that are missing and the four files
`node.json`, `policy.json`, `presets.json` and `instances.json`, all at
version 1, with no `last_operation`: the settings are the defaults above, the
policy is `authz.DefaultPolicy()`, there is one preset, `default`, and no
instance. For each of the four files: if it is missing it is written; if it
exists and is valid it is left byte for byte, however the owner laid it out; if
it exists and is not valid, `init` fails and names it, and writes nothing. The
reason is that the keys can be deleted and `init` run again while `config/` is
complete: the owner's decisions and the installed adapters must survive that,
and an invalid file is the owner's to repair.

### Transactions

Each is declared as data; the table of the program is built from
`config.Transactions()` and the declarations of the other owners.

| Transaction | Kind | Writes in order (`P` is preparation, `O` optional, `M` many) | Commit point |
| ----------- | ---- | ------------------------------------------------------------ | ------------ |
| `node-set` | normal | `node` replace | `node.json` rename |
| `presets-change` | normal | `presets` replace | `presets.json` rename |
| `endpoint-set`, `adapter-enable`, `adapter-disable` | normal | `instances` replace | `instances.json` rename |
| `adapter-stage` | normal | `parent` mkdir (P, O, M), `stage` mkdir (P), `subdir` mkdir (P, O, M), `file` replace (P, M) | none: all preparation |
| `adapter-unstage` | normal | `stage` discard (O), `version` discard (O), `parent` discard (O) | the first discard |
| `adapter-gc` | recovery | `dir` discard (M) | the first discard |

Transactions that span owners are declared by the package that composes them,
with the roles `config` exports, and the write functions of `config` write only
under those roles: `direct-register` and `runtime-admit` (`ids`, then the
endpoint record), `backup-confirm` (`backup`, then the result), `identity-retire`
(`revocation`, then the result), `adapter-install` (`stale`, `version`,
`current`, the result, `old`) and `init` (`dir`, `node`, `policy`, `presets`,
`instances`, then the rest of `init`).

**Crash states.** Every transaction of `config` is either a single replacement,
which a restart finds old or new and nothing between, or preparation that a
restart removes:

- the single-file changes leave `X` or `X'`;
- staging leaves preparation: staging directories and, for a first installation,
  an adapter directory with no `current`, all of which the repair removes at the
  next load; unstaging may be cut anywhere, and what is left is removed the same
  way;
- an installation before the replacement of `current` leaves preparation (the
  staging directory, or a version directory the pointer does not name), the
  previous version intact; after it, the new version is installed and a
  previous version that was not yet discarded is removed by the repair;
- an id minted for `direct-register` or `runtime-admit` and written before the
  endpoint record is an id with no endpoint, which costs nothing: the adapter
  retries the same key and gets the same id;
- a crash in the repair itself leaves some of the garbage; the next load finds
  the rest and removes it, and never touches what the pointer names;
- `init` leaves a prefix of its files and directories; running it again reuses
  them and completes.

On a run without a crash there is no recovery work at any quiet point:
`AdapterGCWork` is empty, and so is the cleanup of temporary names.

## Why

**JSON, edited by hand while stopped, read strictly.** The files are the owner's
decisions, so they must be readable and editable without a tool; the same format
as the adapter manifest needs no new dependency. Strictness is what makes
editing by hand safe: a misspelled key is an error that names the key, not a
setting that silently stays at its default. No file is watched and nothing is
reloaded, so a running node cannot be made to loosen its own rules by an edit
that did not go through the management interface and its levels.

**One file and one version per collection.** Presets and instances are each one
file with one version. A change of one member and the new version are one
replacement, so nothing can see a member without its version; creating and
removing a member need no cross-file transaction; and a request names one
number. The price is that two changes to different presets conflict: with a
single local owner that is rare, and the remedy is to refresh and repeat.
Per-member files with a per-member version would need both versions at once, and
the request has room for one. The version of an endpoint's settings is the
version of `instances.json`, for the same reason.

**The statement about local sources is an enumeration with no default.** Without
it, an agent in a sandbox could create a session with a preset that has none and
start whatever it likes. A boolean would let "forgot to write it" and "wrote
false" look alike on disk and have a default; the words `allow` and `deny` have
neither, and a file without one is refused.

**The version is checked inside the change.** The check and the write must be one
step; if a caller checked first and wrote after, a change could slip in between.
The management layer does not repeat the check.

**Results live in the file they describe.** Recording the operation in the file
it changed makes a replayed request answerable without a second file, and a crash
cannot separate an effect from its result. The price is that only the most recent
result of a collection is kept; a request repeated after another change is a
conflict, not a second execution. The replay lookup of the management layer
runs against all stored results, the `last_operation` of each versioned file
and the results kept under `state/admin/`, whose key spaces do not overlap,
under `admin.KeyFor(actor, id)`, and in the same loop command as the change, so
that two retries of one request cannot both pass it.

**The endpoint ids are not in `instances.json`.** An adapter registers a direct
endpoint at any time. Were its id in the versioned file, the adapter's action
would invalidate the version the owner holds. They are in their own unversioned
file, written by the daemon, and the owner does not edit it.

**A patch is merged and then validated whole.** One setting depends on others (the
reserve on the queue and the logs), so it is judged against the result. One bad
value then changes nothing, with no half-applied patch to undo.

**Settings are validated by the package that owns the limit.** Duplicating
`delivery`'s bound on a queue time would give it two homes that drift apart; the
tests check that the error is the owner's, and that the defaults are the owner's
constants.

**A pointer file and a version directory, not a rename of the package.**
Replacing one small file is the commit point and everything before it is
preparation, so a crash leaves nothing that needs a rule other than "remove what
the pointer does not name". The alternative, two renames (old out, new in),
leaves a state with the package missing and needs a rule for it, and for a while
there is no usable version.

**The copy is checked, not the source.** A package directory can change between
checking it and installing it; the copy cannot.

**No `current` is not an error.** A first installation that crashed leaves a
directory with no pointer. Refusing to start for it would let a crash of an
`adapter install` keep the node down; removing it is safe because nothing was
installed.

**`init` never overwrites.** The earlier idea, that without the marker of a
finished `init` nothing can exist and so it may rewrite, fails when the keys are
deleted to start a new identity while `config/` is whole: rewriting would erase
the owner's decisions and orphan the installed adapters. A file that is there
and valid is the owner's; one that is there and invalid is theirs to fix.

**Strict about stray files and missing directories.** A directory the owner
deleted is named and refused. So is an unexpected file in the three directories
described under Start, where an unread file changes what the node does:
configuration that is being quietly misread is worse than a node that tells the
owner what to look at. An editor's backup file beside a configuration file there
is a stray file and is named; a name that begins with a dot is not. Elsewhere
(the root of `config/`, the package directories) extra files are not checked.

**A result function, not a result.** The shape of a management result belongs to
the management layer, and it contains the new version, which exists only once
the change is made. A function lets the layer that knows the shape build it from
the state that knows the version.

**The settings of an endpoint that are not set take the node default.** Storing
both numbers would freeze the node's defaults at the time of the first change
for one endpoint, and a later change of the defaults would then apply to some
endpoints and not to others for a reason nobody remembers.

## Known gaps

- Changing a setting for a closed session endpoint is refused only when the
  caller says which endpoints are open; a caller that cannot say gets no check.
- Windows: the order of two renames after a power loss is not documented by the
  platform. The installation depends on the rename of the staging directory
  becoming durable before the replacement of `current`; not exercised by CI, no
  job loses power. The repair makes either order harmless for the files of
  `config`: a pointer that names a version that is not there refuses startup,
  naming the manifest, rather than starting on half an installation.
- A direct-endpoint key is never forgotten: `endpoint-ids/` only grows.

## Where

| What | Where |
| ---- | ----- |
| Types, paths, errors, the roles and declarations | `internal/node/config/types.go`, `paths.go`, `errors.go`, `decls.go` |
| Strict decoders and canonical encoders | `node.go`, `policy.go`, `presets.go`, `instances.go`, `files.go`, `wire.go` |
| Loading, accessors | `config.go` |
| Versioned changes, write functions for other packages | `change.go` |
| Adapter packages, the repair and its predicate | `adapters.go` (`AdapterGCWork`, `StageAdapter`, `Unstage`, `PlanInstall`) |
| `init` | `init.go` |
| An invalid file refuses startup, one row per file and failure | `TestInvalidFileRefusesStartupAndNamesTheFile`, `TestMissingDirectoryRefusesStartupAndNamesIt`, `TestFirstInvalidFileInFixedOrderIsNamed`, `TestWrongTypeIsNamedByItsPath` |
| A preset without the statement is invalid | `TestPresetWithoutLocalSourcesIsInvalid`, `TestAddPresetWithoutLocalSourcesIsRefused`, `TestEveryPresetOnDiskMadeTheStatement` |
| Expected versions, recording the operation | `TestExpectedVersionConflictPerCollection`, `TestVersionedChangeRecordsTheOperation`, `TestStaleVersionIsReportedBeforeAnInvalidValue` |
| No file is written that its decoder refuses | `TestChangeWithoutOperationIDWritesNothing`, `TestEncodeRefusesAZeroOperationID`, `TestEncodeRefusesVersionZero`, `TestChangeRefusesToWrapTheVersion` |
| Settings and their owners | `TestSettingsValidationDelegatesToOwningContract`, `TestEndpointSettingsValidationDelegatesToOwningContract`, `TestDefaultsComeFromTheirOwners`, `TestNodeSettingRanges`, `TestDiskReserveCoversTheQueueAndTheLogs`, `TestNodeSettingsPatch` |
| Crash states and replay | `TestEveryConfigTransactionSurvivesACrashAtEveryPoint`, `TestCrashDuringAdapterGCReplay`, `TestInitSurvivesACrashAtEveryPoint`, `TestInitCrashNeverReplacesAnOwnersFile`, `TestEveryDeclaredCrashPointIsReached` |
| Staging does not hold up the loop; modes of staged files; manifests are copies | `TestStagingDoesNotHoldUpTheWriterLoop`, `TestStagedFilesKeepTheirModes`, `TestAdaptersReturnsCopiesOfTheManifests` |
| Names that begin with a dot | `TestNamesBeginningWithADotAreIgnored` (and the stray-file rows of `TestInvalidFileRefusesStartupAndNamesTheFile`) |
| The retention and its bound | `TestArchiveRetention`, `TestNodeSettingRanges` |
| A clean run leaves no recovery work | `TestCleanScenariosLeaveNoConfigRecoveryWork` |
| `init` | `TestInitWritesTheDefaults`, `TestInitNeverOverwritesExistingConfig`, `TestInitRefusesAnInvalidExistingFileNamingIt` |
| Golden files | `TestGoldenNode`, `TestGoldenPolicy`, `TestGoldenPresets`, `TestGoldenInstances`, `TestGoldenEndpointIDs`, `TestGoldenBackup`, `TestGoldenAdapterCurrent` |
| No write path outside `txn` | `TestConfigHoldsNoWritableFileSystem` |
| Decoders on any input | `FuzzDecode…` in `fuzz_test.go`, one for each decoder |
