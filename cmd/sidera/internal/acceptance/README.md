# Acceptance support

Support for exercising the built program through its public front ends.
[`agent/cli`](agent/cli/README.md) implements the layer-neutral fake Agent by
starting the actual CLI. [`agent/mcp`](agent/mcp/README.md) drives a persistent
MCP subprocess through its stdin/stdout pipes, negotiating either supported era.
The fixture starts and waits for that real process; it never calls the MCP
server in process for binary acceptance. These drivers are test tooling and are not imported by
the executable.

Give a driver the built binary, a data directory, a project working directory,
and the endpoint or probe credential issued by the node. Session endpoints come
from `control ... session.create`, never from a test-only registration path.
Callers own their process context deadlines and any ledger recording wrapper.

## Foreground-process fixture

The root package owns a real foreground `sidera` process and a control listener
that survives its generations. `NewFixture` takes prebuilt tagged and plain node
programs, the two fake programs, an absolute short root directory, a stable seed
and a logical clock origin. The caller owns that directory and removes it after
`Close`. The fixture never builds a program or calls the daemon or engine in
process. `daemon.Table` supplies declarations only.

`Init` runs the short initialization command and waits for it. `Start` starts a
new foreground daemon, waits for public status to report `running`, checks the
complete tagged crash-point table, and waits for enabled-instance probes and
quiet. A tagged ping alone may answer while recovery is still running. Its first start
installs the caller-owned fake package, enables the `fake` instance and adds the
`fixture` preset through management. Bootstrap retains each complete request,
including its operation ID and expected version, before sending it. A later
generation resumes at the first unanswered stage with that same request, so a
committed change replays its stored result; completed stages are not repeated.
Later starts reuse the installed package; only the separate input configuration's
clock origin is updated. The default
adapter provides direct endpoints. Runtime-driven CLI actions are not connected:
the current fake process inputs cannot clear inherited daemon crash variables
from the CLI a runtime starts.

`Manage`, `Adapter` and `Runtime` expose the existing external clients. The
fixture owns one logical `Now` across daemon generations. `SetTime` retains the
requested target before sending it to the daemon, even if the RPC fails: a
daemon may have changed its clock before losing its response, and the next
generation starts at the retained target. An RPC failure remains an error and
does not synchronize the fakes. After a successful daemon RPC, `SetTime` waits
for every connected fake's clock update. `BeginStep` records a unique step and
synchronizes connected fakes. Handles and quiet checks synchronize connections
again before returning. No runtime session is created by this base fixture;
the fake runtime currently records its creation before its fixture control
connection exists, so that initial event cannot acquire a current step here.

`Quiesce` samples every enabled adapter's notification count, held-method
multiset and outstanding requests, requires the remote runtime to be idle,
submits the expectations to `test.quiesce`, and samples again. It succeeds only
when both samples agree and the daemon and fakes are satisfied. Its timeout is
the scenario default, bounded by the caller's context. Diagnostic failures name
the daemon report and fake progress. Wall-clock tickers pace external readiness
queries; they never move logical time.

Every started node command gets its own stdout, stderr and append-only trace
files and exactly one `Wait` goroutine. `Stop` freezes the complete-line scenario
prefix before the public `daemon.stop` operation, then waits for actual exit.
`Kill` kills only the current daemon, waits, and records only the PID it actually
killed. Native runtimes belong to the control listener rather than a daemon
generation. `Close` stops the daemon and closes the listener and fixture ledger.

`ReadTrace` requires completed `Wait`. It validates generations, the full
declaration table's kinds, contiguous per-key ordinals and complete JSON lines;
ordinary observations go through `fsx.ParsePoint` and `crashwalk.NewHit` once.
A matching second crash-marker line is termination evidence, not another hit.
`Reached` additionally requires the actual abnormal process exit. CLI process
points are returned separately and never count toward transaction coverage.
The first `BeginStep` marks the setup prefix without resetting any ordinal.

Tests build the four programs once per outer test process, from that checkout.
The child builds follow the outer race build. They cover actual targeted
termination, unexpected exits, restart after kill, clock and quiet, setup
ordinals, bootstrap replay after a committed preset's response is lost, trace
rejection, and ordinary builds rejecting tagged controls while ignoring their
environment variables. Their short roots are removed after each
run. Domain recipes, offline damage and crash-walk binding are separate callers
of this process fixture.
