FROM golang as build-stage
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
COPY liveness liveness
COPY replicate replicate
RUN go build -o kubernetes-replicator

FROM golang as production-stage
LABEL MAINTAINER="Aurelien Lambert <aure@olli-ai.com>"

COPY --from=build-stage /app/kubernetes-replicator /kubernetes-replicator
ENTRYPOINT  ["/kubernetes-replicator"]
