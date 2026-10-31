# Continuous integration

> **Temporary diagnostic branch only; never merge.** This branch provides two
> independent native observations, not a product fix or a formal acceptance gate.
> Only manual `workflow_dispatch` on `codex/f7b-native-observation` runs them;
> there are no inputs, automatic triggers, retries, sampling loops or full walks.
> Each job has a 10-minute limit (20 runner-minutes total); every Go test command
> has a 90-second timeout and uses `-race=false`. Exact-list guards require the
> selected entry points before any observation.
>
> Windows first runs explicit privacy and scope canaries, lists
> `TestDiagnosticWCLCalibration` and
> `TestUnobservedPreparationLeavesGenerationOneForDaemon`, and runs calibration
> once. Only successful calibration permits one execution of that unchanged
> original cleanup test with `acceptance,diagnostic,crashtest,testclock` and
> `SIDERA_CI_DIAGNOSTIC_WCL_OBSERVER=1`. Calibration is unverified until it runs
> on native Windows. Its owned helper opens a private file without delete sharing,
> confirms the sharing violation and its PID plus creation time in Restart
> Manager, then closes the file while staying alive and confirms that identity
> disappears from the resource list before exiting normally.
>
> The Windows probe captures a confirmed fixture adapter identity before cleanup;
> that identity capture can perturb scheduling. Original Close, RemoveAll and
> assertion order remain, including the original failed verdict and exit status.
> After the first failed RemoveAll, only its exact adapter JSONL file under that
> fixture root may be registered with Restart Manager. `resource_users` records
> PID plus creation FILETIME associations, not proof of a blocking handle;
> `handle_owner` remains unknown. Missing files, empty lists, query failures and
> incomplete lists do not prove absence of a leak. Only a matching owned identity
> may be observed through its already-held process handle for up to 10 seconds;
> external resource users are never opened or waited on. That 10-second bound
> covers process waiting, not Restart Manager calls: those APIs have no separate
> deadline and rely on the outer Go 90-second timeout. Only StartSession,
> RegisterResources for one file, GetList and EndSession are used; no RmShutdown,
> RmRestart, download, EULA acceptance, external-process operation or RemoveAll
> retry is added.
>
> macOS runs explicit privacy and scope canaries, lists the original
> `TestBinaryManagementFeedbackRepro`, then runs it once with
> `acceptance,diagnostic` and `SIDERA_CI_DIAGNOSTIC_MF_OBSERVER=1`. Only the existing
> management-instance generation-1 `source-update/out.write#4` recipe is observed,
> after its original Check fails and before its original Stop. There is no time
> advance or extra quiet boundary. The read-only scan is not frozen or atomic;
> fixture logical Now, pending feedback At and notice state times are projected
> when available, while the merge window remains unavailable. The private model's
> view of a notice remains unknown. The original oracle and error are preserved;
> a passing run establishes wiring only, not a reproduced failure or a fix.
>
> Typed allowlisted stdout records use only `[DEBUG-wcl]` and `[DEBUG-mf]`; no
> product API, artifact schema or raw log persistence is added. Original stdout
> and errors stay in the existing CI console. The only uploaded files remain
> `manifest.json`, `attempts.jsonl`, `boundary.jsonl`, `ledger-projection.jsonl`
> and `trace-projection.jsonl`. Raw ledger, credentials, freeform bodies, process
> output, native app/service names and full fixture directories are excluded.
> Explicit diagnostic canary selectors exclude calibration and helper execution.
> The previous disposable batch classifier is removed; unused existing exact
> target entry points remain unchanged. These observations establish neither a
> shared cause nor completed minimization and do not run the other failure families.

## Workflow

The disposable workflow is [`.github/workflows/ci.yml`](../../../.github/workflows/ci.yml).
It replaces the formal workflow only on this never-merge branch; its result
cannot establish the candidate's formal acceptance guarantee.

| Job | Runner | Executions after guards and canaries |
| --- | --- | --- |
| `windows-cleanup` | `windows-latest` | One native Restart Manager calibration, then only on success one original cleanup test |
| `macos-management` | `macos-latest` | One original management exact target |

The jobs are independent: neither has a `needs` dependency. Each dispatch uses
its run ID as its concurrency group and cannot cancel another run. The workflow
token has only `contents: read`; checkout does not persist credentials. Checkout,
Go setup and artifact upload retain their full pinned action SHAs. Go setup reads
the root `go.mod` toolchain and caches against every `go.sum`.

Each observed test command is recorded verbatim in the existing manifest through
`SIDERA_CI_DIAGNOSTIC_COMMAND`. Direct Go invocations preserve their nonzero status
as the step and job result. Export verification and upload run even after failure;
only the five explicit projection files are uploaded, with three-day retention.
No new classifier, shell helper, raw-output file or fallback command exists.

## Local verification

Local checks cover the six explicitly named diagnostic privacy/scope/unit
canaries, Windows cross-compilation with and without diagnostic instrumentation,
non-diagnostic acceptance compilation, source selection, unchanged original
reproduction bodies, formatting and workflow syntax. A single combined macOS
command executes the original cleanup and management targets once each, with
both toggles enabled; the Windows observer is a stub there. Those macOS results
verify wiring only. They cannot verify Restart Manager calibration or native
Windows behavior, and no local Linux or Docker execution is part of this probe.

Check the workflow with `actionlint .github/workflows/ci.yml`. Formal repository
checks are not run by this temporary workflow and must remain a separate gate.
