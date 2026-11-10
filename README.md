# halo-demo

Try [Halo](https://github.com/three-agent-problem/halo) without installing it:

```sh
ssh halo.3agents.dev
```

This is a small [Wish](https://github.com/charmbracelet/wish) SSH server. Every session gets its own `halo --demo` process, which plays a scripted run with no Lagrange server. Halo itself is unchanged: the image `go install`s a released Halo and runs the binary.

## How it works
- **Login:** anyone can connect, with any key, any password, or none at all.
- **One process per session:** each session runs `halo --demo` on a real PTY, so resizing your terminal reflows the TUI. It gets a throwaway `HOME` under `/tmp`, which is deleted when you disconnect.
- **Nothing but the demo:** commands (`ssh host ls`), subsystems (sftp/scp) and port forwarding are refused, and a session without a terminal is turned away.
- **Limits:**
  - Sessions close after 10 minutes idle, or 30 minutes in total.
  - At most 50 sessions run at once (`HALO_DEMO_MAX_SESSIONS`).
  - Connections are rate-limited per IP.

## Run locally
```sh
go run .                       # needs `halo` on PATH (or HALO_DEMO_BIN=/path/to/halo)
ssh -p 2222 localhost
```
Or with Docker:
```sh
docker compose up --build
ssh -p 2222 localhost
```

| Variable | Default |
|---|---|
| `HALO_DEMO_ADDR` | `:2222` |
| `HALO_DEMO_HOST_KEY` | `.ssh/halo_demo_ed25519` (in the image: `/data/host_ed25519`) |
| `HALO_DEMO_BIN` | `halo` |
| `HALO_DEMO_MAX_SESSIONS` | `50` |
| `HALO_VERSION` (build arg) | `v0.1.0-beta.1`. Use any tag, commit, or `main`. |

## Deploying (Coolify or any VPS)
SSH is not HTTP. A reverse proxy such as Traefik routes requests by domain using the Host header or TLS SNI, and SSH sends neither. So a domain can't route SSH to the container through the proxy; **the port has to be published on the host.**

1. **DNS:** create an `A` record for `halo.3agents.dev` pointing at the server. If you use Cloudflare, make it DNS only (grey cloud), because the orange-cloud proxy doesn't carry SSH.
2. **Port:** a plain `ssh halo.3agents.dev` connects to port 22, and the server's own sshd usually holds that port.
   - Move the admin sshd to another port. For example, set `Port 2200` in `/etc/ssh/sshd_config`, open 2200 in the firewall, then restart sshd. Keep your current session open until you've checked that a new login works.
   - Then deploy with `HALO_DEMO_PORT=22`, which compose maps to the container's 2222.
   - The other choice is to keep 2222 and tell people to run `ssh -p 2222 halo.3agents.dev`.
3. **Coolify:** deploy this repo as a Docker Compose resource. Leave its domain empty, because the proxy isn't involved. Set `HALO_DEMO_PORT` in the environment.
4. **Host key:** the `hostkey` volume keeps the server's host key. Don't delete it. A new key makes every returning visitor's ssh warn them that the host identification has changed.

## Hardening
The container is read-only except for `/tmp`, runs as an unprivileged user, drops all capabilities, and has `no-new-privileges`, a 1 GB memory limit and a 512-process limit.
