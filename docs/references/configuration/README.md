# Configuration

What the owner of a node decides, and where it is kept: the node settings, the
access policy, the presets, the adapter instances with the settings of their
endpoints, where the node listens and the addresses of other nodes, the
endpoint ids that must outlive the run-time state, the revocation
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
  network.json               where the node listens and the addresses of other nodes; version, last_operation
  endpoint-ids/<instance>.json   the ids of an instance's runtime and direct endpoints
  revocation-backup.json     the owner's confirmation that the self-revocation is kept offline
  revocations/<digest>.rec   revocation records the node keeps (record wire bytes)
  adapters/<id>/current                 {"format": 1, "version": "1.2.3", "digest"?, "revision"?}
  adapters/<id>/versions/<version>/     one installed package, never changed
  adapters/<id>/versions/<version>~<n>/ a package of a version whose directory was taken, revision n
  adapters/<id>/versions/.stage-<hex>/  a package being installed
```

| File | Written by | Version |
| ---- | ---------- | ------- |
| `node.json`, `presets.json`, `instances.json` | `init`; then only management requests (`node set`, `preset add|remove`, `adapter enable|config|disable|remove`, `endpoint set`) | one integer each, from 1, for the whole file |
| `network.json` | `init`; then only management requests (the listen list and the address book) | one integer, from 1, for the whole file |
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

Every setting is required except `name`, which is absent when the node has no name of its own. A request changes settings with a patch, a JSON
object with the settings it names (`{"node_queue_limit": 20000,
"endpoint_defaults": {"backlog_limit": 99}}`); what it does not name stays. The
patch is merged into the current settings and the result is validated whole.

| Setting | Meaning | Default (from) | Valid when |
| ------- | ------- | -------------- | ---------- |
| `name` | what the owner calls the node, shown beside its identifier; empty means the node is called by its host name | none: the setting is absent | `adapterproto.ValidateDisplayName` accepts it (1 to 128 characters, none of the control, line-separator and bidirectional-control characters); a patch with `""` takes the name away |
| `reply_permission` | whether a reply with evidence may reach an endpoint that did not ask for it | on | a boolean; it is written explicitly because the zero value of the rule it feeds is "off" |
| `node_queue_limit` | unfinished deliveries the node holds before new sends are refused | 10,000 (`config`) | at least 1, and the reserve rule below |
| `disk_reserve_bytes` | free space below which new work is refused | 256 MiB (`config`) | at least 1, and the reserve rule below |
| `node_prompt_turns` | turns started by prompts that may run at once on the node, not counting attached sessions | `handoff.DefaultMaxConcurrentTurns` | `handoff.PromptParams.Validate` accepts it (positive) |
| `archive_retention_days` | days the archive keeps its summaries | 30 (`config`) | from 1 to 106,751, the days a `time.Duration` holds |
| `log_total_bytes` | space all logs together may take | 150 MiB (`config`) | at least 1, and the reserve rule below |
| `endpoint_defaults.max_queue_s` | longest an endpoint keeps a message queued, in seconds | `delivery.DefaultEndpointMaxQueue` | `delivery.ValidateEndpointMaxQueue` accepts it |
| `endpoint_defaults.backlog_limit` | messages an endpoint holds queued | `delivery.DefaultEndpointBacklogLimit` | `delivery.ValidateEndpointBacklogLimit` accepts it |

**The reserve rule.** `disk_reserve_bytes ≥ log_total_bytes + 4 KiB ×
node_queue_limit` (`config.OpenDeliveryDiskGrowth`). An unfinished delivery adds
its archive summary and the feedback notification it may cause,
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

**An optional setting needs no new `format`.** `name` is read with the strictness of any optional member (see Rules every file follows): a `node.json` written before it existed has no `name` and reads as a node without one, a node without one writes none, and a program that does not know the setting still reads the file until the owner sets a name. An explicit empty `name` in the file is refused as a second spelling of absent; in a patch `""` is the way to take the name away, because a patch value is a value. The name is a label for people and agents: nothing is addressed, trusted or allowed by it, and another node learns it only through the node's profile.

A setting that another package limits is validated by that package, and the
error the owner gets is that package's error: the rule has one home.

The owner's commands show settings in the form the file holds them:
`NodeSettings.WireJSON()` is the compact object under `"settings"` (the two
settings of the endpoint defaults among them), and `EndpointOverride.WireJSON()`
only those the owner gave an endpoint, with the names a patch takes. The names are written once, for the file and for what a command
shows, so a command cannot show a setting under a name `node set` does not take.

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
A preset cannot be changed, only removed and added again. The values reach an adapter
only in `adapter.start`, so adding or removing a preset restarts the instances that run
and declare a runtime (the engine reference says which); a restart is the one way a
running adapter learns of a change.

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
would be silently ignored or would replace the owner's. The owner replaces the
configuration of an instance that exists with `adapter config`
(`Config.SetInstanceConfig`): the document replaces `config` whole, is read as
`enable` reads one (an object whose values are not `null`; `{}` clears it), works on an
enabled instance and a disabled one, and changes nothing else of the instance. The
values reach an adapter only in `adapter.start`, so the caller restarts a running
instance; this layer only keeps them.

`adapter remove` (`Config.RemoveInstance`) deletes a disabled instance and the endpoint
settings it holds. It refuses an instance that is enabled (`instance_enabled`) and one
that session endpoints made through it are open on (`instance_in_use`, saying how many;
the caller that holds the endpoints counts them for `config`). The stable endpoint ids
in `endpoint-ids/<instance>.json` stay, as they do when an instance is disabled, so that an
instance made later under the same name keeps the same endpoints; loading does not require
an instance for an id file. The state directory of the adapter is outside `config/`, and the
removal moves it aside in the same transaction (see `adapter-remove` below) and deletes it
afterwards.

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

### `network.json`

```json
{
  "format": 1,
  "version": 2,
  "listen": ["/ip4/0.0.0.0/tcp/7744", "/ip4/0.0.0.0/udp/7744/quic-v1"],
  "peers": {
    "node1…": ["/ip4/192.168.1.20/tcp/7744/p2p/12D3KooW…", "/dns4/node2.example.org/udp/7744/quic-v1"]
  }
}
```

`listen` is where the node accepts connections from other nodes; `peers` is the
address book: for each node the addresses the owner entered for reaching it. Both
are required. An empty `listen` is valid and means the node does not listen (it
can still dial); `peers` may be empty.

- **One definition of an address.** `ValidateListen` and `ValidatePeerAddress`
  are the only place that says what an address may be, for the file and for every
  change: `/<host>/tcp/<port>` or `/<host>/udp/<port>/quic-v1`, with an `ip4`,
  `ip6`, `dns`, `dns4` or `dns6` host and a port from 1 to 65535. A peer address
  may end in `/p2p/<peer id>`, which must be a libp2p peer identifier; a listen
  address names no peer. `/p2p-circuit` and every other protocol is refused: the
  owner states direct addresses only, and a relay circuit would name a third
  node. Port 0 is refused: it asks the system for any port, which no one else can
  know.
- **Bounds.** At most 16 addresses in a list and at most 256 bytes in an address,
  the limits of an address list in a pairing record, so an address the book holds
  can be offered in one. The count is judged on what was written, before repeats
  are dropped.
- **Node ids** are the text form of a node id, and a node listed has at least one
  address: removing the last one is removing the node.
- **Normalised text.** An address is written as the multiaddr library prints it
  (`/ip6/0:0:0:0:0:0:0:1/tcp/9` becomes `/ip6/::1/tcp/9`), repeats are dropped,
  and the addresses of a list keep the order in which each first appears, since
  the order is the owner's to choose and nothing depends on it. `peers` is sorted
  by node id like every map of config.
- The address book is the owner's decision, so it is in `config/` and not in
  `state/`: deleting `state/` must not lose it. Addresses learned later are
  state, not configuration.

`init` writes the listen list for a port: `/ip4/0.0.0.0/tcp/<port>` and
`/ip4/0.0.0.0/udp/<port>/quic-v1`, and no peers. The port is fixed: a port of 0
is refused. The default, `DefaultListenPort`, is 7744.

Port 7744 is registered with IANA as `raqmon-pdu` ("RAQMON PDU", RFC 4712, TCP and UDP). It is kept as a provisional default: a clash needs a RAQMON protocol on the same machine, and `init --port` chooses another. Two nodes on one machine need two ports for the same reason.

Both lists are changed only by a management request: the address book takes effect at once (the engine gives the network the whole book after the write is durable), the listen list at the next start, because the listeners open once, at the end of the start.

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
adapters/example/current                 {"format": 1, "version": "1.2.3", "digest": "<64 hex>", "revision": 1}
adapters/example/versions/1.2.3/         sidera-adapter.json, bin/…, any files
adapters/example/versions/1.2.3~1/       a package of the version when versions/1.2.3 was taken
adapters/example/versions/.stage-<hex>/  a copy being installed
```

At start every adapter directory that has a `current` pointer is read: the
pointer must name a version, and a revision, whose directory has a
`sidera-adapter.json` that exists and parses, and whose id and version are those
of the directory. A package never changes after it is installed.

`digest` is the SHA-256, in lower case hex, of the package's contents as staging
copied them: every directory by its relative path and every file by its path, length
and bytes, in sorted order. A file's mode is not part of it (which files are executable
follows from the manifest, which is hashed). It is how an installation tells the same package from a
rebuilt one of the same version. `revision` numbers the directories of one version: the
directory of revision 0 is `versions/<version>` and of revision n `versions/<version>~n` (a
`~` is not part of a version, so the names never meet). An installation takes the first
revision whose directory does not exist, counting on from the current one when the version
is the one installed, because a directory of the version that exists may be the package
that was replaced, which a process may still run from, or one a failed installation left. Both are absent in a pointer written before they existed: the
digest is then unknown, so the first installation of the version replaces that package,
and the revision is 0. A pointer that carries `"digest": ""` or `"revision": 0` is
invalid, like any optional field written empty.

**Installing** (`adapter install` of a package directory):

1. Staging, outside the single writer loop and holding none of its locks: the
   package is read through a reader
   rooted at its directory (its manifest must parse, its entry program and its
   setup program, when the manifest declares one, must be in it, and no name in it
   may begin with `.tmp-`, `.trash-` or `.stage-`, which
   the cleanup would remove silently), then copied to
   `versions/.stage-<hex>` beside the versions, because a rename needs one
   parent. The copy's manifest is parsed again and must name the same adapter
   and version as the source did, so the package cannot change between the check
   and the install. A directory or file of the package that cannot be read is a
   `SourceError` that names its path inside the package: the owner can mend the
   package, and it is no failure of the node's own storage. Everything staging
   writes is preparation. A copy can take as long as the package is big and touches
   only a directory that no change reads, so the versioned changes of the loop
   never wait for it.
2. In the loop, `PlanInstall` and the transaction it plans in the same command,
   so that nothing changes between the plan and its writes. The transaction:
   rename the staging directory to a directory that does not exist yet
   (`versions/<version>`, or `versions/<version>~n` for the first n that is free);
   replace `current` (the commit point); the caller's own result write; discard the
   previous package. The same version with the same digest as the one that is
   current is "already installed" and writes nothing. Any other package of the adapter
   replaces the installed one, and the instances that run are not refused: they keep
   running the files they were started from, the pointer names the new directory, and
   the caller restarts them. That holds for the same version with another digest (a
   rebuilt package that kept its version), and for another version. Nothing is refused
   for an instance, enabled or not.
   While an instance of the adapter is enabled, or a disabled one's process is still
   ending (which the caller that runs the adapters says through the `StillEnding` it
   passes to `PlanInstall`), the directory of the package that was replaced is not
   discarded, since a running executable does not allow that on every platform; the
   repair at the next start removes it. No other directory of the adapter is discarded
   by an installation: a version that was replaced earlier and is installed again goes
   to a directory of its own (`A`, then `B`, then `A` is `versions/A~1`), because the
   earlier directory may still be run from. The result says whether the package is new,
   replaced or already installed.
3. An installation that did not install is unstaged by the install worker: when
   the result is "already installed", when it is refused, and when it failed
   with an error that is not fatal. Only the worker's own staging directory is dropped,
   and, for an adapter never installed whose directory holds nothing else, that
   directory, so that no `adapters/<id>/` without a `current` is left by a refused
   first installation. A package directory that was renamed in and did not become
   current is not dropped here: the unstaging runs outside the loop and cannot tell
   it from one an installation committed in the meantime, and a directory removed
   under a pointer that names it would stop the next start. It is garbage like any
   other directory the pointer does not name: the next installation takes another
   name, and the repair removes it at the next start. After a fatal
   error the next start removes what is left.

**The repair.** An adapter directory without `current`, a staging directory, and
a version directory the pointer does not name are garbage. `AdapterGCWork(r)`
lists exactly this, and loading removes it in one recovery transaction after
every file has been found valid. An adapter directory without `current` is an
installation that did not finish: it is never a reason to refuse startup.

**A bad package faults its adapter and not the node.** The pointer of an adapter
that is unreadable or invalid, and a manifest that is missing, unreadable, invalid
or of another adapter or version, do not refuse startup: the adapter is loaded as
installed with `Adapter.Fault`, the `*FileError` that names the file and says what
is wrong, and no manifest. The instances of the other adapters start; the instances
of the faulted one are reported `package_invalid` (see the adapter host) and the
daemon logs a warning for the adapter at start. Installing the package again
replaces it, even with the digest the pointer holds (the pointer's package is what
is wrong), restarts its enabled instances and clears the fault. The repair leaves an
adapter whose pointer cannot be read alone, since which version it keeps is not
known. What still refuses startup is damage no single package is to blame for: a
missing or unreadable `adapters/` directory, an entry of it that is no adapter
directory, and every invalid file outside the adapter packages.

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
| `peer_unknown` | removing a node that has no address in the book |
| `instance_enabled`, `instance_in_use`, `instance_state_busy` | removing an instance that is enabled; removing one that has session endpoints open; removing one whose state directory cannot be moved aside because something still holds a file in it |
| `instance_unknown`, `instance_exists`, `instance_adapter_mismatch` | the instance named is not there (also for `adapter config` and `adapter remove`); an existing instance given a configuration by `enable`; an existing instance of another adapter |
| `adapter_not_installed` | enabling an adapter with no package |
| `adapter_in_use` | none: no change returns it now (a package is installed whatever the instances of its adapter do), and the code stays in the vocabulary, which never loses a code |
| `endpoint_unknown` | a setting for an endpoint that is neither stable nor open |

The management layer owns the shape of a result. Because the version it
contains is known only once the change is made, the change takes a function that
builds the result from the state the change produces, and writes what it
returns into `last_operation`.

### Start

`Load` checks, in this fixed order: the directories of `config/` (a missing one
is named), `node.json`, `policy.json`, `presets.json`, `instances.json`, `network.json`, the
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

`init` writes the directories of `config/` that are missing and the five files
`node.json`, `policy.json`, `presets.json`, `instances.json` and `network.json`,
all at version 1, with no `last_operation`: the settings are the defaults above, the
policy is `authz.DefaultPolicy()`, there is one preset, `default`, no instance,
and `network.json` listens on the port the owner gave (`sidera init --port`,
default 7744) with no peers. For each of the five files: if it is missing it is written; if it
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
| `network-set` | normal | `network-config` replace | `network.json` rename |
| `presets-change` | normal | `presets` replace | `presets.json` rename |
| `endpoint-set`, `adapter-enable`, `adapter-config`, `adapter-disable` | normal | `instances` replace | `instances.json` rename |
| `adapter-remove` | normal | `state` rename-dir (P, O), `instances` replace | `instances.json` rename |
| `adapter-state-drop` | normal | `trash` remove-all (P), `trash-parent` sync-dir (P) | none: all preparation |
| `adapter-stage` | normal | `parent` mkdir (P, O, M), `stage` mkdir (P), `subdir` mkdir (P, O, M), `file` replace (P, M) | none: all preparation |
| `adapter-unstage` | normal | `stage` discard (O), `parent` discard (O) | the first discard |
| `adapter-gc` | recovery | `dir` discard (M) | the first discard |

Transactions that span owners are declared by the package that composes them,
with the roles `config` exports, and the write functions of `config` write only
under those roles: `direct-register` and `runtime-admit` (`ids`, then the
endpoint record), `backup-confirm` (`backup`, then the result), `identity-retire`
(`revocation`, then the result), `adapter-install` (`version`,
`current`, the result, `old`) and `init` (`dir`, `node`, `policy`, `presets`,
`instances`, `network-config`, then the rest of `init`). The role of `network.json`
is `network-config` because `network` is the network key in the same transaction.

**Crash states.** Every transaction of `config` is either a single replacement,
which a restart finds old or new and nothing between, or preparation that a
restart removes:

- the single-file changes leave `X` or `X'`;
- `adapter-remove` moves the state directory of the instance aside before it replaces
  `instances.json`: a crash between the two leaves the instance configured with an empty
  state (the adapter makes it anew at its next start) and a garbage name the next start
  removes; after the replacement the directory is under a garbage name and the instance is
  gone. `adapter-state-drop`, which deletes that name afterwards, is all preparation and may
  be cut anywhere. An instance removed with its state directory under its own name is never
  left;
- staging leaves preparation: staging directories and, for a first installation,
  an adapter directory with no `current`, all of which the repair removes at the
  next load; unstaging may be cut anywhere, and what is left is removed the same
  way;
- an installation before the replacement of `current` leaves preparation (the
  staging directory, or a package directory the pointer does not name), the
  previous package intact, the same version installed again included; after it, the new version is installed and a
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

**A listen change waits for a restart.** The listeners are opened once, after the node serves, and an address that cannot be opened fails the start and names it. Changing them while running would mean closing and opening sockets under live connections, and a port in use found then would leave a node that no longer listens anywhere it said it would; failing at a start, where the owner is looking, is cheaper and the answer of the change says `restart_required`.

**`network.json` is a file of its own.** `node.json` holds scalar settings that
are all required and changed by merging a patch; the address book is a map the
owner changes entry by entry, so it has its own version and its own transaction,
and a change of an address never conflicts with a change of a setting.

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
neither, and a file without one is refused. The statement protects only while the
sandbox does not read the data directory, where the tokens of other sessions and
the node key are kept (see [local-ipc](../local-ipc/README.md), "Sandboxes and
the rest of the data directory").

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
there is no usable version. The same holds when the same version is installed again with
other contents, or another version whose directory a package that was replaced still
holds: the new package takes a directory that does not exist yet (the revision) so that
the pointer, which then differs, is still the commit point, and no directory is ever
replaced in place or discarded because its name was wanted.

**The digest is kept in the pointer, not recomputed.** Telling a rebuilt package from
the installed one by reading the installed directory again would read up to
`MaxPackageBytes` in the loop. The staging, which reads every file anyway, computes the
digest, and the pointer keeps it.

**The copy is checked, not the source.** A package directory can change between
checking it and installing it; the copy cannot. The manifest that is judged is the one
read first, and the copy must hold exactly those bytes: comparing only the id and the
version would let a manifest that kept both and changed its entry program, its platforms
or its declarations be installed after another had been judged.

**A package has a size limit.** `MaxPackageBytes` (256 MiB) is the most a package may hold
in all, and a package over it is refused as invalid with the file that went over and the
limit in the error. The package is read one file at a time into memory from a directory the
owner names, so a mistaken directory (a home, a checkout with a model file in it) would
otherwise be read until memory ran out. No adapter is near the limit. The manifest
is read with `fsx.Reader.ReadFileLimit` before parsing, and the copy reads each file
with the remaining package budget. Overflow is an invalid package, naming the file
and the limit, without returning a truncated file or first reading the oversized
content whole. The preflight refuses an ordinary file whose reported size
puts the package over the limit; it does not count a symbolic link's own length,
which says nothing about the content copied. The bounded reads count target bytes,
are authoritative on package size and remain bounded if a file grows between the
size check and the read.

Symbolic links to regular files are copied by content, like `cp -L`: the owner
names a package on the same machine, so following such a link introduces no new
authority. A link to a directory is an unreadable source, as are FIFOs and devices,
whether reached directly or through a link. The file layer checks the opened
handle before reading and opens FIFOs without waiting for a writer, so installing
one cannot strand the worker. These are `SourceError`s naming the package path;
the source manifest has an invalid-manifest classification. Link tests run on
every platform and skip when the account cannot create links. FIFO tests run only
on Unix because Windows has no FIFO in a package directory. A refused or unreadable
copy leaves no staging work after cleanup.

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
  `config`: a pointer that names a version that is not there leaves that adapter
  faulted, naming the manifest, rather than starting it on half an installation.
  The repair then keeps nothing of that adapter that the pointer does not name, and
  the pointer names a directory that does not exist: it removes the adapter's other
  version directories and its staging directories, so the previous version is gone
  too and the adapter must be installed again.
- A direct-endpoint key is never forgotten: `endpoint-ids/` only grows.

## Where

| What | Where |
| ---- | ----- |
| Types, paths, errors, the roles and declarations | `internal/node/config/types.go`, `paths.go`, `errors.go`, `decls.go` |
| Strict decoders and canonical encoders | `node.go`, `policy.go`, `presets.go`, `instances.go`, `network.go`, `files.go`, `wire.go` |
| Loading, accessors | `config.go` |
| Versioned changes, write functions for other packages | `change.go`; the changes of `network.json` (`SetListen`, `SetPeerAddresses`, `RemovePeer`) and the address rules are in `network.go` |
| Adapter packages, the repair and its predicate | `adapters.go` (`AdapterGCWork`, `StageAdapter`, `Unstage`, `PlanInstall`) |
| `init` | `init.go` |
| A bad package faults its adapter, one row per way a package is bad; installing over it | `TestABadPackageFaultsItsAdapterAndNotTheNode`, `TestInstallingOverAFaultedAdapterRecoversIt`, `TestAdapterGCWorkSkipsAnAdapterWhosePointerIsBad` |
| An invalid file refuses startup, one row per file and failure | `TestInvalidFileRefusesStartupAndNamesTheFile`, `TestMissingDirectoryRefusesStartupAndNamesIt`, `TestFirstInvalidFileInFixedOrderIsNamed`, `TestWrongTypeIsNamedByItsPath` |
| A preset without the statement is invalid | `TestPresetWithoutLocalSourcesIsInvalid`, `TestAddPresetWithoutLocalSourcesIsRefused`, `TestEveryPresetOnDiskMadeTheStatement` |
| Expected versions, recording the operation | `TestExpectedVersionConflictPerCollection`, `TestVersionedChangeRecordsTheOperation`, `TestStaleVersionIsReportedBeforeAnInvalidValue` |
| No file is written that its decoder refuses | `TestChangeWithoutOperationIDWritesNothing`, `TestEncodeRefusesAZeroOperationID`, `TestEncodeRefusesVersionZero`, `TestChangeRefusesToWrapTheVersion` |
| Settings and their owners | `TestSettingsValidationDelegatesToOwningContract`, `TestEndpointSettingsValidationDelegatesToOwningContract`, `TestDefaultsComeFromTheirOwners`, `TestNodeSettingRanges`, `TestDiskReserveCoversTheQueueAndTheLogs`, `TestNodeSettingsPatch` |
| Removing an instance takes its state directory and only that; a directory that cannot be moved refuses the removal | `TestRemoveInstanceTakesItsStateDirectoryAndOnlyThat`, `TestRemoveInstanceRefusedWhenTheStateDirectoryCannotBeMoved` |
| Crash states and replay | `TestEveryConfigTransactionSurvivesACrashAtEveryPoint`, `TestCrashDuringAdapterGCReplay`, `TestInitSurvivesACrashAtEveryPoint`, `TestInitCrashNeverReplacesAnOwnersFile`, `TestEveryDeclaredCrashPointIsReached` |
| Staging does not hold up the loop; modes of staged files; manifests are copies | `TestStagingDoesNotHoldUpTheWriterLoop`, `TestStagedFilesKeepTheirModes`, `TestAdaptersReturnsCopiesOfTheManifests` |
| Names that begin with a dot | `TestNamesBeginningWithADotAreIgnored` (and the stray-file rows of `TestInvalidFileRefusesStartupAndNamesTheFile`) |
| The retention and its bound | `TestArchiveRetention`, `TestNodeSettingRanges` |
| A clean run leaves no recovery work | `TestCleanScenariosLeaveNoConfigRecoveryWork` |
| `init` | `TestInitWritesTheDefaults`, `TestInitNeverOverwritesExistingConfig`, `TestInitRefusesAnInvalidExistingFileNamingIt` |
| The address rules, the file's bounds, its changes | `TestAddressRules`, `TestNetworkFileRules`, `TestGoldenNetwork`, `TestEncodeNetworkRefusesWhatTheDecoderRefuses`, `TestNetworkChanges`, `TestInitWritesTheListenPortItIsGiven` |
| Golden files | `TestGoldenNode`, `TestGoldenPolicy`, `TestGoldenPresets`, `TestGoldenInstances`, `TestGoldenEndpointIDs`, `TestGoldenBackup`, `TestGoldenAdapterCurrent` |
| No write path outside `txn` | `TestConfigHoldsNoWritableFileSystem` |
| Decoders on any input | `FuzzDecode…` in `fuzz_test.go`, one for each decoder |
