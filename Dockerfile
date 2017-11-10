FROM golang:1.8

COPY . /go/src/github.com/mittwald/kubernetes-replicator
WORKDIR /go/src/github.com/mittwald/kubernetes-replicator
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o replicator .

FROM scratch
MAINTAINER Martin Helmich <m.helmich@mittwald.de>

COPY --from=0 /go/src/github.com/mittwald/kubernetes-replicator/replicator /replicator

CMD ["/replicator"]
