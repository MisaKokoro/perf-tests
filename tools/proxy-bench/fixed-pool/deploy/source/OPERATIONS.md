# Fixed-pool source: deployment walkthrough

Start with [the source overview](README.md) for capabilities, prerequisites and
configuration. Run these steps from this directory in one shell session. Fill
only the environment-specific inputs; the Python snippets check or read those
inputs and actual results, rather than generating replacement evidence.

All steps apply to a new dataset, except the explicitly optional new-PVC step.
For an existing unchanged source, use the reuse/recovery rules in the overview.

## 1. Copy and edit the configuration

The copy is a template, not a ready-to-run environment. Fill images, node, PVC,
final BASE_URL and B/N before loading it. Review the file as trusted shell input.

```sh
set -eu
cp -n source.env.example source.env
# Edit source.env before continuing: images, node, PVC and experiment settings.
```

## 2. Load settings, check them, then render

`set -a` exports the loaded settings for envsubst and Python. The BASE_URL default
is applied once; it does not provision a network endpoint. Confirm the resulting
address is the one that the Seed will use.

```sh
set -a
. ./source.env
set +a
: "${DNS_DOMAIN:=cluster.local}"
: "${BASE_URL:=http://${SOURCE_NAME}.${NAMESPACE}.svc.${DNS_DOMAIN}}"
export DNS_DOMAIN BASE_URL
```

This check rejects missing values, mutable image references and nonpositive B/N
or generation concurrency before any cluster resources are created. Correct
source.env when it fails; do not bypass the check.

```sh
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
```

The whitelist names the variables envsubst may replace. Rendering writes local
Job and server YAML to a new RENDERED directory, then checks for unexpanded
variables. The final commands check the namespace/node and ask the Kubernetes
API to validate the resources without creating the Job or server.

```sh
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

## 3. Prepare the PVC

For a new PVC only, fill PVC_STORAGE_CLASS/PVC_CAPACITY, inspect the rendered
resource and run this block. Skip it for an existing PVC: do not apply the
optional example over that volume.

```sh
: "${PVC_CAPACITY:?set the new PVC capacity}"
envsubst "$VARS" < pvc.example.yaml > "$RENDERED/pvc.yaml"
kubectl apply --dry-run=server -f "$RENDERED/pvc.yaml"
kubectl apply -f "$RENDERED/pvc.yaml"
```

Save the actual PVC configuration and inspect capacity, access mode, node affinity
and permissions. Generation runs with the configured UID/GID. A
WaitForFirstConsumer claim may bind only when the Job is scheduled.

```sh
kubectl get pvc -n "$NAMESPACE" "$PVC_NAME" -o yaml > "$RENDERED/pvc-before.yaml"
```

## 4. Generate data and preserve the actual result

Apply only the data Job. It directly invokes proxy-bench-data-prepare on the final
PVC; it does not start an HTTP server. Stop on failure and keep the Job/Pod facts,
logs and residual files. Deleting a failed Job does not empty its dataset.

```sh
kubectl apply -f "$RENDERED/dataset-job.yaml"
if ! kubectl wait -n "$NAMESPACE" --for=condition=complete \
    "job/${SOURCE_NAME}-data" --timeout=30m; then
  kubectl get job,pods -n "$NAMESPACE" -o wide
  kubectl logs -n "$NAMESPACE" "job/${SOURCE_NAME}-data" > "$RENDERED/failed-generation.log" 2>&1 || true
  exit 1
fi
```

Save Job/Pod facts and require one successful generation Pod with container exit
0. More than one actual Pod requires inspection, not selection of a "latest"
result. Read the log from that Pod and keep it unchanged.

```sh
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
```

Kubectl logs merges stdout and stderr. The next snippet locates exactly one
original data-result JSON line, checks success, and saves its original bytes as
dataset-result.json. It does not manufacture a manifest reference from a directory
scan or treat diagnostic text as JSON.

```sh
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
```

Only after preserving those facts, delete the completed Job to release its volume
mount before starting the server. The PVC and dataset remain.

```sh
# Release the completed Job's volume before starting the server. The PVC stays.
kubectl delete job -n "$NAMESPACE" "${SOURCE_NAME}-data" --wait=true
```

Controller re-execution is still possible. Generator locking and conflict checks
protect the dataset; do not assume a Job ran exactly once or adopt residual data.

## 5. Start the server and export the manifest

The server reads the same dataset through the read-only mounts described in the
overview. Save its actual Pod/imageID and verify the configured image's nginx root
and port. Pod Ready alone does not establish that the source bytes are correct.

```sh
kubectl apply -f "$RENDERED/file-server.yaml"
kubectl rollout status -n "$NAMESPACE" "statefulset/${SOURCE_NAME}" --timeout=5m
SERVER_POD="${SOURCE_NAME}-0"
kubectl get pod -n "$NAMESPACE" "$SERVER_POD" -o json > "$RENDERED/server-pod.json"
```

Read the manifest ID from the actual successful Job result. The generation path
is /data/dataset/manifests/ID/dataset-manifest.json; the server sees the same file
at /fixed-pool-data/manifests/ID/dataset-manifest.json. Export it unchanged and
compare its raw SHA-256 to the generation result before handing it to other tools.

```sh
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

## 6. Check the published HTTP source

These checks are required before Seed preparation. Run them on a machine/network
that can access the exact manifest URLs as the Seed will. If the operator terminal
has different routing/DNS, copy the manifest unchanged and set RENDERED to that
copy's directory on a suitable machine. Do not replace the URL with localhost.
Provision the previously chosen external endpoint first, if applicable.

The first block selects the first object's URL and saves three responses: HEAD
(headers only), full GET (the complete object), and a Range of at most 64 KiB.
Accept-Encoding: identity avoids compressed bytes; --fail makes HTTP errors fail.

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
```

The independent check compares the saved response status/headers and bytes to the
manifest: HEAD 200 and size; full length and SHA-256; Range 206, Content-Range,
length and matching prefix. The sample object may require substantial local disk
space at large B. This is a source check, not a whole-pool Seed readiness proof.

```sh
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

Success prints Representative source HEAD/full GET/Range checks passed. Preserve
the headers/body evidence as needed and hand the exact exported manifest to plan
or Seed preparation. Those commands perform their own input/target checks.

For source failures, restore the original URL/mount/service and repeat the source
checks without rerunning successful generation. Keep the configuration, resources,
Pod/image facts, logs, manifest and check evidence with this dataset.
