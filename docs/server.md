# Run the server in Docker

This page tells a server operator how to run the project on a LAN host in Docker, so that a team shares one server (FR-team-server-31).

The image is `ghcr.io/crank-git/atak-map-downloader`. Replace `<tag>` with a release tag, for example `v0.2.0`.

## What the image runs

The image runs this command (FR-team-server-30):

```bash
atak-dl serve --addr 0.0.0.0:5000 --data-dir /data
```

- The server listens on port 5000 on every interface of the container.
- The address `0.0.0.0` is not loopback, so every route needs the token, apart from `GET /healthz` and the sign-in page.
- `/data` is a volume. It holds the output files and `jobs.jsonl`.
- The process runs as the `nonroot` user of the distroless base image, with the user ID 65532.

Arguments after the image name replace `serve --addr 0.0.0.0:5000 --data-dir /data`. For example, this command shows the version:

```bash
docker run --rm ghcr.io/crank-git/atak-map-downloader:<tag> --version
```

## Start the server

To keep the output files in the directory `./data` of the host, run this command:

```bash
mkdir -p data
docker run -d --name atak-dl \
  --user "$(id -u):$(id -g)" \
  -v ./data:/data \
  -e ATAKDL_TOKEN=abc \
  -p 5000:5000 \
  ghcr.io/crank-git/atak-map-downloader:<tag>
```

Replace `abc` with a long random token. Then open `http://<host>:5000` in a browser and sign in with the token.

### Give the server write access to the data volume

On a Linux host, a bind mount keeps the owner of the host directory. The `nonroot` user cannot write a directory that your host user owns. Thus, the server stops at start with this error:

```text
Error: data directory is not writable: /data (open /data/.atak-dl-write-check-…: permission denied)
```

Use one of these two choices:

- **A bind mount with your host user.** Add `--user "$(id -u):$(id -g)"`, as the command above does. The server then writes the files as your host user, and you can read them in `./data`.
- **A named volume.** Replace `-v ./data:/data` with `-v atak-dl-data:/data`, and remove `--user`. Docker makes a new named volume with the owner of `/data` in the image, which is the `nonroot` user.

Docker Desktop on macOS and Windows shares a host directory with any user of the container. On these hosts, the bind mount also works without `--user`.

## Compose

This `compose.yaml` file starts the same server with a named volume:

```yaml
services:
  atak-dl:
    image: ghcr.io/crank-git/atak-map-downloader:<tag>
    ports:
      - "5000:5000"
    environment:
      # Docker Compose reads ATAKDL_TOKEN from the shell or from the .env file next to compose.yaml.
      ATAKDL_TOKEN: ${ATAKDL_TOKEN:?set ATAKDL_TOKEN}
    volumes:
      - atak-dl-data:/data
    restart: unless-stopped

volumes:
  atak-dl-data:
```

To start the server, run these commands:

```bash
export ATAKDL_TOKEN=<a long random token>
docker compose up -d
```

To use a bind mount in place of the named volume, replace `atak-dl-data:/data` with `./data:/data`. Then add `user: "<uid>:<gid>"` with the output of `id -u` and `id -g`.

## The token

A server that is not on loopback needs the token. The server reads the token from the first of these sources:

1. The `--token` flag. Arguments after the image name replace all the serve arguments, so give them all: `serve --addr 0.0.0.0:5000 --data-dir /data --token <token>`.
2. The `ATAKDL_TOKEN` environment variable.
3. A generated token. With no flag and no variable, the server generates a 32-character token at start.

The server prints a generated token once to stderr. To read it, run `docker logs atak-dl`. A generated token changes at each restart, so set `ATAKDL_TOKEN` to keep one token.

A client sends the token in one of two ways:

- A script sends the header `Authorization: Bearer <token>`.
- A browser signs in at `/signin`, and the server sets the `atakdl_token` cookie.

The server ignores a token in the query string. After 10 failed token attempts in one minute from one IP address, the server returns 429 `too_many_attempts`.

## The data volume

The server writes these files in `/data`:

- The output file of each job, and the data package zip when the job asks for one.
- `jobs.jsonl`, the record of each job. The server reads it at start, so the job list stays after a restart.

`jobs.jsonl` holds no token. Back up the volume to keep the output files and the job list.

## The health route

`GET /healthz` returns 200 with the version of the binary. It needs no token, so a monitor can read it:

```bash
curl http://<host>:5000/healthz
```

```json
{"status":"ok","version":"0.2.0"}
```

The image has no shell and no `curl`, so the Dockerfile has no `HEALTHCHECK`. Point a monitor outside the container at `/healthz`.
