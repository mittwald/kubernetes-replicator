FROM        scratch
LABEL       MAINTAINER="Martin Helmich <m.helmich@mittwald.de>"
COPY        kubernetes-replicator /replicator
# run as an unprivileged user (the binary is static and does not need a passwd entry)
USER        65532:65532
ENTRYPOINT  ["/replicator"]
