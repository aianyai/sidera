# Acceptance disk operations

Read the parent [README](../README.md) first. This package shares acceptance
operations across execution layers and imports no fixture.

`Damage` changes only an existing product-created source, target, creation control,
retained source body, or empty layout directory. Callers must stop the node first,
use the raw disk outside crash observations, and retain the returned endpoint or
body identity for the recipe's owner-alert judgement. It never seeds recovery
records or counts its offline writes as coverage.

`InspectDamage` accepts the current real `store.Scan` and the `DamageResult` of
damage already applied. It returns the existing recipe facts with `Applied` true,
the damaged identities, all owner alerts, all `control_unknown` alerts and only
`corrupt_body` alerts whose subject matches the damaged body. Recipes own the
judgement; bindings own whether damage happened and how to obtain the scan.

`CheckClosed` takes a read-only file system, the current real record scan, time
and enabled-instance predicate. It audits the engine's recovery plan, transaction
leftovers, package garbage and unresolved delivery/control obligations.
`CheckLayout` uses the daemon's existing layout predicate after actual startup.
Bindings supply facts and lifecycle ownership; these helpers share disk semantics.

`CheckClosedSession` accepts the current real record scan and endpoint identifier.
It requires that endpoint to remain present before retention, closed with an empty
token and without a session checkpoint.

`InspectRetention` accepts a read-only file system, the current real record scan
and endpoint identifier. It returns `recipes.RetentionFacts` for every delivery
record, body file, archive file and parsed summary, plus that endpoint's presence.
Archive read failures and damaged lines return the facts accumulated so far and
the original error or damaged-line error.

`InspectInstalledPackage` accepts a read-only file system and package identifier.
It decodes the actual current pointer and returns all version names in sorted
order as `recipes.InstalledPackage`. An absent pointer requires an absent package
directory; unfinished version directories fail. Read and decode errors propagate.
Both bindings acquire their own actual reader and scan inside each callback;
these helpers retain no process, filesystem or scan across calls.
