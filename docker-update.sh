#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

IMAGE="cacao:latest"
CONTAINER="cacao"

case "$(uname -m)" in
	x86_64)
		arch="amd64"
		variant=""
		;;
	aarch64)
		arch="arm64"
		variant=""
		;;
	armv7l | armv7*)
		arch="arm"
		variant="v7"
		;;
	*)
		echo "unsupported architecture: $(uname -m)" >&2
		exit 1
		;;
esac

binary="dist/cacao-linux-${arch}${variant}"
if [ ! -f "$binary" ]; then
	echo "missing $binary, run build.bat first" >&2
	exit 1
fi

echo "removing old container..."
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true

echo "removing old image..."
docker rmi -f "$IMAGE" >/dev/null 2>&1 || true

echo "building new image..."
docker build -t "$IMAGE" -f Dockerfile.dist \
	--build-arg TARGETARCH="$arch" \
	--build-arg TARGETVARIANT="$variant" .

# First-run setup token for creating the initial admin. compose reads .env for
# ${CACAO_SETUP_TOKEN}. Fail closed: without a >=32 char token registration is
# rejected, so generate one before the container starts.
if ! grep -qE '^CACAO_SETUP_TOKEN=.{32,}' .env 2>/dev/null; then
	token="$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
	if grep -q '^CACAO_SETUP_TOKEN=' .env 2>/dev/null; then
		sed -i.bak "s|^CACAO_SETUP_TOKEN=.*|CACAO_SETUP_TOKEN=$token|" .env && rm -f .env.bak
	else
		printf 'CACAO_SETUP_TOKEN=%s\n' "$token" >> .env
	fi
	echo "generated setup token (use it on the first registration page):"
	echo "  $token"
fi

echo "starting new container..."
if docker compose version >/dev/null 2>&1; then
	docker compose up -d --no-build
elif command -v docker-compose >/dev/null 2>&1; then
	docker-compose up -d --no-build
else
	echo "docker compose not found" >&2
	exit 1
fi

echo "done"
