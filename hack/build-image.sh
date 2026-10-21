#!/bin/sh
# Build locally using ordinary Docker unless platforms are explicitly requested.
set -eu

if [ "$#" -ne 4 ]; then
    echo "Usage: sh hack/build-image.sh build|push IMAGE DOCKERFILE PLATFORMS" >&2
    exit 2
fi
action=$1
image=$2
dockerfile=$3
platforms=$4
case "$action" in
    build|push) ;;
    *) echo "Unknown action: $action" >&2; exit 2 ;;
esac

if [ -z "$platforms" ]; then
    docker build -t "$image" -f "$dockerfile" .
    if [ "$action" = push ]; then
        docker push "$image"
    fi
else
    if ! docker buildx version >/dev/null 2>&1; then
        echo "Explicit PLATFORMS requires Docker Buildx; omit PLATFORMS for a native docker build." >&2
        exit 2
    fi
    if [ "$action" = push ]; then
        docker buildx build --platform "$platforms" --push -t "$image" -f "$dockerfile" .
    else
        case "$platforms" in
            *,*)
                echo "Multiple platforms require the docker-push target; local builds accept one platform with --load." >&2
                exit 2
                ;;
        esac
        docker buildx build --platform "$platforms" --load -t "$image" -f "$dockerfile" .
    fi
fi
