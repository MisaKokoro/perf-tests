#!/usr/bin/env python3
"""Render source deployment YAML locally using only the Python standard library."""

import argparse
import json
import pathlib
import re
import shlex
import sys
from urllib.parse import urlsplit


HERE = pathlib.Path(__file__).resolve().parent


def read_config(path):
    values = {}
    for number, line in enumerate(path.read_text().splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        match = re.fullmatch(r"([A-Z][A-Z0-9_]*)=(.*)", line)
        if not match:
            raise ValueError(f"{path}:{number}: expected NAME=value")
        key, raw = match.groups()
        words = shlex.split(raw, comments=True)
        if len(words) > 1 or key in values:
            raise ValueError(f"{path}:{number}: duplicate key or unquoted whitespace")
        value = words[0] if words else ""
        if any(c in value for c in "\n\r\x00$`"):
            raise ValueError(f"{path}:{number}: use literal values, without shell expansion")
        values[key] = value
    return values


def validate(c, existing_pvc):
    optional = {"BASE_URL", "PVC_STORAGE_CLASS"}
    if existing_pvc:
        optional.add("PVC_CAPACITY")
    for key, value in c.items():
        if not value and key not in optional:
            raise ValueError(f"请填写 {key}")
    for key in ("TOOLS_IMAGE", "FILE_SERVER_IMAGE"):
        image = c[key]
        # Accept ordinary image names/tags as well as pinned SHA-256 digests.
        # Registry ports belong to the repository, not the final tag component.
        repository, separator, digest = image.partition("@")
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._:/-]*", repository) or "//" in repository:
            raise ValueError(f"{key} 必须为镜像名、镜像名:版本或镜像名@sha256:摘要")
        last = repository.rsplit("/", 1)[-1]
        if not last or (":" in last and not re.fullmatch(r"[^:]+:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}", last)):
            raise ValueError(f"{key} 的镜像名称或版本标签无效")
        if separator and not re.fullmatch(r"sha256:[a-f0-9]{64}", digest):
            raise ValueError(f"{key} 的 sha256 摘要必须为 64 位小写十六进制字符")
    for key in ("B", "N", "GENERATION_WORKERS", "DATA_UID", "DATA_GID", "FS_GROUP"):
        if not re.fullmatch(r"0|[1-9][0-9]*", c[key]):
            raise ValueError(f"{key} 必须是十进制整数（不带前导零）")
        if key in ("B", "N", "GENERATION_WORKERS", "DATA_UID") and int(c[key]) == 0:
            raise ValueError(f"{key} 必须大于 0")
    for key in ("B", "N"):
        if int(c[key]) > 2**53 - 1:
            raise ValueError(f"{key} 超过工具支持的整数范围")
    label = r"[a-z0-9](?:[a-z0-9-]*[a-z0-9])?"
    for key in ("SOURCE_NAME", "NAMESPACE", "PVC_NAME", "SOURCE_NODE", "DNS_DOMAIN", "PVC_STORAGE_CLASS"):
        value = c[key]
        if key == "PVC_STORAGE_CLASS" and not value:
            continue
        if len(value) > 253 or any(len(p) > 63 or not re.fullmatch(label, p) for p in value.split(".")):
            raise ValueError(f"{key} 不是有效的 DNS 名称")
    if not re.fullmatch(r"[a-z](?:[a-z0-9-]*[a-z0-9])?", c["SOURCE_NAME"]) or len(c["SOURCE_NAME"]) > 58:
        raise ValueError("SOURCE_NAME 须以字母开头，仅含小写字母、数字、连字符，最多 58 字符")
    if "." in c["NAMESPACE"] or len(c["NAMESPACE"]) > 63:
        raise ValueError("NAMESPACE 必须是最多 63 字符的 DNS label")
    quantity = r"(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+|[numkMGTPE]|[KMGTPE]i)?"
    for key in c:
        if key.endswith(("_REQUEST", "_LIMIT")) or (key == "PVC_CAPACITY" and not existing_pvc):
            if not re.fullmatch(quantity, c[key]):
                raise ValueError(f"{key} 不是有效的资源数量")
    if not c["BASE_URL"]:
        c["BASE_URL"] = f'http://{c["SOURCE_NAME"]}.{c["NAMESPACE"]}.svc.{c["DNS_DOMAIN"]}'
    url = urlsplit(c["BASE_URL"])
    if url.scheme != "http" or not url.hostname or url.username is not None or url.password is not None or url.query or url.fragment or any(ch.isspace() for ch in c["BASE_URL"]):
        raise ValueError("BASE_URL 必须为无认证信息、query、fragment 的 HTTP 来源地址")
    _ = url.port


def render(template, config):
    # YAML accepts JSON-quoted strings. Quote complete scalar values, including
    # suffixes such as ${SOURCE_NAME}-data; keep security-context IDs numeric.
    lines = []
    for line in template.splitlines():
        if "${" not in line:
            lines.append(line)
            continue
        match = re.fullmatch(r'(\s*(?:[\w./-]+:\s*|-\s+))(.*)', line)
        if not match:
            raise ValueError(f"unsupported template line: {line}")
        prefix, scalar = match.groups()
        scalar = scalar.strip('"')
        numeric = scalar in ("${DATA_UID}", "${DATA_GID}", "${FS_GROUP}")
        scalar = re.sub(r"\$\{([A-Z0-9_]+)\}", lambda m: config[m[1]], scalar)
        if "${" in scalar:
            raise ValueError("unresolved template variable")
        lines.append(prefix + (scalar if numeric else json.dumps(scalar, ensure_ascii=False)))
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser(description="读取配置并生成来源部署 YAML；不连接或修改集群。")
    parser.add_argument("--config", type=pathlib.Path, default=pathlib.Path("source.env"))
    parser.add_argument("--output", type=pathlib.Path, default=pathlib.Path("rendered-source"))
    parser.add_argument("--existing-pvc", action="store_true", help="使用已有 PVC，不生成 pvc.yaml")
    args = parser.parse_args()
    config = read_config(HERE / "source.env.example")
    overrides = read_config(args.config)
    unknown = overrides.keys() - config.keys()
    if unknown:
        raise ValueError("未知配置项：" + ", ".join(sorted(unknown)))
    config.update(overrides)
    validate(config, args.existing_pvc)
    files = {"dataset-job.yaml": "dataset-job.tpl.yaml", "file-server.yaml": "file-server.tpl.yaml"}
    if not args.existing_pvc:
        files["pvc.yaml"] = "pvc.example.yaml"
    outputs = {name: render((HERE / template).read_text(), config) for name, template in files.items()}
    # A fresh/empty directory avoids mixing new files with stale PVC manifests.
    if args.output.exists() and (not args.output.is_dir() or any(args.output.iterdir())):
        raise ValueError(f"输出目录必须为空或不存在：{args.output}")
    args.output.mkdir(parents=True, exist_ok=True)
    for name, content in outputs.items():
        (args.output / name).write_text(content)
    print(f'BASE_URL={config["BASE_URL"]}')
    for name in outputs:
        print(f"已生成：{args.output / name}")
    print("仅完成本地渲染；尚未执行 Kubernetes API 检查或部署。")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError) as error:
        print(f"错误：{error}", file=sys.stderr)
        sys.exit(1)
