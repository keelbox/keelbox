# Preview a dev server

The agent publishes a dev server from the box's loopback at an HTTPS URL, such as `https://acme.tail1234.ts.net:8401/`. The URL opens only from devices on your tailnet, the private Tailscale network between your devices.

## Before the first preview

- The box is on the tailnet: `keelbox status acme` has a `tailnet` row. If not, run `keelbox login acme tailscale`.
- MagicDNS and HTTPS Certificates are on, on the DNS page of the Tailscale admin console.

## Publish

Ask the agent to expose its dev server, or do it yourself in `keelbox ssh acme`:

```sh
pnpm dev --host 127.0.0.1 --port 5173 &
keelbox expose 5173 --name web    # prints the URL
keelbox expose --list             # each preview, and whether its port is listening
keelbox expose --remove web
```

The first preview can take a minute while Tailscale issues the certificate. A preview answers 502 until its dev server runs. Each name keeps its URL until removed; `keelbox expose 5174 --name web` points it at another port. URL ports come from `previews.ports` (`8401-8499` by default).

Host checks such as Vite's pass without settings; the browser's host and scheme arrive in `X-Forwarded-Host` and `X-Forwarded-Proto`. A dev server that checks `Origin` needs the box's name allowed, as with `allowedDevOrigins` in Next.js. WebSockets work.

## Who can open a preview

Any device your Tailscale ACLs let reach the box, with no further sign-in. The certificate puts the box's MagicDNS name, including your tailnet's name, in public Certificate Transparency logs.

## When it does not work

`keelbox expose` and `keelbox doctor acme` name the cause and the fix. The logs:

```sh
keelbox ssh acme --admin
sudo journalctl -u keelbox-previews.service -u keelbox-preview-caddy.service
```

Without Tailscale, use an SSH tunnel: `ssh -N -L 3000:127.0.0.1:3000 agent@ADDRESS` serves the box's port 3000 at `http://localhost:3000`.

`previews.enabled: false` stops keelbox managing previews and removes nothing ([files on the box](../reference/box-files.md)).
