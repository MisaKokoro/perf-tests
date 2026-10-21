# Fixed-pool HTTP source

Publish a generated dataset as an HTTP source that a Dragonfly Seed can download.
The dataset is generated on a PVC, then served read-only by an independent nginx
FileServer. Generate it once and reuse the same source across test rounds.

This directory provides Kubernetes deployment materials. It does not introduce a
new deployment command or install Kubernetes/Dragonfly. The existing
`proxy-bench-data-prepare` command runs inside the data Job.

## What this bundle provides

| File | Purpose |
|---|---|
| [source.env.example](source.env.example) | Shared settings for the dataset, source address, images and storage. |
| [dataset-job.tpl.yaml](dataset-job.tpl.yaml) | Generate objects and their manifest on the final PVC. |
| [file-server.tpl.yaml](file-server.tpl.yaml) | Serve the objects through an independent StatefulSet and Service. |
| [pvc.example.yaml](pvc.example.yaml) | Optional PVC for a new dataset; skip when using an existing PVC. |

The output is a reachable HTTP source and an exported `dataset-manifest.json`.
Pass that manifest to the separate plan and Seed-cache preparation commands.
This bundle does not run a benchmark or establish that a Seed is ready.

## Before starting

Provide a working cluster, a namespace, a source node and suitable storage.
The operator terminal needs kubectl, a POSIX shell, envsubst, Python 3, curl and
sha256sum. The tools themselves are Go binaries; Python is used only by the
operator-side configuration, result-handoff and HTTP checks in the walkthrough.

Provide two image references that the source node can obtain. The renderer accepts
image names, `repository:tag`, or `repository@sha256:...`; prefer digests when
exact image reproducibility is required:

- **TOOLS_IMAGE:** the three preparation binaries built with the
  [preparation tools Dockerfile](../../Dockerfile.tools).
- **FILE_SERVER_IMAGE:** nginx serving `/usr/share/nginx/html` on port 80, with
  HEAD and Range support. The existing
  [FileServer image](../../../../file-server/Dockerfile) provides this capability.

See [build instructions](../../README.md#build). Building a local OCI archive
does not distribute or publish it; arrange image delivery before deploying.

## Configure this dataset

### 一条命令生成部署 YAML（推荐）

在本目录执行，只需要 Python 3，不需要设置环境变量或安装 envsubst。
创建 `source.env`，填写实际环境参数，例如：

```dotenv
SOURCE_NODE=your-source-node
TOOLS_IMAGE=your-registry/proxy-bench-tools:your-version
FILE_SERVER_IMAGE=your-registry/file-server:your-version
PVC_STORAGE_CLASS=your-storage-class
# Seed 不在集群网络中时，填写已经规划好的可达 HTTP 入口。
# BASE_URL=http://your-source-endpoint
```

未填写的配置自动沿用 [source.env.example](source.env.example)：4 个 4MiB
对象、1Gi PVC、benchmark 命名空间等。可以在自己的配置中覆盖任意示例参数。
配置使用 `NAME=value`，支持注释和引号，不执行 shell 命令、不展开变量。
镜像支持 `仓库:版本` 和 `仓库@sha256:摘要`，也支持不带标签的镜像名。
`PVC_STORAGE_CLASS` 默认空字符串，表示显式不使用 StorageClass；需要动态申请
存储时请填写集群中实际存在的存储类。

```bash
python3 render.py --config source.env --output rendered-source
```

已有 PVC 时，在配置中设置 `PVC_NAME`，执行：

```bash
python3 render.py --config source.env --output rendered-source --existing-pvc
```

产物为 `dataset-job.yaml`、`file-server.yaml`，以及新建卷时的 `pvc.yaml`。
输出目录必须为空或不存在，避免覆盖旧配置或混入旧的 PVC 文件。
固定配置后直接保留这些 YAML，无须每轮重新渲染。这个脚本替代手动加载配置、
校验和 envsubst；仅在本地生成文件，不运行 kubectl，不生成对象正文。

部署前可分别执行服务端检查：

```bash
kubectl apply --dry-run=server -f rendered-source/dataset-job.yaml
kubectl apply --dry-run=server -f rendered-source/file-server.yaml
# 仅新建 PVC 时执行：
kubectl apply --dry-run=server -f rendered-source/pvc.yaml
```

之后按下文操作说明，依次申请 PVC、运行数据 Job、保存生成结果、删除完成的
Job，再发布 FileServer。不要直接 `kubectl apply -f rendered-source/` 同时启动
全部资源：FileServer 使用的子目录由数据 Job 创建。配置文件不变可以复用 YAML，
但已有数据的 PVC 不能重新运行生成任务；数据生成器要求 dataset 目录为空。

### 手动渲染方式

The legacy manual checks in OPERATIONS.md still require digest references;
use render.py for image names or tags.

From this directory, copy `source.env.example` to `source.env`, then edit it.
The [deployment walkthrough](OPERATIONS.md) gives the commands in execution order.

| Setting | What to provide |
|---|---|
| SOURCE_NAME / NAMESPACE | An independent source name and an existing namespace. |
| SOURCE_NODE | The actual source node; there is no usable default. |
| TOOLS_IMAGE / FILE_SERVER_IMAGE | Reachable image names, tags or SHA-256 digest references (render.py). |
| PVC_NAME | The dataset volume; check capacity, placement and write permissions. |
| BASE_URL | The final HTTP address reachable from the Seed. |
| B / N | Object bytes and count; the small example is 4194304 bytes times four. |
| PVC_STORAGE_CLASS / PVC_CAPACITY | For a new PVC only; select values for your cluster. |

When BASE_URL is empty, the configuration derives
`http://SOURCE_NAME.NAMESPACE.svc.DNS_DOMAIN`. Use this only where the Seed can
resolve and reach that Service address. An external Seed needs a provisioned
endpoint and an explicit BASE_URL; setting a URL does not create an ingress or
port forward. Choose the address before generation: it becomes part of the
immutable manifest.

The example starts with generation concurrency 1, DNS domain `cluster.local`,
UID/GID/fsGroup 1000 and small resource requests/limits. These are functional
starting values to review against your storage and cluster, not capacity-test
defaults. Existing PVC users do not apply the new-PVC example.

## Deploy once per dataset

Follow [OPERATIONS.md](OPERATIONS.md) in one shell session:

| Step | Action | Result to check before continuing |
|---|---|---|
| 1 | Load/check source.env and render the templates. | Final BASE_URL and valid Job/server YAML. |
| 2 | Prepare storage; create a PVC only if needed. | Correct volume and an empty/absent dataset directory. |
| 3 | Run the data Job and preserve its actual result. | Successful Pod, original log and manifest reference. |
| 4 | Start nginx and export the referenced manifest. | Local manifest SHA-256 matches the generation result. |
| 5 | Check the exact manifest URLs from the Seed's network. | HEAD, full-body digest and HTTP 206 Range pass. |

The walkthrough explains the longer configuration and JSON-checking snippets,
including their inputs and outputs. Result handoff and source checks are required;
they are not repeated for every request or every unchanged test round.

## Where the data goes

The Job mounts the PVC at `/data` and generates only `dataset/`. The PVC root may
contain `lost+found` or unrelated files; the dataset directory must be empty or
absent. Storage must support sequential mounts by the Job and the server;
ReadWriteOnce does not mean only one Pod, and ReadWriteMany is not required.

| PVC path | Server mount | Use |
|---|---|---|
| dataset/objects | /usr/share/nginx/html/objects, read-only | HTTP object downloads. |
| dataset | /fixed-pool-data, read-only | Export the manifest with kubectl. |

Objects are available as `BASE_URL/objects/sha256/DIGEST`. Other files already in
the FileServer image remain available. Manifests are not published as HTTP files;
export the exact path from the successful generation result, rather than scanning
for a "latest" manifest.

## Reuse and recovery

- Repeated rounds, RATE changes and R/W changes reuse the source bytes and URL.
  R/W changes need a separate access plan, not another data Job.
- B/N changes require a new dataset. Keep the old manifest and objects immutable.
- Server, mount or network failures require service repair and HTTP rechecks.
  Restarting nginx does not regenerate data. Restore the original endpoint.
- Preserve failed generation logs and remnants before choosing a new empty
  dataset or inspecting ownership for manual cleanup. Do not adopt partial data.
- Deleting the completed Job or restarting the server does not delete the PVC.

Retain source.env, rendered resources, Job/Pod facts and imageIDs, generation
logs, the exported manifest and HTTP evidence. Once the source checks pass,
continue with [plan preparation](../../README.md#prepare-or-reuse-an-access-plan)
and [Seed preparation](../../README.md#prepare-a-target-seed-cache).
