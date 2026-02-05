.PHONY: kind-up kind-down run control-plane cli demo image image-base image-sidecar images install-cli clean-sbx prepull

ENV_FILE ?=
CONFIG ?=

kind-up:
	@command -v kind >/dev/null 2>&1 || (echo "kind is not installed" >&2; exit 1)
	kind create cluster --name sandbox-dev
	kubectl cluster-info

kind-down:
	@command -v kind >/dev/null 2>&1 || (echo "kind is not installed" >&2; exit 1)
	kind delete cluster --name sandbox-dev

run:
	pkill -f control-plane || true
	@if [ -n "$(ENV_FILE)" ]; then set -a; . "$(ENV_FILE)"; set +a; fi; \
	if [ -n "$(CONFIG)" ]; then export SANDBOX_CONFIG="$(CONFIG)"; fi; \
	go run ./control-plane/cmd/control-plane


control-plane:
	go build -o bin/control-plane ./control-plane/cmd/control-plane

cli:
	go build -o bin/sbx ./cli/cmd/sbx

install-cli:
	mkdir -p $(HOME)/bin
	go build -o $(HOME)/bin/sbx ./cli/cmd/sbx

clean-sbx:
	kubectl get ns | rg '^sbx-' | awk '{print $$1}' | xargs -r kubectl delete ns --wait=false --grace-period=0

prepull:
	kubectl apply -f dev/prepull.yaml

image: image-base

image-base:
	cd images/sandbox-base && docker build -t sandbox-base:dev .
	kind load docker-image sandbox-base:dev --name sandbox-dev

image-sidecar:
	docker build -f images/stream-sidecar/Dockerfile -t sandbox-streamer:dev .
	kind load docker-image sandbox-streamer:dev --name sandbox-dev

images: image-base image-sidecar

demo:
	./dev/demo.sh
