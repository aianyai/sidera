# CLI Agent driver

`New(Config)` returns a `fakeagent.Agent` backed by the built `sidera` program.
`Config` supplies `Binary`, `Home`, `Dir`, `Token` and `ProbeToken`. `Dir` is the
project directory; distinct sessions can use the same directory because their
node-issued tokens identify them. Inherited Sidera home and credential variables
are removed before the supplied values are installed.

`Config.Env`, when supplied, returns `KEY=value` overrides once per call. The
driver removes matching inherited names before adding them. A fixture can give
each process its own deterministic seed and crash target without changing the
test process's environment. An abnormal exit without a wire error retains the
`*exec.ExitError` in its error chain, so the fixture can check the process exit
alongside its crash marker. Every started call is reaped by `Cmd.Run`.

Each call starts one process with `--json`, decodes the existing Agent response
type with `strictjson`, and maps it to the layer-neutral type. A nonzero process
exit must contain a wire error whose category agrees with that exit code.
There are no retries. The caller's context bounds the process lifetime.

`Recv` preserves stdout exactly, including its trailing newline. A nonzero wait
on a part request is passed through so the Agent boundary rejects that
incompatible input; the driver never silently drops it. It accepts an
empty or `cli` front end, and rejects any other front end. `Probe` has the same
front-end restriction. `Send` rejects `DiscloseRecipients=true`: the CLI has no
flag for it. These restrictions return clear errors and never erase requested
fields. `Control.Body` follows the fake Agent contract: an empty string omits the
optional initial message, and a non-empty value supplies one. No driver changes the production
front end's public command tree.
