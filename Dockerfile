# GoReleaser builds this image from the binaries that it compiled before.
# The build context holds one directory for each platform, for example linux/amd64/.
FROM gcr.io/distroless/static:nonroot

ARG TARGETPLATFORM

# BuildKit creates a new WORKDIR owned by the image user, so a new named volume on /data is writable by nonroot.
WORKDIR /data
# The nonroot user can create the downloads directory only in its home directory.
WORKDIR /home/nonroot

COPY $TARGETPLATFORM/atak-dl /usr/bin/atak-dl

EXPOSE 5000

# The output files and jobs.jsonl stay in /data, so they outlive the container (FR-team-server-30).
# A bind mount keeps the owner of the host directory, so docs/server.md runs the container with the host user.
VOLUME ["/data"]

# CMD holds the serve arguments, so "docker run <image> --version" replaces them.
# The image has no shell and no curl, so it has no HEALTHCHECK. A monitor reads GET /healthz.
ENTRYPOINT ["/usr/bin/atak-dl"]
CMD ["serve", "--addr", "0.0.0.0:5000", "--data-dir", "/data"]
