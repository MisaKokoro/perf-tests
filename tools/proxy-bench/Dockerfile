FROM alpine:3.22 AS worker-binary

ARG TARGETARCH
COPY bin/proxy-bench-tools/ /binaries/
RUN set -eu; \
    arch="${TARGETARCH:-}"; \
    if [ -z "$arch" ]; then \
        case "$(uname -m)" in \
            x86_64) arch=amd64 ;; \
            aarch64|arm64) arch=arm64 ;; \
            *) echo "Unsupported worker architecture: $(uname -m)" >&2; exit 1 ;; \
        esac; \
    fi; \
    mkdir -p /out; \
    cp "/binaries/$arch/proxy-bench-worker-support" /out/proxy-bench-worker-support; \
    chmod 755 /out/proxy-bench-worker-support

FROM grafana/k6:2.3.0

ENV K6_NO_USAGE_REPORT=true

COPY tools/proxy-bench/proxy-bench.js /home/k6/proxy-bench.js

COPY tools/proxy-bench/fixed-pool/benchmark.js tools/proxy-bench/fixed-pool/run-benchmark.sh /home/k6/fixed-pool/
COPY tools/proxy-bench/fixed-pool/lib /home/k6/fixed-pool/lib
COPY --from=worker-binary /out/proxy-bench-worker-support /usr/local/bin/

ENTRYPOINT ["k6", "run", "/home/k6/proxy-bench.js"]
