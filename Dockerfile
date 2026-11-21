# GoReleaser builds this image from the binaries that it compiled before.
# The build context holds one directory for each platform, for example linux/amd64/.
FROM gcr.io/distroless/static:nonroot

ARG TARGETPLATFORM

# The nonroot user can create the downloads directory only in its home directory.
WORKDIR /home/nonroot

COPY $TARGETPLATFORM/atak-dl /usr/bin/atak-dl

EXPOSE 5000

# CMD holds the serve arguments, so "docker run <image> --version" replaces them.
ENTRYPOINT ["/usr/bin/atak-dl"]
CMD ["serve", "--addr", "0.0.0.0:5000"]
