# Dedicated VK-SNI MTProxy Design for `@sch_42`

## Context

`@sch_42` can use the existing MTProxy over Wi-Fi but not through MegaFon. Packet captures show that MegaFon reaches the RuVDS endpoint and exchanges TCP payload, while the existing `telemt` service accepts the user's secret and records traffic. Changing only the public IP from `194.87.80.237` to `87.247.157.120` did not help because both links still use port `9443`, the same FakeTLS SNI (`lk.rt.ru`), and the same tunnel path.

The existing production path must remain unchanged:

`client:9443 -> RuVDS nftables -> sing-box:29443 -> Reality/VLESS -> Hetzner telemt:9443`

## Goal

Create an isolated experimental MTProxy link for Telegram user `user_388535440` with:

- public endpoint `87.247.157.120:443`;
- FakeTLS SNI `vk.com`;
- the user's existing 32-hex secret;
- separate services and statistics;
- no changes to existing MTProxy links or users.

Adding the link to the bot is out of scope until the user confirms that the experimental endpoint works through MegaFon.

## Alternatives Considered

1. **Dedicated endpoint and services (selected).** A second RuVDS sing-box tunnel and a second Hetzner telemt instance isolate the experiment and make rollback straightforward.
2. **Run telemt directly on RuVDS.** Rejected because the existing RuVDS telemt unit conflicts with nginx on port `9443`, and direct Telegram egress would introduce another variable.
3. **Route multiple SNIs through the existing `9443` listener.** Rejected because it would require changing the shared nftables/nginx path and could disrupt working users.

## Architecture

The experimental path is:

`@sch_42 -> 87.247.157.120:443 -> nftables -> sing-box-mt-vk:29444 -> existing Reality/VLESS entry on Hetzner:8765 -> telemt-sch42:19444`

### RuVDS

- Add a destination-specific nftables rule before the generic port-`443` DNAT rule:
  - destination `87.247.157.120`;
  - TCP destination port `443`;
  - redirect to local port `29444`.
- Keep `194.87.80.237:443` and all existing VPN forwarding unchanged.
- Run a new `sing-box-mt-vk.service` using its own config.
- The new sing-box direct inbound listens on `0.0.0.0:29444`, overrides the destination to `127.0.0.1:19444`, and uses the existing Reality/VLESS tunnel credentials for Hetzner port `8765`.
- Allow TCP port `29444` through the local firewall.

### Hetzner

- Run a new `telemt-sch42.service` with a separate config and work directory.
- Listen only on `127.0.0.1:19444`; the port is not exposed publicly.
- Enable only TLS mode with `tls_domain = "vk.com"`.
- Enable masking and TLS emulation, with a dedicated TLS-front cache directory.
- Configure only `user_388535440` with the existing secret.
- Bind the local API to `127.0.0.1:9092`, whitelist loopback only, and use it only for isolated connection and octet counters.
- Set generated-link metadata to public host `87.247.157.120` and public port `443`.

## Link

The test link uses the standard telemt FakeTLS format with server `87.247.157.120`, port `443`, and a `secret` value built from the `ee` prefix, the user's existing 32-hex secret, and the hexadecimal encoding of `vk.com`.

The raw secret is read from the production telemt configuration during deployment and must not be copied into this design document, local files, or command output.

## Deployment Safety

- Create timestamped backups of every modified remote configuration.
- Validate the alternate telemt service and confirm `127.0.0.1:19444` and `127.0.0.1:9092` are listening before changing nftables.
- Validate the new sing-box config and confirm `0.0.0.0:29444` is listening before adding the public redirect.
- Validate the complete nftables file before applying it, and place the specific `87.247.157.120:443` rule before the generic port-`443` DNAT rule.
- Do not restart or reload the existing `telemt`, `sing-box-tunnel9443`, nginx, or VPN services.

## Verification

1. Confirm both new systemd units remain active and their expected ports are listening.
2. Confirm a TCP connection to `87.247.157.120:443` reaches `sing-box-mt-vk:29444`.
3. Open the generated link on the user's phone through MegaFon.
4. Confirm the dedicated telemt API reports active connections for `user_388535440` and an increasing octet counter.
5. Confirm the original `194.87.80.237:9443` endpoint and existing service listeners remain unchanged.

Success means Telegram becomes usable through MegaFon with the dedicated link while existing users continue to use the original endpoint without interruption.

## Rollback

Remove the destination-specific nftables redirect, stop and disable `sing-box-mt-vk.service` and `telemt-sch42.service`, then remove their isolated configuration files. No existing service or user secret needs to be changed.
