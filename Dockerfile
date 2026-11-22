# Used by goreleaser (dockers_v2): the build context holds <os>/<arch>/forgejo-mcp.
FROM busybox:stable AS data
RUN mkdir -p /data && chown 65532:65532 /data

FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/forgejo-mcp /forgejo-mcp
COPY --from=data --chown=65532:65532 /data /data
WORKDIR /data
ENV FORGEJO_SAVE_DIR=/data/save
# stdio by default. HTTP mode: --transport http --addr 0.0.0.0:8787 (needs FORGEJO_MCP_BEARER).
ENTRYPOINT ["/forgejo-mcp"]
