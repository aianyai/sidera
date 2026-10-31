# Temporary native batch classification

This directory belongs only to the disposable diagnostic branch. `batch.sh`
runs one fixed `go test -json` command, with a 90-second Go timeout, and preserves
its exit status. Workflow steps explicitly select the preplanned batches.
`main.go` consumes Go's documented test events, prints their original output to
the CI console, and emits only static classifications and integer counts.
It does not export a new artifact, probe the fixture or modify an oracle.

Each invocation of the selected test is a sample. Only its own original failure
signature counts as the same symptom; any other assertion is another failure.
A passing target test also needs its existing successful result evidence where
the test prints it. Missing samples, skipped tests, invalid output, setup failure
and timeout cannot qualify a batch. A qualifying 20-sample batch needs at least
10 same-symptom failures and no incomplete samples. Its independent 10-sample
confirmation needs at least 5. These are practical selection thresholds, not
statistical confidence bounds or proof of cause or completed minimization.

The maintenance timeout is incomplete. A last `phase=main` start plus the test's
single `waitProbes` / `Start` / `runScript` stack is only a candidate for the same
waiting boundary. A timeout without that evidence leaves target association
unproven. Reached remains unknown without a returned result. The original Go
timeout stack stays in the console; no watchdog or stack collector is added.
