# Fixed-pool HTTP source

This bundle runs the existing data command on the final PVC, then serves the
immutable objects through an independent FileServer. It does not operate the
cluster automatically. Plan preparation only needs a manifest and is independent
of source or Seed availability.

## Prerequisites and configuration

Use a working Kubernetes cluster, kubectl, POSIX shell, envsubst, Python 3, curl,
sha256sum, head, and cmp. Provide a PVC and storage topology supporting sequential
mounts by the generation Job and server. RWO does not mean one Pod; RWX is not
required. The PVC root may contain lost+found; only `/data/dataset` must be empty
or absent. Ensure volume permissions allow the configured generation UID/GID.

Build the three tools with `make build-proxy-bench-tools`, then build
`tools/proxy-bench/fixed-pool/Dockerfile.tools` using Buildx and select a registry
or local image distribution mechanism. No image is published by these steps.
Use immutable image references in the configuration and save their digests and
the running Pods' imageIDs. Verify the chosen FileServer image serves
`/usr/share/nginx/html` on port 80 and supports HEAD and Range. The existing
[FileServer](../../../../file-server/Dockerfile) provides this nginx capability;
the fixed-pool mounts preserve its existing files outside `objects/`.

From this directory, copy `source.env.example` to `source.env` and edit it before
running the following block. Choose the final address before generating data.
The default address uses the Service, namespace and DNS domain. An external Seed
needs an explicitly provisioned reachable BASE_URL; changing the value does not
provision an ingress, port forward or load balancer.

```sh
set -eu
cp -n source.env.example source.env
# Edit source.env before continuing: images, node, PVC and experiment settings.
```

B/N have small functional example values (4 MiB, four objects); they are not
capacity-test defaults. Generation concurrency starts at 1. DNS defaults to
cluster.local, UID/GID/fsGroup to 1000, and resource values are small engineering
starting points requiring validation on the selected cluster. Images and node
have no usable defaults. Existing PVC users can ignore new-PVC capacity/class.

```sh
set -a
. ./source.env
set +a
: "${DNS_DOMAIN:=cluster.local}"
: "${BASE_URL:=http://${SOURCE_NAME}.${NAMESPACE}.svc.${DNS_DOMAIN}}"
export DNS_DOMAIN BASE_URL
python3 - <<'PY'
import os, re
required = '''SOURCE_NAME NAMESPACE PVC_NAME TOOLS_IMAGE FILE_SERVER_IMAGE
SOURCE_NODE B N GENERATION_WORKERS DATA_UID DATA_GID FS_GROUP DATA_CPU_REQUEST
DATA_CPU_LIMIT DATA_MEMORY_REQUEST DATA_MEMORY_LIMIT SERVER_CPU_REQUEST
SERVER_CPU_LIMIT SERVER_MEMORY_REQUEST SERVER_MEMORY_LIMIT'''.split()
for name in required:
    if not os.environ.get(name):
        raise SystemExit('missing configuration: ' + name)
for name in ['TOOLS_IMAGE', 'FILE_SERVER_IMAGE']:
    if not re.fullmatch(r'[^\s@]+@sha256:[a-f0-9]{64}', os.environ[name]):
        raise SystemExit(name + ' must be repository@sha256:...')
for name in ['B', 'N', 'GENERATION_WORKERS']:
    if not re.fullmatch(r'[0-9]+', os.environ[name]) or int(os.environ[name]) == 0:
        raise SystemExit(name + ' must be a positive decimal integer')
print('Final BASE_URL=' + os.environ['BASE_URL'])
PY
RENDERED="$(mktemp -d "${TMPDIR:-/tmp}/fixed-pool-source.XXXXXX")"
export RENDERED
VARS='${SOURCE_NAME} ${NAMESPACE} ${BASE_URL} ${B} ${N} ${GENERATION_WORKERS}
${PVC_NAME} ${PVC_STORAGE_CLASS} ${PVC_CAPACITY} ${TOOLS_IMAGE} ${FILE_SERVER_IMAGE}
${SOURCE_NODE} ${DATA_UID} ${DATA_GID} ${FS_GROUP} ${DATA_CPU_REQUEST}
${DATA_CPU_LIMIT} ${DATA_MEMORY_REQUEST} ${DATA_MEMORY_LIMIT}
${SERVER_CPU_REQUEST} ${SERVER_CPU_LIMIT} ${SERVER_MEMORY_REQUEST} ${SERVER_MEMORY_LIMIT}'
envsubst "$VARS" < dataset-job.tpl.yaml > "$RENDERED/dataset-job.yaml"
envsubst "$VARS" < file-server.tpl.yaml > "$RENDERED/file-server.yaml"
python3 - "$RENDERED" <<'PY'
import pathlib, re, sys
for path in pathlib.Path(sys.argv[1]).glob('*.yaml'):
    if re.search(r'\$\{[A-Z_]+\}', path.read_text()):
        raise SystemExit('unsubstituted variable in ' + str(path))
PY
cp source.env "$RENDERED/source.env"
kubectl get namespace "$NAMESPACE"
kubectl get node "$SOURCE_NODE"
kubectl apply --dry-run=server -f "$RENDERED/dataset-job.yaml"
kubectl apply --dry-run=server -f "$RENDERED/file-server.yaml"
```

Create the namespace beforehand if needed. For a **new PVC only**, fill
PVC_STORAGE_CLASS/PVC_CAPACITY, render `pvc.example.yaml` with the same whitelist,
inspect it and apply it. WaitForFirstConsumer storage may bind only after Job
scheduling. With an existing PVC, inspect its access mode, capacity, node affinity
and permissions; do not apply the optional example over it.

Only when requesting a new PVC:

```sh
: "${PVC_CAPACITY:?set the new PVC capacity}"
envsubst "$VARS" < pvc.example.yaml > "$RENDERED/pvc.yaml"
kubectl apply --dry-run=server -f "$RENDERED/pvc.yaml"
kubectl apply -f "$RENDERED/pvc.yaml"
```

```sh
kubectl get pvc -n "$NAMESPACE" "$PVC_NAME" -o yaml > "$RENDERED/pvc-before.yaml"
```

## Generate once, preserve the actual Pod result

Apply only the Job first. It invokes the data binary directly; there is no
generation initContainer, automatic retry or generation on server restart.

```sh
kubectl apply -f "$RENDERED/dataset-job.yaml"
if ! kubectl wait -n "$NAMESPACE" --for=condition=complete \
    "job/${SOURCE_NAME}-data" --timeout=30m; then
  kubectl get job,pods -n "$NAMESPACE" -o wide
  kubectl logs -n "$NAMESPACE" "job/${SOURCE_NAME}-data" > "$RENDERED/failed-generation.log" 2>&1 || true
  exit 1
fi
kubectl get job -n "$NAMESPACE" "${SOURCE_NAME}-data" -o json > "$RENDERED/job.json"
kubectl get pods -n "$NAMESPACE" -l "batch.kubernetes.io/job-name=${SOURCE_NAME}-data" \
  -o json > "$RENDERED/generation-pods.json"
GEN_POD="$(python3 - "$RENDERED/generation-pods.json" <<'PY'
import json, sys
items = json.load(open(sys.argv[1]))['items']
assert len(items) == 1, 'inspect multiple actual generation Pods before proceeding'
p = items[0]
assert p['status']['phase'] == 'Succeeded'
assert p['status']['containerStatuses'][0]['state']['terminated']['exitCode'] == 0
print(p['metadata']['name'])
PY
)"
kubectl logs -n "$NAMESPACE" "$GEN_POD" > "$RENDERED/generation.log"
python3 - "$RENDERED" <<'PY'
import json, pathlib, sys
r = pathlib.Path(sys.argv[1])
# kubectl logs combines stdout and stderr. Keep the complete log, and extract
# exactly one original JSON result line without treating diagnostics as JSON.
matches = []
for line in (r/'generation.log').read_bytes().splitlines(keepends=True):
    try:
        value = json.loads(line)
    except (ValueError, UnicodeDecodeError):
        continue
    if isinstance(value, dict) and 'generated_objects' in value and 'manifest' in value:
        matches.append((line, value))
assert len(matches) == 1
line, result = matches[0]
assert result['schema_version'] == 1 and result['errors'] == [] and result['manifest']
(r/'dataset-result.json').write_bytes(line)
PY
# Release the completed Job's volume before starting the server. The PVC stays.
kubectl delete job -n "$NAMESPACE" "${SOURCE_NAME}-data" --wait=true
```

On generation failure stop here. Preserve Job/Pod status, logs and remnants;
choose a new empty dataset/PVC or inspect ownership before manual cleanup.
Deleting a failed Job does not make its nonempty dataset safe to adopt. Controller
re-execution is still possible; generator locking and conflict checks protect
the dataset instead of assuming the Job ran exactly once.

## Start the source and export the manifest

```sh
kubectl apply -f "$RENDERED/file-server.yaml"
kubectl rollout status -n "$NAMESPACE" "statefulset/${SOURCE_NAME}" --timeout=5m
SERVER_POD="${SOURCE_NAME}-0"
kubectl get pod -n "$NAMESPACE" "$SERVER_POD" -o json > "$RENDERED/server-pod.json"
MANIFEST_ID="$(python3 - "$RENDERED/dataset-result.json" <<'PY'
import json, re, sys
r = json.load(open(sys.argv[1]))
digest = r['manifest']['sha256']
assert re.fullmatch('[a-f0-9]{64}', digest)
assert r['manifest']['path'] == '/data/dataset/manifests/' + digest + '/dataset-manifest.json'
print(digest)
PY
)"
kubectl exec -n "$NAMESPACE" "$SERVER_POD" -- \
  cat "/fixed-pool-data/manifests/$MANIFEST_ID/dataset-manifest.json" \
  > "$RENDERED/dataset-manifest.json"
printf '%s  %s\n' "$MANIFEST_ID" "$RENDERED/dataset-manifest.json" | sha256sum -c -
```

The Job mounts the PVC at `/data`, generating only its `dataset/` business
directory. The server mounts `dataset/objects` read-only at
`/usr/share/nginx/html/objects`, preserving existing nginx files, and mounts
`dataset` read-only at `/fixed-pool-data` for manifest export. Manifests are not
published under the HTTP root. Check actual imageID, mounts, port and nginx root;
Pod Ready is insufficient to establish source correctness.

## Check from the Seed's reachable network

Run these checks where the chosen Seed can reach the exact manifest URLs. If the
operator terminal has different DNS/routing, transfer the exported manifest and
this check block to a suitable machine or verification Pod. Do not silently
replace the URL with localhost. Preserve status/header/body evidence.

```sh
OBJECT_URL="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["objects"][0]["url"])' "$RENDERED/dataset-manifest.json")"
curl --noproxy '*' --silent --show-error --fail --head --max-time 60 \
  "$OBJECT_URL" > "$RENDERED/head.headers"
curl --noproxy '*' --silent --show-error --fail --max-time 1800 \
  -H 'Accept-Encoding: identity' -D "$RENDERED/full.headers" \
  "$OBJECT_URL" -o "$RENDERED/object.bin"
PROBE_BYTES="$(python3 -c 'import json,sys; print(min(65536,json.load(open(sys.argv[1]))["object_size_bytes"]))' "$RENDERED/dataset-manifest.json")"
curl --noproxy '*' --silent --show-error --fail --max-time 60 \
  -H 'Accept-Encoding: identity' -H "Range: bytes=0-$((PROBE_BYTES - 1))" \
  -D "$RENDERED/range.headers" "$OBJECT_URL" -o "$RENDERED/range.bin"
python3 - "$RENDERED" <<'PY'
import email.parser, hashlib, json, pathlib, sys
r = pathlib.Path(sys.argv[1])
o = json.loads((r/'dataset-manifest.json').read_bytes())['objects'][0]
size = min(65536, o['size_bytes'])
def headers(name, status):
    lines = (r/name).read_text().splitlines()
    assert int(lines[0].split()[1]) == status
    h = email.parser.Parser().parsestr('\n'.join(lines[1:]))
    assert h.get('Content-Encoding') in [None, 'identity']
    return h
assert int(headers('head.headers', 200)['Content-Length']) == o['size_bytes']
headers('full.headers', 200)
body = (r/'object.bin').read_bytes()
assert len(body) == o['size_bytes'] and hashlib.sha256(body).hexdigest() == o['sha256']
h = headers('range.headers', 206)
assert h['Content-Range'] == f'bytes 0-{size-1}/{o["size_bytes"]}'
assert int(h['Content-Length']) == size
assert (r/'range.bin').read_bytes() == body[:size]
print('Representative source HEAD/full GET/Range checks passed')
PY
```

This samples the first object, not the whole pool. Only after successful source
checks pass the exact exported manifest to the independent plan and Seed-cache
preparation commands. A normal source HTTP 200 is not a target readiness proof.

## Reuse and recovery

Keep the source bytes and URL stable across RATE changes, repeats and R/W changes.
Rebuild only the plan when R/W changes. B/N changes require a new dataset.
Server/mount/network failures require service repair, not regeneration; restore
the original endpoint instead of editing a published manifest. Server restart
never runs the generation Job. After a restart recheck manifest SHA and HTTP
content before reuse; preserve before/after evidence.

Keep `source.env`, rendered YAML, Job/Pod JSON, generation logs, exported manifest,
image references/imageIDs and HTTP checks. Source failures must not flow into
Seed preparation. Source resources use independent names/labels/selectors and
do not replace the original FileServer. Deleting this bundle's Job or server
does not delete the PVC; PVC removal is a separate data-destruction decision.
