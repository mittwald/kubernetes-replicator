FROM        scratch
LABEL       MAINTAINER="Martin Helmich <m.helmich@mittwald.de>"
COPY        kubernetes-replicator /replicator
ENTRYPOINT  ["/replicator"]