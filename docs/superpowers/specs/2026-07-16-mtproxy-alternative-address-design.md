# Alternative MTProxy Address Design

## Context

The bot currently generates one per-user MTProxy link from `TelemetConfig.ServerAddress`. The primary address is `194.87.80.237:9443`. Some users can connect over Wi-Fi but not over mobile networks, which may indicate IP-based filtering.

The RuVDS host already owns `87.247.157.120`. An external TCP check confirms that `87.247.157.120:9443` reaches the existing MTProxy path:

`RuVDS:9443 -> nftables redirect :29443 -> sing-box Reality tunnel -> Hetzner telemt:9443`

No additional proxy instance or user-secret synchronization is required.

## Goals

- Show every user both the primary and alternative MTProxy links in the bot.
- Reuse the user's existing MTProxy secret, TLS domain, and port.
- Provide a QR code for each address.
- Preserve the existing single-link behavior when no alternative address is configured.
- Avoid changing or restarting telemt, sing-box, nftables, or nginx.

## Non-goals

- Automatic reachability selection inside Telegram.
- Adding alternative-address management to the database or admin panel.
- Rotating user secrets.
- Changing the existing primary address.

## Configuration

Add an optional environment variable:

```dotenv
TELEMT_ALT_SERVER_ADDRESS=87.247.157.120
```

An environment variable keeps the operational address outside the binary and avoids a database migration. If the value is empty or equals the primary address, the bot returns only one link.

## Bot Behavior

The existing `Telegram Proxy` button remains unchanged. Its handler returns:

1. `Основная ссылка` using `TelemetConfig.ServerAddress`.
2. `Резервная ссылка` using `TELEMT_ALT_SERVER_ADDRESS`.

Both links use the same `TelemetUser.Secret`, `TelemetConfig.Port`, and `TelemetConfig.TLSDomain`.

The existing `QR Proxy` button sends one labeled QR image for the primary link and, when configured, a second labeled QR image for the alternative link.

## Code Structure

- Replace the single-link helper with a helper that returns a small result containing primary and optional alternative links.
- Keep user lookup and atomic `TelemetUser` creation in one place so both links always share one secret.
- Deduplicate addresses before formatting links.
- Keep link generation in `service.GenerateTelemetProxyLink`; only address selection belongs in the bot package.

## Error Handling

- Existing user, configuration, and secret-creation errors remain unchanged.
- Failure to generate either QR returns the existing user-facing QR error.
- Failure while sending the second QR is returned to telebot and logged through the existing bot error path.
- An empty alternative address is not an error and preserves legacy behavior.

## Verification

- Unit tests cover one-link behavior, two-link behavior, address deduplication, and reuse of the same secret/domain/port.
- Run `go test ./...`, `go vet ./...`, and `go build -o vpnbot .` locally.
- Build the production binary, keep a backup of the deployed binary, add `TELEMT_ALT_SERVER_ADDRESS` to `/opt/VpnBot/.env`, and restart only `vpnbot`.
- Verify that the bot returns two differently addressed links and two labeled QR codes for a test user.
- Confirm both `194.87.80.237:9443` and `87.247.157.120:9443` remain externally reachable.

## Success Criteria

- All active users see both MTProxy links without receiving new secrets.
- Existing primary links remain valid.
- The alternative link uses `87.247.157.120:9443` and connects through the existing production MTProxy path.
- No MTProxy data-plane service is restarted or reconfigured.
