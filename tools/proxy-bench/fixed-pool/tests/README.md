# Fixed-pool functional regressions

These tests protect the benchmark tool's behavior through its commands, HTTP
requests, exit status and result files. They run the real preparation tools,
worker helper, shell entry and k6 against a small controlled HTTP proxy.
They do not measure Dragonfly capacity or establish real Seed cache readiness.

## When to run

| Change or event | Required regression |
| --- | --- |
| Dataset, plan, preparation, input checks, request script, reporting or shared execution | `integration.py` |
| Shared runtime or contracts affecting single and multi-worker execution | Both suites |
| Shards, rates, worker identity, independent processes or peer failures | Both suites |
| Harness, k6, toolchain, dependency or image changes affecting execution | Both suites |
| Before merging fixed-pool changes or submitting phase acceptance | Both suites against the final candidate |
| Prose-only documentation changes | Check documentation; unchanged steps and behavior need no regression rerun |
| Changed build or run instructions | Execute changed steps and the affected suite; both for shared changes |

During development, the affected suite gives feedback; the full run is the
functional regression gate. These scripts are not currently run by GitHub Actions.
They complement Go unit tests and applicable real Seed, Kubernetes and scale tests.
Benchmark users do not need to run them before every measurement.

## Prerequisites and execution

Use Linux with Python 3 (standard library only), POSIX shell, and **k6 2.3.0**.
Allow local loopback HTTP sockets and child processes. No Docker, Kubernetes,
real Dragonfly service, credentials or external source server is needed to run
the suites. Installing dependencies and building binaries may require downloads.

From the repository root, build native binaries with the repository's Go toolchain:

```bash
make build-proxy-bench-data-prepare build-proxy-bench-plan-prepare \
  build-proxy-bench-seed-cache-prepare build-proxy-bench-worker-support
export PATH="$PWD/bin:$PATH"
```

Put k6 on `PATH`, then run both suites into a **new** directory:

```bash
make test-proxy-bench-fixed-pool WORK_DIR=/tmp/fixed-pool-regression-001
```

For focused development, run either script directly:

```bash
python3 tools/proxy-bench/fixed-pool/tests/integration.py --work-dir /tmp/fixed-pool-single-001
python3 tools/proxy-bench/fixed-pool/tests/multi_worker.py --work-dir /tmp/fixed-pool-multi-001
```

Do not use `python -O`: assertions are the test oracles. Both suites must exit 0
for the combined gate to pass. A timeout or assertion failure is a failed run;
inspect its artifacts and retain them before rerunning in a new directory.
The combined run normally takes a few minutes; timing depends on host load.

## Scope and expectations

`integration.py` checks normal execution, request numbering and Range mapping,
transport resend, HTTP status/redirect/body/timeout handling, dynamic VUs and
dropped iterations. It also checks interruption, numbering gaps, threshold and
script failures, rejected inputs, incomplete results, and native metric mapping.

`multi_worker.py` runs two or three local independent worker processes. It checks
shard loops, fixed allocations (17 requests/s to 9/8 or 7/6/4), per-worker input
rates and k6-inspected executor options, overlapping request windows, invalid
peers, interruption with a continuing healthy peer, missing summaries, dropped
iterations and intentionally non-overlapping starts. It does not test physical
host distribution or orchestrate production workers.

The fixtures deliberately use two 64 KiB objects and 16 KiB ranges. The proxy
listens on a dynamic loopback port. `192.0.2.1` is a simulated response identity,
not a deployment address; the client version names the supported protocol profile,
not a running client. These fixed values are regression inputs, not user examples.
Real benchmark usage belongs in the tool and deployment documentation.

Expected allocations are fixed literals. Request mapping is checked against the
prepared plan; metrics are checked against native k6 output. Measured latency,
QPS and request totals can vary between runs and are not performance baselines.
Test copies add request labels or inject faults; the shipped runtime is unchanged.
Instrumentation fails explicitly if its source anchors no longer match.

## Results and cleanup

The combined command creates `single/` and `multi/` under `WORK_DIR`. Each retains
generated inputs, per-case logs, received requests and results. Preparation calls
retain arguments, exit codes and stdout/stderr. Multi-worker cases also retain
per-worker k6 inspection output and request-window evidence.

`completed-cases.json` records progress before a failure; a successful suite writes
`result.json`. The single-worker suite separately records rejected inputs and
incomplete-result cases. Raw k6 results, execution records and worker summaries
remain available for diagnosis. Fault-case nonzero worker exits are expected only
when asserted by the suite; the suite itself still exits 0 on success.

Commands have bounded waits. On normal completion, assertion failure or timeout,
the harness closes its proxy and terminates its own child process groups. Results
are retained for inspection; remove only the work directory when no longer needed.
