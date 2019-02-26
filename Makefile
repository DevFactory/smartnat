
# Image URL to use all building/pushing image targets
IMG ?= controller:latest

all: test manager

# Run tests
test: generate fmt vet manifests
	SNCTRLR_DEBUG_ENABLED=true \
	SNCTRLR_INTERFACES_PATTERN=docker0 \
	SNCTRLR_SETUP_MASQUERADE=true \
	SNCTRLR_SETUP_SNAT=false \
	SNCTRLR_DEFAULT_GW_INTERFACE=enp0s3 \
	SNCTRLR_IPTABLES_TIMEOUT_SEC=10 \
	SNCTRLR_AUTOREFRESH_PERIOD_SEC=10 \
	SNCTRLR_MAX_FINALIZE_WAIT_MINUTES=60 \
	go test ./pkg/... ./cmd/... -coverprofile cover.out

# Run tests for ci build
testci: generate fmt vet manifests
	SNCTRLR_DEBUG_ENABLED=true \
	SNCTRLR_INTERFACES_PATTERN=docker0 \
	SNCTRLR_SETUP_MASQUERADE=true \
	SNCTRLR_SETUP_SNAT=false \
	SNCTRLR_DEFAULT_GW_INTERFACE=enp0s3 \
	SNCTRLR_IPTABLES_TIMEOUT_SEC=10 \
	SNCTRLR_AUTOREFRESH_PERIOD_SEC=10 \
	SNCTRLR_MAX_FINALIZE_WAIT_MINUTES=60 \
	go test -v -coverprofile=cover.out ./...

# Build manager binary
manager: generate fmt vet
	go build -ldflags "-X main.version=`git describe`" -o bin/smartnat-manager github.com/DevFactory/smartnat/cmd/manager

managerci:
	go build -ldflags "-X main.version=`git describe`" -o bin/smartnat-manager github.com/DevFactory/smartnat/cmd/manager

# Run against the configured Kubernetes cluster in ~/.kube/config
run: generate fmt vet
	go run ./cmd/manager/main.go

# Install CRDs into a cluster
install: manifests
	kubectl apply -f config/crds

# Deploy controller in the configured Kubernetes cluster in ~/.kube/config
deploy: manifests
	kubectl apply -f config/crds
	kustomize build config/default | kubectl apply -f -

# Generate manifests e.g. CRD, RBAC etc.
manifests:
	go run vendor/sigs.k8s.io/controller-tools/cmd/controller-gen/main.go all

# Run go fmt against code
fmt:
	go fmt ./pkg/... ./cmd/...

# Run go vet against code
vet:
	go vet -composites=false ./pkg/... ./cmd/...

# Generate code
generate:
	go generate ./pkg/... ./cmd/...

# Build the docker image
docker-build: test
	docker build . -t ${IMG}
	@echo "updating kustomize image patch file for manager resource"
	sed -i'' -e 's@image: .*@image: '"${IMG}"'@' ./config/default/manager_image_patch.yaml

# Push the docker image
docker-push:
	docker push ${IMG}
