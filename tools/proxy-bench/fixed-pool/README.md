# proxy-bench fixed-pool preparation

The fixed-pool mode adds explicitly prepared objects and reusable Range access
plans to proxy-bench. This directory currently delivers dataset and plan
preparation. The Kubernetes source deployment bundle, Seed readiness, k6
execution, and workload Jobs follow separately.

## Build

Use the repository's Go 1.25 toolchain; no new Go dependencies are required.

```bash
make build-proxy-bench-data-prepare build-proxy-bench-plan-prepare
./bin/proxy-bench-data-prepare --help
./bin/proxy-bench-plan-prepare --help
```

Linux amd64 and arm64 builds are supported. Cross-compilation establishes
buildability; native arm64 execution and performance require separate validation.

## Command parameters

Sizes and budgets use decimal integer byte counts, without unit suffixes.
Required arguments have no usable defaults; internal zero/empty values indicate
missing input. Missing or invalid input produces a structured error and exit 2.
`--help` or `-h` prints usage and exits 0.

Dataset command:

| Argument | Required/default | Purpose and rationale |
|---|---|---|
| `--size` | Required | Object bytes; explicitly choose the experiment's object size. |
| `--count` | Required | Object count; explicitly choose dataset scale. |
| `--root` | Required | Empty business directory; explicitly choose storage placement. |
| `--base-url` | Required | Final source HTTP address; deployment determines reachability and URL identity. |
| `--generation-workers` | Default `1` | Start sequentially; concurrency changes cost, not content. |

Plan command:

| Argument | Required/default | Purpose and rationale |
|---|---|---|
| `--manifest` | Required | Select a specific dataset; never infer the newest manifest. |
| `--range` | Required | Range bytes; explicitly choose the request geometry. |
| `--workers` | Default `1` | Start with one load generator; a larger count creates explicit shards. |
| `--plan-dir` | Required | Explicitly choose plan storage and reuse location. |
| `--memory-budget` | Omitted by default | Estimated plan-memory ceiling; no guessed machine limit. |
| `--file-budget` | Omitted by default | Total plan-file ceiling; excludes source/Seed data capacity. |

Both budget arguments must be positive when supplied. Internal zero represents
an omitted budget; explicitly passing zero is invalid. Omission still reports
estimates and checks output free space before generating a new plan. Memory
budgeting is an estimate check, not an OS/container memory limit or a runtime
RSS monitor. Budgets apply to the Go preparation process and all plan files,
not individual k6 workers. Preparation can run elsewhere; these checks do not
prove that the load generators have sufficient resources. File budgeting checks
the estimate before generation and the total
actual file lengths before publication. See the formulas and limits below.

## Create a dataset

Use decimal positive byte sizes and counts, an empty source directory, and a real
HTTP source address when serving the objects. This example generates URLs without
connecting to them. Credentials, queries, fragments, localhost, and loopback IPs
are rejected.

```bash
./bin/proxy-bench-data-prepare \
  --size 64 --count 2 --root /data/dataset \
  --base-url http://source.example.test > dataset-result.json
```

The command generates all objects and publishes an immutable manifest. It accepts
no manifest, Range size, shard count, plan directory, or plan budget. To change
object size or count, create another dataset in a new directory. There is no
append, shrink, subset, inventory management, or cross-pool object reuse.
`--generation-workers` defaults to 1 and only changes generation concurrency.

Stdout is one JSON object with `schema_version`, `manifest:{path,sha256}`,
`generated_objects`, `generated_bytes`, `elapsed_ms`, and `errors`. A failure before
publication leaves `manifest:null`; generated counts include complete objects.
Progress and diagnostic text go to stderr. `--help` prints usage instead.

Objects reside at `ROOT/objects/sha256/CONTENT_SHA256`; snapshots reside at
`ROOT/manifests/MANIFEST_ID/dataset-manifest.json`. The manifest ID hashes the raw
UTF-8 file bytes. Published files are read-only. [Publish the HTTP origin](#publish-the-http-origin)
before later Seed preparation.

## Publish the HTTP origin

Origin publication is required before Seed cache preparation. The dataset
command writes files and URLs; it does not start nginx, upload data, or operate
Kubernetes. Independent plan preparation only needs a manifest copy and can run
before the origin is online.

The existing [file-server image](../../file-server/Dockerfile) runs nginx and
embeds zero-filled `/4m`, `/1g`, and other fixtures. Its original
[StatefulSet and Service](../../file-server/file-server.yaml) serve those fixtures
without a data-volume mount. Reuse that static HTTP serving capability with
externally generated objects. Create separate fixed-pool resources with distinct
names, labels, and Service selectors to preserve the original FileServer.

The planned Kubernetes source bundle will supply a shared configuration,
data-generation Job, FileServer/Service templates, an optional PVC example, and
step-by-step rendering and recovery instructions. Those templates are not yet
included in this revision. The following layout is the source integration
contract for the existing standalone data command.

Use a dedicated business directory inside the source PVC:

| Location | Path |
|---|---|
| Writable PVC mount in the generation task | `/data` |
| Dataset command root | `/data/dataset` |
| Generated objects | `/data/dataset/objects/sha256/DIGEST` |
| Generated manifest | `/data/dataset/manifests/MANIFEST_ID/dataset-manifest.json` |
| nginx object directory | `/usr/share/nginx/html/objects` |
| Read-only dataset mount for manifest retrieval | `/fixed-pool-data` |

Only `/data/dataset` must be empty or absent; the PVC root may contain unrelated
entries such as `lost+found`. Generate on the final source volume to avoid copying
the object pool between machines. If using another location, transfer the files
without changing bytes or paths before serving them.

Before target cache preparation:

1. Choose storage, permissions, and a stable address reachable from the Seed.
   For an in-cluster Seed, an example is
   `http://SOURCE_NAME.NAMESPACE.svc.cluster.local`; adjust the DNS domain or use
   an explicit routable address for an external Seed. Determine the final address
   once and use it as `--base-url` before generation; generation makes no HTTP
   requests to it.
2. Run data generation separately from the HTTP server. The planned Job directly
   invokes the existing command with `completions=1`, `parallelism=1`,
   `restartPolicy=Never`, and `backoffLimit=0`. Preserve the actual generation
   Pod's stdout JSON and stderr. Do not publish the source after failed
   generation, and do not use a generation initContainer on the server.
3. After successful generation and release of its volume mount, start a separate
   FileServer and Service on the same PVC. Follow the PVC access mode and storage
   topology; RWX is not required, and RWO does not mean only one Pod. Confirm the
   pinned image's nginx root and listening port before setting Service targetPort.
4. Retrieve the manifest identified by the generation result as described below.
   From the Seed's network, check a representative manifest URL: full GET length
   and SHA-256, HEAD metadata, and Range status 206, Content-Range, length, and
   bytes. Pod readiness alone is insufficient. Sampling does not verify the
   whole pool; separate Seed preparation performs full-pool validation.

The service container mounts only the objects under the HTTP root and also
mounts the dataset outside that root for manifest retrieval:

```yaml
# Separate fixed-pool file-server container
volumeMounts:
  - name: fixed-pool-source
    mountPath: /usr/share/nginx/html/objects
    subPath: dataset/objects
    readOnly: true
  - name: fixed-pool-source
    mountPath: /fixed-pool-data
    subPath: dataset
    readOnly: true

# Its Pod spec
volumes:
  - name: fixed-pool-source
    persistentVolumeClaim:
      claimName: fixed-pool-source
```

This adds `BASE_URL/objects/sha256/DIGEST` while preserving the image's original
fixtures. Manifests are not published as HTTP files. Use the returned
`manifest.path`, not a scan for the newest manifest: a generation path
`/data/dataset/manifests/ID/dataset-manifest.json` maps to
`/fixed-pool-data/manifests/ID/dataset-manifest.json` in the service container.
For example, with `SERVICE_POD` and `MANIFEST_ID` taken from the actual deployment
and successful generation result:

```bash
kubectl exec -n "$NAMESPACE" "$SERVICE_POD" -- \
  cat "/fixed-pool-data/manifests/$MANIFEST_ID/dataset-manifest.json" \
  > dataset-manifest.json
sha256sum dataset-manifest.json
```

Require a successful export and compare the digest to the generation result's
`manifest.sha256` before passing this copy to plan or Seed preparation. Exporting
the manifest does not require copying the source objects. A workstation path is
not automatically available on a container host or cluster node. With Docker,
bind-mount the same object and dataset directories read-only at the nginx and
manifest locations above, and publish a port reachable from the Seed.

Keep the source URL and bytes stable. Server, mount, or network failures require
repairing the service, not rerunning successful data generation. Restarting the
server never regenerates objects. Changes to RATE or R/W reuse the source;
B/N changes require a new dataset. A changed source address must not silently
rewrite the published manifest: restore the original endpoint, or create a new
dataset for the new address. Generation failures retain artifacts under the
existing failure rules; do not automatically adopt them. Deleting a Job or
restarting a service must not delete the PVC. Archive the rendered resources,
image digests, generation logs, exported manifest, and HTTP check evidence.

## Prepare or reuse an access plan

Pass the manifest path from the dataset result to the independent plan command:

```bash
./bin/proxy-bench-plan-prepare \
  --manifest /data/dataset/manifests/RETURNED_ID/dataset-manifest.json \
  --range 16 --workers 3 --plan-dir /data/fixed-plans > plan-result.json
```

Only the manifest is needed: source object files, a source root, target
configuration, and Seed access are unnecessary. A copied manifest works without
the original body directory. B/N and object URLs come from the validated input.
Object size must be divisible by Range size; invalid geometry is rejected
without selecting a different Range. The default worker count is 1.
R/W changes generate a separate plan. RATE and
duration are absent from preparation; rate allocation is shared Go code for the
later worker entry point.

Stdout contains `schema_version`, `manifest_sha256`, `plan:{path,sha256}`,
`region_count`, `worker_count`, `reused`, `estimates:{memory_bytes,file_bytes}`,
`actual_file_bytes`, `elapsed_ms`, and `errors`. On failure `plan` is null;
unavailable inputs or statistics are null and errors explain the failure.
Correcting plan inputs and rerunning does not regenerate data or prepare a Seed.

Plans reside in `PLAN_DIR/INPUT_KEY/` with `request-plan.json`, `object-urls.json`,
and `shards/WORKER_ID.json`. The input key hashes the manifest's actual byte
digest, R, W, fixed algorithm, and seed. `plan.sha256` hashes the actual plan file
and is distinct from that lookup key. Repeating identical inputs reuses the plan
only after validating every reference, checksum, count, and fixed shard order.
A changed manifest at the same path is a new input; moving identical bytes does
not change identity. Corrupt or missing existing artifacts are rejected without
overwriting or automatically repairing them.

## Contracts, budgets, and failures

[schemas](schemas) describes version 1 contracts and command results. Shared Go
validation also checks fields, duplicate keys, nulls, types, fixed seeds,
geometry, identity relationships, and shard membership. Fixed fixtures live in
[testdata/vectors.json](testdata/vectors.json). Generated manifests and plans use
compact fixed-order JSON with one LF and no embedded self-hash. Imported valid
manifest JSON is identified by its original bytes, without re-encoding.

The plan command accepts optional positive `--memory-budget` and `--file-budget`
byte counts. Given manifest byte length D, total regions M, and workers W:

- Estimated memory: `4*M + ceil(M/8) + 2*D + 512*W + 64*1024*1024` bytes.
- Estimated files: `11*M + 512*W + 2*D + 1024*1024` bytes.

Configured budgets are checked before allocating the permutation. Actual plan,
URL index, and shard lengths are checked before publication. Without budgets,
estimates and free-space checks still run. These estimates do not guarantee peak
Go/k6 memory. Production pool sizes and loading costs require calibration.
M is limited to `2^32-1`; JS-facing integers are limited to `2^53-1`.

Errors contain `code`, `stage`, and `message`. Exit status is 0 for completed
preparation, 2 for invalid input, contract mismatch, budget excess or data
conflict, 3 for preparation/I/O/publication failure, and 130 for interruption.
An interruption takes precedence while other available errors are retained.

One writer locks each output directory. On a dataset failure inspect
`ROOT/.fixed-pool-incomplete`, its `failed.json`, and any published but
unregistered objects. A retry refuses to adopt residual artifacts. Confirm
ownership before removing the failed dataset or choose another empty directory.
Incomplete plans use `.incomplete-INPUT_KEY`; inspect them before cleanup or use a
new plan directory. Partial object bytes may exceed completed-object counters.
There is no automatic recovery or block-level resume.

## Artifact compatibility

Content and order seeds (`nydus-range-content-v1`, `nydus-range-order-v1`) and
algorithm versions remain fixed computation identifiers. Renaming this mode does
not change object bytes, digests, URLs, or existing manifest identity.

The URL-index name is now `object-urls.json`, so new plan bytes and IDs differ
from the original prototype. Keep published artifacts immutable and use a new
plan output directory when an old incompatible plan occupies the input key.
There are no old command aliases or automatic migrations. Plan checks do not
read or hash source bodies; keep source storage controlled. Later Seed
preparation performs full target-body verification.
