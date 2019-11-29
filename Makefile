.PHONY: default build builder-image test clean-images clean push deploy

BINARY ?= kubernetes-replicator
DOCKER_IMAGE ?= kubernetes-replicator
# Default value "dev"
DOCKER_TAG ?= latest
DOCKER_REPOSITORY ?= asia.gcr.io/olli-iviet
REPOSITORY = ${DOCKER_REPOSITORY}/${DOCKER_IMAGE}:${DOCKER_TAG}

GOCMD = go
GOFLAGS ?= $(GOFLAGS:)
LDFLAGS =

default: build

install:
	"$(GOCMD)" mod download

build:
	"$(GOCMD)" build ${GOFLAGS} ${LDFLAGS} -o "${BINARY}"

builder-image:
	@docker build -t "${DOCKER_IMAGE}" .

# test:
# 	"$(GOCMD)" test -timeout 1800s -v ./...

clean-images:
	@docker rmi "${DOCKER_REPOSITORY}"

clean:
	"$(GOCMD)" clean -i

push: builder-image
	docker tag "${DOCKER_IMAGE}" "${REPOSITORY}"
	docker push "${REPOSITORY}"

deploy: build push
