#!/bin/bash
# Runs on the AWS instance through Session Manager, as root.
# Usage: aws-deploy.sh IMAGE_URI
# Pulls the image, swaps it in as messwa:latest and restarts the service.
# If /healthz doesn't answer within 90 seconds, it puts the previous image
# back and fails.
set -euo pipefail

image=$1
dir=/opt/messwa
export HOME=/root

health() {
	local ip
	ip=$(tailscale ip -4)
	for _ in $(seq 1 30); do
		if curl -fsS -m 3 "http://$ip:8766/healthz" >/dev/null 2>&1; then
			return 0
		fi
		sleep 3
	done
	return 1
}

echo "pulling $image"
docker pull -q "$image"

if docker image inspect messwa:latest >/dev/null 2>&1; then
	docker tag messwa:latest messwa:previous
fi
docker tag "$image" messwa:latest

cd "$dir"
docker compose up -d --no-build

if health; then
	echo "healthy on $(docker inspect messwa --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')"
	docker image prune -f >/dev/null
	exit 0
fi

echo "messwa did not become healthy; recent logs:"
docker logs --tail 40 messwa 2>&1 || true
if docker image inspect messwa:previous >/dev/null 2>&1; then
	echo "rolling back to the previous image"
	docker tag messwa:previous messwa:latest
	docker compose up -d --no-build
	health && echo "rollback healthy" || echo "rollback is not healthy either"
fi
exit 1
