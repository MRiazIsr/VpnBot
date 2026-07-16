# Alternative MTProxy Smart SYN Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Install an isolated Smart SYN limiter for `87.247.157.120:443` on RuVDS so the affected Android/MegaFon client can retry MTProxy handshakes without changing the primary MTProxy or VPN.

**Architecture:** Add one dedicated nftables table on RuVDS in the input hook after the existing redirect to the dedicated telemt listener. Match the conntrack original destination `87.247.157.120:443` plus the translated port `29444`, meter new TCP SYN packets per IPv4 source at `54/minute burst 1`, and immediately reject excess SYNs. A oneshot systemd unit owns only this table, making persistence and rollback independent from telemt and the current firewall.

**Tech Stack:** nftables, systemd, OpenSSH, telemt 3.4.24

## Global Constraints

- Apply only to destination `87.247.157.120`, TCP port `443`.
- Do not edit telemt configuration, VPN configuration, Hetzner, bot output, or existing nftables tables.
- Preserve the current `client_mss` settings during this first test so only SYN handling changes.
- Save timestamped backups of `/etc/telemt/telemt.toml` and the complete nftables ruleset before applying the rule.
- Stop on any mismatch between the expected public redirect and the live ruleset.

---

### Task 1: Confirm the live packet path and capture rollback state

**Files:**
- Inspect: `/etc/telemt/telemt.toml` on RuVDS
- Inspect: `/etc/telemt-sch42/telemt.toml` on RuVDS
- Create: `/root/mtproxy-smart-syn-backup-20260716-191227/telemt.toml`
- Create: `/root/mtproxy-smart-syn-backup-20260716-191227/telemt-sch42.toml`
- Create: `/root/mtproxy-smart-syn-backup-20260716-191227/nftables.nft`

- [x] **Step 1: Inspect the live address, telemt listener, service state, and complete nftables ruleset**

Run over SSH with `~/.ssh/russian-vps`:

```bash
ip -4 address show
systemctl is-active telemt-sch42-direct
ss -lntp
awk '/^\[access.users\]$/ { print; print "[REDACTED]"; exit } { print }' /etc/telemt-sch42/telemt.toml
nft -a list ruleset
```

Expected: `87.247.157.120` is local, `telemt-sch42-direct` is active on `29444`, and an existing rule redirects public TCP `443` to that dedicated listener.

- [x] **Step 2: Save timestamped backups before mutation**

```bash
stamp=$(date +%Y%m%d-%H%M%S)
backup=/root/mtproxy-smart-syn-backup-$stamp
install -d -m 700 "$backup"
install -m 600 /etc/telemt/telemt.toml "$backup/telemt.toml"
install -m 600 /etc/telemt-sch42/telemt.toml "$backup/telemt-sch42.toml"
nft list ruleset > "$backup/nftables.nft"
printf '%s\n' "$backup"
```

Expected: one printed backup directory containing both telemt configs and the nftables ruleset.

### Task 2: Install the isolated Smart SYN rule

**Files:**
- Create: `/usr/local/sbin/mtproxy-smart-syn-alt`
- Create: `/etc/systemd/system/mtproxy-smart-syn-alt.service`

- [x] **Step 1: Build the nftables owner script locally and validate its syntax**

The script must:

1. delete only `table inet mtproxy_smart_syn_alt` if it already exists;
2. create a base chain in the input hook, where `reject` is supported by the server's nftables 0.9.3/kernel combination;
3. match only initial SYN packets whose conntrack original destination is `87.247.157.120:443` and whose translated destination port is `29444`;
4. meter packets per `ip saddr` at `54/minute burst 1 packets`, with a 60-second timeout;
5. count allowed packets and reject only over-limit matching SYNs with `icmp type host-unreachable`;
6. support `start` and `stop` actions for deterministic rollback.

Validate the generated ruleset with `nft --check` on RuVDS before activating it.

- [x] **Step 2: Install the owner script and systemd unit without touching existing firewall files**

Copy both files to RuVDS, install them with root ownership, then run:

```bash
systemctl daemon-reload
systemctl enable --now mtproxy-smart-syn-alt.service
```

Expected: the unit exits successfully and remains `active (exited)`.

- [x] **Step 3: Confirm the rule scope and packet path**

```bash
systemctl status --no-pager mtproxy-smart-syn-alt.service
nft -a list table inet mtproxy_smart_syn_alt
systemctl is-active telemt-sch42-direct
ss -lntp
```

Expected: only the alternative original destination and dedicated translated port appear in the new table; `telemt-sch42-direct` stays active and still listens on `29444`.

### Task 3: Verify reachability and prepare the user test

**Files:**
- Inspect: `/var/log` or journald entries for telemt

- [x] **Step 1: Check TCP reachability from outside RuVDS**

```bash
nc -vz -w 5 87.247.157.120 443
```

Expected: TCP connection succeeds.

- [x] **Step 2: Capture baseline counters and recent telemt state**

```bash
nft list table inet mtproxy_smart_syn_alt
journalctl -u telemt-sch42-direct --since '5 minutes ago' --no-pager
```

Expected: counters are visible and no service crash/restart occurred.

- [ ] **Step 3: Ask the affected user for one mobile-network connection attempt**

Use the existing alternative link. After the attempt, compare the allow/reject counters and telemt log timestamp to determine whether Smart SYN changed the handshake result.

### Task 4: Document exact rollback

**Files:**
- Remove on rollback: `/etc/systemd/system/mtproxy-smart-syn-alt.service`
- Remove on rollback: `/usr/local/sbin/mtproxy-smart-syn-alt`

- [x] **Step 1: Record the rollback command sequence**

```bash
systemctl disable --now mtproxy-smart-syn-alt.service
/usr/local/sbin/mtproxy-smart-syn-alt stop
rm -f /etc/systemd/system/mtproxy-smart-syn-alt.service
rm -f /usr/local/sbin/mtproxy-smart-syn-alt
systemctl daemon-reload
```

Expected: `nft list table inet mtproxy_smart_syn_alt` reports that the table does not exist, while `telemt-sch42-direct` and the original public redirect remain unchanged.
