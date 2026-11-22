# Preview a dev server

```sh
keelbox expose 5173 --name web
```

The agent runs it on the box. It publishes a dev server from the box's loopback at an HTTPS URL, such as `https://acme.tail1234.ts.net:8401/`. The URL opens from devices on your tailnet, the private Tailscale network between your devices, and from nowhere else.

## Before the first preview

- The box is on the tailnet: `keelbox status acme` has a `tailnet` row, and `keelbox login acme tailscale` joins it.
- The tailnet issues HTTPS certificates: MagicDNS and HTTPS Certificates are on, on the DNS page of the Tailscale admin console.

Previews are on wherever Tailscale is. `previews.enabled: false` in the config stops keelbox managing them and removes nothing: [what is on the box](../reference/box-files.md).

## Publish

Ask the agent to expose its dev server, or do it yourself in `keelbox ssh acme`:

```sh
pnpm dev --host 127.0.0.1 --port 5173 &
keelbox expose 5173 --name web
keelbox expose --list
keelbox expose --remove web
```

`expose` prints the URL. The first preview on a box waits for Tailscale to issue the certificate, which can take a minute.

Each name gets the lowest free port in `previews.ports`, `8401-8499` unless the config says otherwise, and keeps it until removed. Without `--name` the preview is named after the port. `keelbox expose 5174 --name web` points `web` at another local port, at the same URL.

`--list` shows each preview and whether something listens on its port now. A preview answers 502 until its dev server runs.

## Dev server settings

The proxy sends the dev server its own address as `Host`, so a host check such as Vite's passes without settings. The browser's name and scheme arrive in `X-Forwarded-Host` and `X-Forwarded-Proto`.

The browser's `Origin` header passes unchanged. A dev server that checks it needs the box's name allowed, as with `allowedDevOrigins` in Next.js.

WebSockets pass through, and a dev server on `::1` is reached as well as one on `127.0.0.1`.

## Who can open a preview

Any device your Tailscale ACLs let reach the box, with no further sign-in. The proxy listens on the box's tailnet addresses only.

Requesting the certificate puts the box's MagicDNS name, your tailnet's name included, in public Certificate Transparency logs.

## When it does not work

`keelbox expose` and `keelbox doctor acme` name the cause and the fix. The services' logs are read as the admin, the box's account with sudo:

```sh
keelbox ssh acme --admin
sudo journalctl -u keelbox-previews.service -u keelbox-preview-caddy.service
```

## Without Tailscale

An SSH tunnel reaches a loopback port from one computer. `ssh -N -L 3000:127.0.0.1:3000 agent@ADDRESS`, with the box's address, serves it at `http://localhost:3000`.
