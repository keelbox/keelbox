# 0060: Previews send the dev server its own Host

Date: 2026-11-10. Status: accepted. Amends ADR 0019 (previews).

## Context
Caddy's reverse proxy passes the browser's `Host` (`box.tailnet.ts.net:8401`) to the dev server by default. Vite 6 and later answer 403 to a host they do not know, unless the project sets `server.allowedHosts`; webpack-dev-server and others do the same. An agent asked to "expose the dev server" then hands the owner a URL that is refused, or edits the project's config to allow the tailnet name, which is not the project's business.

## Decision
- Each preview's route sets `Host` to the upstream it dials (`localhost:PORT`), `X-Forwarded-Host` to the browser's host and `X-Forwarded-Proto` to `https`.
- Caddy dials `127.0.0.1:PORT`, and `[::1]:PORT` when that is refused (first-available selection, a refused address marked down for a second). Dialling `localhost` would reach `127.0.0.1` only, which is all Ubuntu's `/etc/hosts` maps it to, so a server on `[::1]` alone would get 502.
- The browser's `Origin` is passed unchanged: checks on it (Next.js `allowedDevOrigins`) still need the name, and the guide says so.

## Consequences
- Stock Vite 6 and webpack-dev-server serve a preview without settings; `TestPreviewDevServer` checks Vite 6 with no `allowedHosts` answers 403 to the tailnet name directly and 200 through the preview.
- An app that builds absolute URLs from `Host` builds them for `localhost`; one that reads `X-Forwarded-Host` gets the tailnet name.
- Nothing changes in who can reach a preview: the host check of a dev server was never an access control here, the tailnet and Caddy's listeners are.
