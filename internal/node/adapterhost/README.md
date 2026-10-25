# adapterhost

The supervision of adapter processes and the carrying of the adapter protocol
between them and the engine. Read
[../../../docs/references/adapter-protocol/README.md](../../../docs/references/adapter-protocol/README.md)
for the protocol itself.

This is a runtime package of layer 4 of [`internal/node/README.md`](../README.md):
it imports the contract packages, `config`, `logfile` and the SDK, and never the
engine. It defines `Engine`, the few things it asks of the engine, and the engine
defines the interface it asks of the host. Time comes only from the clock it is
given, and every goroutine it starts ends with its context.

## Entry points

| Name | Purpose |
| ---- | ------- |
| `Engine`, `EndpointCalls`, `ObservedCalls` | What the host asks of the engine: the life of a connection, notifications, and the requests an adapter makes. The last two are optional parts the engine takes on as it learns to answer them. |
| `Notification`, `GoneCause` | A notification of the adapter as the engine receives it, and why an adapter is reported as gone. |
| `Launcher`, `Process`, `Instance` | How an adapter instance is started and what the host holds of it. |
| `Params`, `DefaultParams`, `OfflineAfterPingFailures` | The tunables, and the one decided rule: two pings in a row unanswered make an adapter offline. |
| `ErrNotConnected` | What a call to an adapter that is not in service returns without sending anything. |
| `Env`, `Source`, `LogOpener` | What the host needs of the node when it starts: the data directory, a read-only view of the configuration, the opener of the adapters' logs, and the engine. |
| `ConnCounts` | What the host counts of one connection, for the question whether the system is quiet. |
