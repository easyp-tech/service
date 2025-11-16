#!/bin/bash

# build-simple.sh

REGISTRY="${1:-localhost:5005}"
PUSH="${2}"

export DOCKER_DEFAULT_PLATFORM=linux/amd64

for dockerfile in $(find registry -name Dockerfile | sort); do
    dir=$(dirname "$dockerfile")
    path="${dir#./}"
    path="${path#registry/}"
    echo "Processing path: ${path}"

    if [[ "$path" =~ ^([^/]+)/([^/]+)/(.+)$ ]]; then
        type="${BASH_REMATCH[1]}"
        echo "Type: ${type}"
        lang="${BASH_REMATCH[2]}"
        echo "Language: ${lang}"
        version="${BASH_REMATCH[3]}"
        echo "Version: ${version}"

        tag="${REGISTRY}/${type}-${lang}:${version}"

        echo "Building ${tag}..."

        if docker build --platform linux/amd64 -t "${tag}" "${dir}"; then
            echo "✓ Built successfully"

            if [ "$PUSH" == "--push" ]; then
                echo "Pushing ${tag}..."
                docker push "${tag}"
            fi
        else
            echo "✗ Build failed"
        fi

        echo "---"
    fi
done

echo "Done!"
docker images | grep "${REGISTRY}"
