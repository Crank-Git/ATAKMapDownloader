# GoReleaser builds this image from the binaries that it compiled before.
# The build context holds one directory for each platform, for example linux/amd64/.
FROM gcr.io/distroless/static:nonroot

ARG TARGETPLATFORM

# The nonroot user can create the downloads directory only in its home directory.
WORKDIR /home/nonroot

COPY $TARGETPLATFORM/atak-dl /usr/bin/atak-dl

EXPOSE 5000

# The output files and jobs.jsonl stay in /data, so they outlive the container (FR-team-server-30).
# The image has no chown step, because docs/server.md gives the nonroot user write access to the volume.
VOLUME ["/data"]

# CMD holds the serve arguments, so "docker run <image> --version" replaces them.
# The image has no shell and no curl, so it has no HEALTHCHECK. A monitor reads GET /healthz.
ENTRYPOINT ["/usr/bin/atak-dl"]
CMD ["serve", "--addr", "0.0.0.0:5000", "--data-dir", "/data"]
