# Dedicated VK-SNI MTProxy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deploy an isolated MTProxy endpoint for `user_388535440` at `87.247.157.120:443` with FakeTLS SNI `vk.ru` without changing the existing MTProxy path.

**Architecture:** A destination-specific nftables rule on RuVDS redirects only `87.247.157.120:443` to a new sing-box process on port `29444`. That process reuses the existing Reality/VLESS transport to Hetzner and targets a new loopback-only telemt instance on `127.0.0.1:19444`.

**Tech Stack:** telemt 3.4.11, sing-box, systemd, nftables, UFW, SSH.

## Global Constraints

- Do not restart or modify the existing `telemt`, `sing-box-tunnel9443`, nginx, or VPN services.
- Do not print or store the raw user secret in local files, repository files, or command output.
- Use SNI `vk.ru`, public host `87.247.157.120`, and public port `443`.
- Keep the experimental endpoint limited to `user_388535440`.
- Back up every modified remote file before replacement.

---

### Task 1: Deploy the isolated telemt instance on Hetzner

**Files:**
- Create: Hetzner `/etc/telemt-sch42/telemt.toml`
- Create: Hetzner `/etc/systemd/system/telemt-sch42.service`
- Create: Hetzner `/opt/telemt-sch42/tlsfront/`

**Interfaces:**
- Consumes: the existing `user_388535440` secret from `/etc/telemt/telemt.toml`.
- Produces: MTProxy listener `127.0.0.1:19444` and local API `127.0.0.1:9092`.

- [ ] **Step 1: Create the alternate config without exposing the secret**

Create a config template containing TLS mode, `vk.ru`, loopback listeners, API port `9092`, masking, and TLS emulation. Copy it to Hetzner, append only the existing `user_388535440` line from the production config, set ownership `root:telemt`, and mode `0640`.

The template content is:

```toml
[general]
use_middle_proxy = false
log_level = "normal"

[general.modes]
classic = false
secure = false
tls = true

[general.links]
show = ["user_388535440"]
public_host = "87.247.157.120"
public_port = 443

[server]
port = 19444

[server.api]
enabled = true
listen = "127.0.0.1:9092"
whitelist = ["127.0.0.1/32", "::1/128"]
minimal_runtime_enabled = false

[[server.listeners]]
ip = "127.0.0.1"

[censorship]
tls_domain = "vk.ru"
mask = true
tls_emulation = true
tls_front_dir = "/opt/telemt-sch42/tlsfront"

[access.users]
```

After copying the template, append the user entry without printing it:

```bash
grep '^user_388535440 = "[0-9a-f]\{32\}"$' /etc/telemt/telemt.toml >> /etc/telemt-sch42/telemt.toml
test "$(grep -c '^user_388535440 = ' /etc/telemt-sch42/telemt.toml)" -eq 1
chown root:telemt /etc/telemt-sch42/telemt.toml
chmod 0640 /etc/telemt-sch42/telemt.toml
```

- [ ] **Step 2: Create and start the dedicated systemd unit**

Use `/bin/telemt /etc/telemt-sch42/telemt.toml`, `User=telemt`, `Group=telemt`, `WorkingDirectory=/opt/telemt-sch42`, `Restart=on-failure`, and `RestartSec=5`. Run `systemctl daemon-reload`, enable and start only `telemt-sch42.service`.

The unit content is:

```ini
[Unit]
Description=Telemt VK SNI for sch42
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=telemt
Group=telemt
WorkingDirectory=/opt/telemt-sch42
ExecStart=/bin/telemt /etc/telemt-sch42/telemt.toml
Restart=on-failure
RestartSec=5
LimitNOFILE=65536
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 3: Verify the isolated service**

Run:

```bash
systemctl is-active telemt-sch42
ss -ltnp 'sport = :19444 or sport = :9092'
curl -fsS http://127.0.0.1:9092/v1/health
```

Expected: service is `active`, both ports listen on loopback, and the health response has `"ok":true`.

---

### Task 2: Deploy the isolated sing-box tunnel on RuVDS

**Files:**
- Create: RuVDS `/etc/sing-box/mt-vk.json`
- Create: RuVDS `/etc/systemd/system/sing-box-mt-vk.service`

**Interfaces:**
- Consumes: the existing Reality/VLESS transport settings from `/etc/sing-box/tunnel9443.json`.
- Produces: local relay listener `0.0.0.0:29444` targeting Hetzner `127.0.0.1:19444`.

- [ ] **Step 1: Derive the alternate sing-box config**

Copy `/etc/sing-box/tunnel9443.json` to `/etc/sing-box/mt-vk.json`, then change only:

```text
tag: mt-in -> mt-vk-in
listen_port: 29443 -> 29444
override_port: 9443 -> 19444
```

Run `/usr/local/bin/sing-box check -c /etc/sing-box/mt-vk.json` and require exit code `0`.

Exact transformation commands:

```bash
cp -a /etc/sing-box/tunnel9443.json /etc/sing-box/mt-vk.json
sed -i 's/"tag": "mt-in"/"tag": "mt-vk-in"/; s/"listen_port": 29443/"listen_port": 29444/; s/"override_port": 9443/"override_port": 19444/' /etc/sing-box/mt-vk.json
/usr/local/bin/sing-box check -c /etc/sing-box/mt-vk.json
```

- [ ] **Step 2: Create and start the dedicated systemd unit**

Use `/usr/local/bin/sing-box run -c /etc/sing-box/mt-vk.json`, `Restart=on-failure`, and `RestartSec=3`. Run `systemctl daemon-reload`, enable and start only `sing-box-mt-vk.service`.

The unit content is:

```ini
[Unit]
Description=sing-box MTProxy VK SNI tunnel for sch42
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/sing-box run -c /etc/sing-box/mt-vk.json
Restart=on-failure
RestartSec=3
LimitNOFILE=65536
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 3: Verify the relay listener**

Run:

```bash
systemctl is-active sing-box-mt-vk
ss -ltnp 'sport = :29444'
```

Expected: service is `active` and sing-box listens on `0.0.0.0:29444`.

---

### Task 3: Publish the endpoint on `87.247.157.120:443`

**Files:**
- Modify: RuVDS `/etc/nftables.conf`
- Modify through UFW: RuVDS firewall rules for TCP `29444`

**Interfaces:**
- Consumes: working local relay `0.0.0.0:29444`.
- Produces: public endpoint `87.247.157.120:443` without changing other destination addresses.

- [ ] **Step 1: Allow the redirected local port**

Run `ufw allow 29444/tcp comment 'MTProxy VK SNI sch42'` and confirm the rule is present.

- [ ] **Step 2: Add a destination-specific persistent nftables rule**

Back up `/etc/nftables.conf`. Insert this rule before the generic `tcp dport 443 dnat` rule:

```nft
ip daddr 87.247.157.120 tcp dport 443 redirect to :29444 comment "MTProxy VK SNI sch42"
```

Validate with `nft -c -f /etc/nftables.conf`, then apply with `nft -f /etc/nftables.conf` only if validation succeeds.

- [ ] **Step 3: Verify rule order and existing listeners**

Run:

```bash
nft list chain ip relay prerouting
systemctl is-active sing-box-tunnel9443
ss -ltnp 'sport = :29443 or sport = :29444'
```

Expected: the destination-specific rule precedes generic port `443` DNAT, and both old and new tunnel listeners remain active.

---

### Task 4: Generate the link and verify the complete path

**Files:** none.

**Interfaces:**
- Consumes: telemt API `127.0.0.1:9092` and public endpoint `87.247.157.120:443`.
- Produces: one test link for `@sch_42`.

- [ ] **Step 1: Confirm public TCP reachability**

Connect to `87.247.157.120:443` and capture only the new relay port to confirm the connection reaches `29444`.

- [ ] **Step 2: Obtain the generated TLS link**

Read `/v1/users/user_388535440` from the alternate telemt API and extract its single TLS link. Do not print the raw 32-hex secret separately.

- [ ] **Step 3: Verify after the user connects**

Read only `current_connections`, `recent_unique_ips`, and `total_octets` from the alternate API. Expected: at least one connection and increasing octets.

- [ ] **Step 4: Confirm production remains unchanged**

Verify `telemt`, `sing-box-tunnel9443`, `194.87.80.237:9443`, and their listeners remain active. If any verification fails, remove the destination-specific nftables rule and stop the two alternate services.
