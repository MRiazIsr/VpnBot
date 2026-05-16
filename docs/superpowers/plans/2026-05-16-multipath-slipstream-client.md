# Multipath Slipstream Client — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fork DNSTT.XYZ into an Android client that auto-discovers working DNS resolvers (validated by a real Slipstream handshake), runs slipstream-rust native multipath over all of them, lets the user add manual DNS that augments auto, defaults to fail-closed, and ships one-tap UX plus a diagnostics export.

**Architecture:** Approach A — all discovery/multipath orchestration lives in the app's Kotlin/Dart layer; `vendor/slipstream-rust` is NOT modified (native multipath via repeated `--resolver`). Fallback C (a minimal Rust `--probe` flag) is only introduced if the spawn-based probe proves unreliable (Task 8 gate). Server (slipstream-rust on Hetzner, `e.moskva.live`, SOCKS target) is unchanged.

**Tech Stack:** Flutter/Dart (UI + orchestrator), Kotlin/Android (enumeration, gate, prober, launcher, VpnService), JUnit + MockK + Robolectric (Kotlin tests), flutter_test + mocktail (Dart tests), Rust toolchain + cargo-ndk (only to build the bundled `libslipstream_client.so`).

**Spec:** `docs/superpowers/specs/2026-05-16-multipath-slipstream-client-design.md`

**Reference facts already established (from PoC, do not re-derive):**
- Upstream repo: `https://github.com/dnstt-xyz/dnstt_xyz_app` (Flutter; Android package `xyz.dnstt.app`; submodule `vendor/slipstream-rust` → `github.com/Mygod/slipstream-rust`).
- Known Android classes (from logcat): `DnsttVpnService`, `SlipstreamBridge`, `SlipstreamTest`. `SlipstreamBridge` spawns the bundled client; observed invocation:
  `libslipstream_client.so --tcp-listen-port <P> --tcp-listen-host 127.0.0.1 --domain <D> --resolver <IP>:53 --congestion-control <cc> --keep-alive-interval 400 --gso`
- slipstream-client (rust) flags: `-l/--tcp-listen-port`, `--tcp-listen-host`, `-r/--resolver` (repeatable → multipath), `-c/--congestion-control {bbr,dcubic}`, `-g/--gso`, `-d/--domain`, `-t/--keep-alive-interval`.
- Slipstream lib is ONLY in the `arm64-v8a` APK split (armeabi-v7a omits it).
- Server: `e.moskva.live`, slipstream-rust slipstream-server on Hetzner `49.13.201.110`, target SOCKS5 `127.0.0.1:1080`. Server build commit must equal client submodule commit (protocol pre-1.0).
- MIUI: `adb install` blocked (`INSTALL_FAILED_USER_RESTRICTED`) → install by tapping pushed APK.
- A known-good test resolver for handshake baselines: Yandex `77.88.8.8`.
- **EMPIRICALLY VERIFIED (Task 2, 2026-05-16) — do not change without re-verifying:** the bundled `libslipstream_client.so` is an ELF aarch64 PIE executable (runs as a CLI despite the `.so` name). At the pinned commit `b103aa66`, the handshake-success log signal is the line **`Connection ready`** (followed by an `acceptor: initial_max_streams_bidir_remote=...` line). It is NOT the literal `Connection confirmed` (that was the old EndPositive C binary's wording). All success predicates in this plan use `Connection ready` accordingly. Task 2 confirmed this exact rebuilt binary tunnels end-to-end through the live server (egress `49.13.201.110`).

---

## Execution adjustments from T3 recon (authoritative: fork `INTEGRATION-NOTES.md`)

- **Android test infra is ABSENT** (`android/app/src/test`, `src/androidTest` do not exist; only Flutter `test/`). Task 4 must FIRST create the Kotlin unit-test source set + add deps (JUnit4, MockK, `kotlinx-coroutines-test`, Robolectric) to `android/app/build.gradle` and verify a trivial test runs, before its TDD steps.
- **A MethodChannel already exists**: `xyz.dnstt.app/vpn` registered in `MainActivity.kt:~83` `configureFlutterEngine`. Task 9 ADDS a second channel `xyz.dnstt.app/multipath` alongside it (do NOT re-register or replace the existing one).
- **Two slipstream spawn paths** exist: `DnsttVpnService.kt` (VPN mode, calls `SlipstreamBridge.startClient` ~L334-343) and `SlipstreamProxyService.kt` (proxy mode). Resolver is single-valued through every hop. Centralize multipath arg construction in `MultipathLauncher` (Task 8) and route BOTH spawn paths through it (Task 11 must update both, not just the VPN path).
- Key anchors (from `INTEGRATION-NOTES.md`): arg builder `SlipstreamBridge.kt:59-96 startClient` (mutable `args`, single `--resolver` ~L87, extra args appended near `--gso` ~L92-94); DNS storage SharedPreferences keys `dns_servers`/`active_dns` in `lib/services/storage_service.dart`, model `lib/models/dns_server.dart`, selection `lib/providers/app_state.dart`; VpnService exclusion `DnsttVpnService.kt:426 addDisallowedApplication`, establish L429, teardown `disconnect()` L754-804; connect UI `lib/screens/home_screen.dart` + `lib/services/vpn_service.dart:490-532`; settings `lib/screens/config_management_screen.dart:884-937`, model `lib/models/dnstt_config.dart:45-49`; entrypoint `MainActivity.kt:73`. Dart package name: `dnstt_xyz_app`. `.so` packaged via `android/app/src/main/jniLibs/<abi>/libslipstream_client.so`.
- Upstream build script checks a stale name `libslipstream.so` while runtime uses `libslipstream_client.so` — Task 14 must assert the `libslipstream_client.so` filename specifically.

## STATUS: IMPLEMENTATION COMPLETE (2026-05-17)

All 14 tasks done via subagent-driven TDD in fork `~/src/dnstt_xyz_app` branch
`feat/multipath` (25 commits ahead of main, **local only — NOT pushed**).
Final whole-impl review: **SHIP-WITH-NOTES** (all 6 spec points implemented;
additive; legacy untouched; submodule pinned `b103aa6`). Signed arm64-v8a
release APK builds, contains `libslipstream_client.so`, abiFilter enforces
arm64-only (APK 32→13.6MB). Full Kotlin (15) + Flutter (9) unit suites GREEN.
Real-server integration `android/integration/probe_real.sh` passes
(egress 49.13.201.110). GUI connect E2E remains a manual field step.

**PUSH BLOCKED — opsec (user decision required):** committed code/tests/docs
(`MainActivity.kt`, `probe_real.sh`, `DISTRIBUTION.md`, `INTEGRATION-NOTES.md`,
tests) embed the production server domain `e.moskva.live` and Hetzner IP
`49.13.201.110`. The fork is likely public → pushing would publicly expose
the censorship-circumvention server (self-defeating + a real leak). Auto-mode
hard-blocked the push (correctly). Before any push: either (a) parametrize
domain/IP (remove hardcoding, scrub history) + push to a PRIVATE fork, or
(b) keep local-only. Non-blocking review notes recorded in fork
`INTEGRATION-NOTES.md` "Final review notes".

## Hardening backlog (non-blocking, from reviews)

- T4 `SlipstreamProcess.runUntil`: in-loop timeout only fires between emitted lines; T7 MUST add a hard outer watchdog/kill so the probe times out even if the process emits nothing (mitigated in practice — the client emits chatty startup lines immediately, per T2).
- T6 `DnsWire.buildAQuery`: no label≤63/name≤255 guard (not triggered by current short whitelist/bogus names). T6 `parseFirstA`: reserved label-types 0x40/0x80 mis-advance (illegal on wire, bounds-safe→null); no response-txid match (acceptable — gate is a pre-filter; tunnel security comes from the QUIC/TLS handshake, not the gate).

- T7 `HandshakeProber`/`SlipstreamProcess`: theoretical-only null-before-`onStart` race (child spawned, hard-timeout fires in the ~sub-µs gap before the live source is stored → unkilled). Not realistically reachable (slack ≥1500ms vs adjacent non-blocking statements). Optional defensive hardening: spawn+`onStart` in same try so it can't be skipped, or have the timeout path also briefly `future.get` to drain.

- Upstream PRE-EXISTING failure (not introduced by us): `test/widget_test.dart` fails (`Couldn't find constructor 'MyApp'` — stale default Flutter scaffold test). T14 (full `flutter test`) must delete/fix this stale upstream test so the suite is green; it is NOT a regression from our work.
- `pubspec.lock` is gitignored upstream — do not force-add it.

## File Structure (target, in the fork repo)

New code under one focused package, one responsibility per file:

- `android/app/src/main/kotlin/xyz/dnstt/app/multipath/ResolverEnumerator.kt` — gather candidate resolvers.
- `.../multipath/EligibilityGate.kt` — cheap whitelisted-domain + anti-hijack check.
- `.../multipath/HandshakeProber.kt` — real mini-handshake probe via bundled client.
- `.../multipath/MultipathLauncher.kt` — build args + launch the long-lived client with all working resolvers.
- `.../multipath/SlipstreamProcess.kt` — thin testable process-runner abstraction (used by Prober + Launcher).
- `.../multipath/MultipathChannel.kt` — MethodChannel handlers exposing the above to Dart.
- `.../multipath/Diagnostics.kt` — collect/format the shareable diagnostics blob.
- `lib/multipath/discovery_orchestrator.dart` — Dart pipeline state machine + cache + retriggers.
- `lib/multipath/multipath_models.dart` — Dart data classes mirroring Kotlin DTOs.
- `lib/multipath/multipath_platform.dart` — Dart side of the MethodChannel.
- UI: extend existing connect screen + an Advanced screen (exact files located in Task 3).
- `INTEGRATION-NOTES.md` (fork root) — produced by Task 3, authoritative for integration-point paths.

Integration edits (existing files, located in Task 3): `SlipstreamBridge`/`DnsttVpnService` (feed multipath port; ensure app-uid exclusion covers spawned process; fail-closed lifecycle), the config/DNS-list storage, the connect-screen widget.

---

## Task 1: Fork, clone, pin submodule, build baseline arm64 APK

**Files:**
- Create: local clone of the fork (working repo, separate from VpnBot).
- Modify: `.gitmodules` / submodule pin (commit ref).

- [ ] **Step 1: Fork upstream and clone with submodules**

```bash
gh repo fork dnstt-xyz/dnstt_xyz_app --clone=false
git clone --recursive git@github.com:$(gh api user --jq .login)/dnstt_xyz_app.git
cd dnstt_xyz_app
git submodule update --init --recursive
```

- [ ] **Step 2: Determine the server's slipstream-rust commit and pin the submodule to it**

The server binary on Hetzner was built from `Mygod/slipstream-rust` (clone on RuVDS `/root/slipstream-rust`). Get the exact commit:

```bash
ssh -i ~/.ssh/russian-vps root@194.87.80.237 'cd /root/slipstream-rust && git rev-parse HEAD'
```

Pin the fork's submodule to that commit:

```bash
cd vendor/slipstream-rust
git fetch origin
git checkout <COMMIT_FROM_PREVIOUS_STEP>
cd ../..
git add vendor/slipstream-rust
git commit -m "chore: pin slipstream-rust to server-matching commit <SHORT_SHA>"
```

- [ ] **Step 3: Install toolchain and build the arm64-v8a APK (with slipstream lib)**

```bash
# Flutter + Android SDK/NDK assumed installed; install Rust + cargo-ndk:
curl -sSf https://sh.rustup.rs | sh -s -- -y
cargo install cargo-ndk
flutter pub get
./scripts/build_slipstream_desktop.sh || true   # if present; the Android build script is authoritative:
flutter build apk --release --target-platform android-arm64
```

- [ ] **Step 4: Verify the built APK contains the slipstream library**

Run:
```bash
unzip -l build/app/outputs/flutter-apk/app-release.apk | grep -i libslipstream_client.so
```
Expected: a line `lib/arm64-v8a/libslipstream_client.so`. If absent, STOP — the Rust/cargo-ndk build did not run; fix before continuing.

- [ ] **Step 5: Commit the pinned baseline**

```bash
git add -A
git commit -m "chore: baseline build, slipstream-rust pinned, arm64 APK verified"
```

---

## Task 2: Baseline smoke test against the real server (no code change)

**Files:** none (verification only).

- [ ] **Step 1: Push the baseline APK to the test phone and install by tap**

```bash
adb push build/app/outputs/flutter-apk/app-release.apk /sdcard/Download/baseline.apk
```
Manually tap-install on device (MIUI blocks `adb install`).

- [ ] **Step 2: Configure upstream app for our server and connect**

In-app: Protocol=Slipstream, Domain=`e.moskva.live`, DNS server=`77.88.8.8`, mode=VPN. Connect.

- [ ] **Step 3: Verify egress through the tunnel**

On the phone open `https://api.ipify.org` (or 2ip.ru). Expected: `49.13.201.110` (Hetzner). This proves toolchain + submodule pin are protocol-compatible with the live server BEFORE writing any new code. If it fails, the submodule commit is wrong — return to Task 1 Step 2.

---

## Task 3: Codebase exploration → INTEGRATION-NOTES.md

**Files:**
- Create: `INTEGRATION-NOTES.md` (fork root).

- [ ] **Step 1: Locate and record integration points**

Find and record exact paths + line ranges + signatures for:
1. The class/method that builds the slipstream-client argument list (search `--resolver` and `tcp-listen-port`): `grep -rn "tcp-listen-port\|--resolver\|SlipstreamBridge" android/ lib/`
2. Where the single DNS server is read from config/UI for Slipstream: `grep -rn "resolver\|dnsServer\|DnsServer" lib/ android/`
3. VpnService app-uid exclusion: `grep -rn "addDisallowedApplication\|VpnService\|DnsttVpnService" android/`
4. The connect-screen widget and the Advanced/Settings screen: `grep -rn "Connect\|VPN Mode\|DNS Servers" lib/`
5. Config/state persistence for DNS list & protocol settings.

- [ ] **Step 2: Write INTEGRATION-NOTES.md**

Document, for each of the 5 points: exact file path, class/function name, signature, and the precise insertion/replacement anchor (the surrounding code snippet) the later tasks will edit. No prose-only entries — include the actual current code snippet at each anchor.

- [ ] **Step 3: Commit**

```bash
git add INTEGRATION-NOTES.md
git commit -m "docs: integration notes for multipath fork"
```

---

## Task 4: SlipstreamProcess (testable process-runner abstraction)

**Files:**
- Create: `android/app/src/main/kotlin/xyz/dnstt/app/multipath/SlipstreamProcess.kt`
- Test: `android/app/src/test/kotlin/xyz/dnstt/app/multipath/SlipstreamProcessTest.kt`

- [ ] **Step 1: Write the failing test**

```kotlin
package xyz.dnstt.app.multipath
import io.mockk.*
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test

class SlipstreamProcessTest {
    @Test fun emits_lines_until_predicate_then_stops() = runTest {
        val fake = FakeLineSource(listOf("Starting", "Connection ready.", "more"))
        val runner = SlipstreamProcess(libPath = "/x/libslipstream_client.so") { _ -> fake }
        val ok = runner.runUntil(
            args = listOf("--domain", "e.moskva.live"),
            timeoutMs = 1000,
            predicate = { it.contains("Connection ready") }
        )
        assertTrue(ok.matched)
        assertTrue(fake.killed)
    }
    @Test fun times_out_without_match() = runTest {
        val fake = FakeLineSource(listOf("Starting", "no-match"))
        val runner = SlipstreamProcess(libPath = "/x/lib.so") { _ -> fake }
        val ok = runner.runUntil(listOf("-d","x"), timeoutMs = 50) { it.contains("confirmed") }
        assertFalse(ok.matched)
        assertTrue(fake.killed)
    }
}

class FakeLineSource(private val lines: List<String>) : ProcLineSource {
    var killed = false
    override fun lines() = lines.asSequence()
    override fun kill() { killed = true }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./gradlew :app:testDebugUnitTest --tests "*SlipstreamProcessTest*"`
Expected: FAIL — `SlipstreamProcess`/`ProcLineSource` unresolved.

- [ ] **Step 3: Write minimal implementation**

```kotlin
package xyz.dnstt.app.multipath

interface ProcLineSource { fun lines(): Sequence<String>; fun kill() }

data class ProbeOutcome(val matched: Boolean, val elapsedMs: Long)

class SlipstreamProcess(
    private val libPath: String,
    private val sourceFactory: (List<String>) -> ProcLineSource = ::realSource
) {
    fun runUntil(args: List<String>, timeoutMs: Long, predicate: (String) -> Boolean): ProbeOutcome {
        val start = System.currentTimeMillis()
        val src = sourceFactory(listOf(libPath) + args)
        try {
            for (line in src.lines()) {
                if (predicate(line)) return ProbeOutcome(true, System.currentTimeMillis() - start)
                if (System.currentTimeMillis() - start > timeoutMs) break
            }
            return ProbeOutcome(false, System.currentTimeMillis() - start)
        } finally { src.kill() }
    }
    companion object {
        fun realSource(cmd: List<String>): ProcLineSource = object : ProcLineSource {
            private val p = ProcessBuilder(cmd).redirectErrorStream(true).start()
            override fun lines() = p.inputStream.bufferedReader().lineSequence()
            override fun kill() { p.destroyForcibly() }
        }
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./gradlew :app:testDebugUnitTest --tests "*SlipstreamProcessTest*"`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add android/app/src/main/kotlin/xyz/dnstt/app/multipath/SlipstreamProcess.kt android/app/src/test/kotlin/xyz/dnstt/app/multipath/SlipstreamProcessTest.kt
git commit -m "feat(multipath): testable slipstream process runner"
```

---

## Task 5: ResolverEnumerator

**Files:**
- Create: `.../multipath/ResolverEnumerator.kt`
- Test: `.../test/kotlin/xyz/dnstt/app/multipath/ResolverEnumeratorTest.kt`

- [ ] **Step 1: Write the failing test**

```kotlin
package xyz.dnstt.app.multipath
import org.junit.Assert.*
import org.junit.Test
import java.net.InetAddress

class ResolverEnumeratorTest {
    @Test fun dedups_tags_and_formats_v6() {
        val sys = listOf(InetAddress.getByName("77.88.8.8"),
                         InetAddress.getByName("2a02:6b8::feed:0ff"))
        val gw = InetAddress.getByName("192.168.1.1")
        val manual = listOf("77.88.8.8", "1.1.1.1")
        val out = ResolverEnumerator.build(systemDns = sys, gateway = gw, manual = manual)
        assertEquals("[2a02:6b8::feed:ff]:53",
            out.first { it.ip.contains(":") }.endpoint())
        // 77.88.8.8 appears once though present in system+manual
        assertEquals(1, out.count { it.ip == "77.88.8.8" })
        assertTrue(out.any { it.source == Source.GATEWAY && it.ip == "192.168.1.1" })
        assertTrue(out.any { it.source == Source.MANUAL && it.ip == "1.1.1.1" })
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./gradlew :app:testDebugUnitTest --tests "*ResolverEnumeratorTest*"`
Expected: FAIL — unresolved `ResolverEnumerator`/`Source`.

- [ ] **Step 3: Write minimal implementation**

```kotlin
package xyz.dnstt.app.multipath
import java.net.InetAddress

enum class Source { SYSTEM, GATEWAY, MANUAL }

data class Candidate(val ip: String, val port: Int = 53, val source: Source) {
    fun endpoint(): String = if (ip.contains(":")) "[$ip]:$port" else "$ip:$port"
}

object ResolverEnumerator {
    fun build(systemDns: List<InetAddress>, gateway: InetAddress?, manual: List<String>): List<Candidate> {
        val seen = LinkedHashMap<String, Candidate>()
        fun add(ip: String, s: Source) { if (ip.isNotBlank()) seen.putIfAbsent(ip, Candidate(ip, 53, s)) }
        systemDns.forEach { add(it.hostAddress!!.substringBefore('%'), Source.SYSTEM) }
        gateway?.let { add(it.hostAddress!!, Source.GATEWAY) }
        manual.forEach { add(it.trim(), Source.MANUAL) }
        return seen.values.toList()
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./gradlew :app:testDebugUnitTest --tests "*ResolverEnumeratorTest*"`
Expected: PASS.

- [ ] **Step 5: Add the Android runtime collector (system/DHCP/gateway)**

Append to `ResolverEnumerator.kt`:

```kotlin
// Runtime collection: must be called for the UNDERLYING network, BEFORE VpnService is up.
fun collectSystem(cm: android.net.ConnectivityManager): Pair<List<InetAddress>, InetAddress?> {
    val net = cm.activeNetwork ?: return emptyList<InetAddress>() to null
    val lp = cm.getLinkProperties(net) ?: return emptyList<InetAddress>() to null
    val gw = lp.routes.firstOrNull { it.isDefaultRoute }?.gateway
    return lp.dnsServers to gw
}
```

- [ ] **Step 6: Commit**

```bash
git add android/app/src/main/kotlin/xyz/dnstt/app/multipath/ResolverEnumerator.kt android/app/src/test/kotlin/xyz/dnstt/app/multipath/ResolverEnumeratorTest.kt
git commit -m "feat(multipath): resolver enumeration (system/dhcp/gateway/manual)"
```

---

## Task 6: EligibilityGate

**Files:**
- Create: `.../multipath/EligibilityGate.kt`
- Test: `.../test/kotlin/xyz/dnstt/app/multipath/EligibilityGateTest.kt`

- [ ] **Step 1: Write the failing test**

```kotlin
package xyz.dnstt.app.multipath
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test

class EligibilityGateTest {
    private fun gate(resolver: FakeDns) = EligibilityGate(resolver)

    @Test fun passes_when_whitelisted_resolve_and_no_hijack() = runTest {
        val dns = FakeDns(
            answers = mapOf("yandex.ru" to "5.255.255.242", "vk.com" to "87.240.190.78",
                            "ozon.ru" to "172.67.1.1", "gosuslugi.ru" to "212.5.1.1"),
            nxForUnknown = true)
        val r = gate(dns).check(Candidate("77.88.8.8", 53, Source.SYSTEM))
        assertTrue(r.passed); assertFalse(r.hijacked)
    }
    @Test fun flags_hijack_when_unknown_returns_ip() = runTest {
        val dns = FakeDns(answers = mapOf("yandex.ru" to "5.255.255.242"), nxForUnknown = false)
        val r = gate(dns).check(Candidate("10.0.0.1", 53, Source.GATEWAY))
        assertTrue(r.hijacked)
    }
    @Test fun fails_when_nothing_resolves() = runTest {
        val r = gate(FakeDns(emptyMap(), nxForUnknown = true)).check(Candidate("8.8.8.8",53,Source.MANUAL))
        assertFalse(r.passed)
    }
}

class FakeDns(private val answers: Map<String,String>, private val nxForUnknown: Boolean) : DnsResolver {
    override suspend fun resolveA(host: String, server: Candidate, timeoutMs: Long): String? =
        answers[host] ?: if (nxForUnknown) null else "1.2.3.4"
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./gradlew :app:testDebugUnitTest --tests "*EligibilityGateTest*"`
Expected: FAIL — unresolved `EligibilityGate`/`DnsResolver`.

- [ ] **Step 3: Write minimal implementation**

```kotlin
package xyz.dnstt.app.multipath

interface DnsResolver {
    suspend fun resolveA(host: String, server: Candidate, timeoutMs: Long): String?
}

data class GateResult(val candidate: Candidate, val passed: Boolean, val hijacked: Boolean, val reason: String)

class EligibilityGate(private val dns: DnsResolver) {
    private val whitelist = listOf("yandex.ru", "vk.com", "ozon.ru", "gosuslugi.ru")

    suspend fun check(c: Candidate, timeoutMs: Long = 2000): GateResult {
        val resolved = whitelist.count { dns.resolveA(it, c, timeoutMs) != null }
        val bogus = "nx-${System.nanoTime()}.invalid-probe.test"
        val hijacked = dns.resolveA(bogus, c, timeoutMs) != null
        val passed = resolved >= (whitelist.size + 1) / 2
        return GateResult(c, passed, hijacked,
            if (passed) "resolved $resolved/${whitelist.size}" else "only $resolved/${whitelist.size}")
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./gradlew :app:testDebugUnitTest --tests "*EligibilityGateTest*"`
Expected: PASS (3 tests).

- [ ] **Step 5: Implement the real `DnsResolver` (UDP/53, protected socket)**

Append `UdpDnsResolver.kt` in the same package implementing `DnsResolver` with a raw A-query over `DatagramSocket`, the socket passed to `VpnService.protect()` via an injected `(DatagramSocket)->Unit` protector (so it bypasses the tunnel). Keep query building minimal (single A question, parse first A answer). Unit-test the wire encoder separately:

```kotlin
// EligibilityGateTest addition
@Test fun encodes_qname() {
    val q = DnsWire.buildAQuery("a.bc")
    // 0x01 'a' 0x02 'b' 'c' 0x00  type=1 class=1 present
    assertTrue(q.toList().windowed(4).any { it == listOf<Byte>(0,1,0,1) })
}
```
Implement `DnsWire.buildAQuery`/`parseFirstA` to satisfy it (RFC1035 minimal). Run the same gradle test command; expected PASS.

- [ ] **Step 6: Commit**

```bash
git add android/app/src/main/kotlin/xyz/dnstt/app/multipath/EligibilityGate.kt android/app/src/main/kotlin/xyz/dnstt/app/multipath/UdpDnsResolver.kt android/app/src/test/kotlin/xyz/dnstt/app/multipath/EligibilityGateTest.kt
git commit -m "feat(multipath): eligibility gate (whitelist resolve + anti-hijack)"
```

---

## Task 7: HandshakeProber

**Files:**
- Create: `.../multipath/HandshakeProber.kt`
- Test: `.../test/kotlin/xyz/dnstt/app/multipath/HandshakeProberTest.kt`

- [ ] **Step 1: Write the failing test**

```kotlin
package xyz.dnstt.app.multipath
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test

class HandshakeProberTest {
    @Test fun success_on_confirmed_with_rtt() = runTest {
        val proc = SlipstreamProcess("lib.so") { FakeLineSource(listOf("Starting","Connection ready.")) }
        val r = HandshakeProber(proc, libPath="lib.so", domain="e.moskva.live")
                  .probe(Candidate("77.88.8.8",53,Source.SYSTEM), ephemeralPort=39001, timeoutMs=2000)
        assertTrue(r.working); assertTrue(r.rttMs >= 0)
    }
    @Test fun failure_on_timeout() = runTest {
        val proc = SlipstreamProcess("lib.so") { FakeLineSource(listOf("Starting","noise")) }
        val r = HandshakeProber(proc,"lib.so","e.moskva.live")
                  .probe(Candidate("10.0.0.1",53,Source.GATEWAY), 39002, 30)
        assertFalse(r.working)
    }
    @Test fun builds_expected_args() {
        val args = HandshakeProber.args("e.moskva.live", Candidate("[2a02::1]".trim(),53,Source.SYSTEM).copy(ip="2a02::1"), 7777)
        assertTrue(args.containsAll(listOf("--domain","e.moskva.live","--resolver","[2a02::1]:53",
            "--tcp-listen-port","7777","--tcp-listen-host","127.0.0.1")))
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./gradlew :app:testDebugUnitTest --tests "*HandshakeProberTest*"`
Expected: FAIL — unresolved `HandshakeProber`.

- [ ] **Step 3: Write minimal implementation**

```kotlin
package xyz.dnstt.app.multipath

data class ProbeResult(val candidate: Candidate, val working: Boolean, val rttMs: Long)

class HandshakeProber(
    private val proc: SlipstreamProcess,
    private val libPath: String,
    private val domain: String
) {
    fun probe(c: Candidate, ephemeralPort: Int, timeoutMs: Long): ProbeResult {
        val o = proc.runUntil(args(domain, c, ephemeralPort), timeoutMs) {
            it.contains("Connection ready")
        }
        return ProbeResult(c, o.matched, o.elapsedMs)
    }
    companion object {
        fun args(domain: String, c: Candidate, port: Int) = listOf(
            "--tcp-listen-port", port.toString(),
            "--tcp-listen-host", "127.0.0.1",
            "--domain", domain,
            "--resolver", c.endpoint()
        )
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./gradlew :app:testDebugUnitTest --tests "*HandshakeProberTest*"`
Expected: PASS (3 tests).

- [ ] **Step 5: Add an integration test against the REAL server (not mocked)**

Create `androidTest` (instrumented) `HandshakeProberRealTest` that runs the actual bundled lib against `e.moskva.live` via `77.88.8.8`, asserts `working == true` within 15s. This requires a device/emulator with network.

Run: `./gradlew :app:connectedDebugAndroidTest --tests "*HandshakeProberRealTest*"`
Expected: PASS (real "Connection ready"). If FAIL while Task 2 baseline passed → the spawn-probe is unreliable → escalate to fallback C: add a minimal `--probe` flag upstream-style to `vendor/slipstream-rust` (handshake then exit non-zero/zero), rebuild lib, switch `HandshakeProber` to use it. Document the escalation decision in `INTEGRATION-NOTES.md`.

- [ ] **Step 6: Commit**

```bash
git add android/app/src/main/kotlin/xyz/dnstt/app/multipath/HandshakeProber.kt android/app/src/test/kotlin/xyz/dnstt/app/multipath/HandshakeProberTest.kt android/app/src/androidTest/kotlin/xyz/dnstt/app/multipath/HandshakeProberRealTest.kt
git commit -m "feat(multipath): real-handshake prober (+ real-server integration test)"
```

---

## Task 8: MultipathLauncher

**Files:**
- Create: `.../multipath/MultipathLauncher.kt`
- Test: `.../test/kotlin/xyz/dnstt/app/multipath/MultipathLauncherTest.kt`

- [ ] **Step 1: Write the failing test**

```kotlin
package xyz.dnstt.app.multipath
import org.junit.Assert.*
import org.junit.Test

class MultipathLauncherTest {
    @Test fun repeats_resolver_for_each_working_and_sets_flags() {
        val working = listOf(Candidate("77.88.8.8",53,Source.SYSTEM),
                             Candidate("2a02::1",53,Source.MANUAL))
        val args = MultipathLauncher.buildArgs(
            domain="e.moskva.live", listenPort=1080, working=working,
            congestion="bbr", gso=true, keepAlive=400)
        assertEquals(2, args.windowed(2).count { it[0]=="--resolver" })
        assertTrue(args.containsAll(listOf("--resolver","77.88.8.8:53","--resolver","[2a02::1]:53",
            "--congestion-control","bbr","--gso","--tcp-listen-port","1080",
            "--tcp-listen-host","127.0.0.1","--domain","e.moskva.live",
            "--keep-alive-interval","400")))
    }
    @Test fun throws_when_no_working() {
        try { MultipathLauncher.buildArgs("d",1080, emptyList(),"bbr",true,400); fail() }
        catch (e: IllegalArgumentException) { assertTrue(true) }
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./gradlew :app:testDebugUnitTest --tests "*MultipathLauncherTest*"`
Expected: FAIL — unresolved `MultipathLauncher`.

- [ ] **Step 3: Write minimal implementation**

```kotlin
package xyz.dnstt.app.multipath

class MultipathLauncher(private val libPath: String) {
    private var handle: Process? = null

    fun start(domain: String, listenPort: Int, working: List<Candidate>,
              congestion: String, gso: Boolean, keepAlive: Int) {
        val cmd = listOf(libPath) + buildArgs(domain, listenPort, working, congestion, gso, keepAlive)
        handle = ProcessBuilder(cmd).redirectErrorStream(true).start()
    }
    fun stop() { handle?.destroyForcibly(); handle = null }

    companion object {
        fun buildArgs(domain: String, listenPort: Int, working: List<Candidate>,
                       congestion: String, gso: Boolean, keepAlive: Int): List<String> {
            require(working.isNotEmpty()) { "no working resolvers" }
            val a = mutableListOf(
                "--tcp-listen-port", listenPort.toString(),
                "--tcp-listen-host", "127.0.0.1",
                "--domain", domain,
                "--congestion-control", congestion,
                "--keep-alive-interval", keepAlive.toString())
            if (gso) a += "--gso"
            working.forEach { a += listOf("--resolver", it.endpoint()) }
            return a
        }
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `./gradlew :app:testDebugUnitTest --tests "*MultipathLauncherTest*"`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add android/app/src/main/kotlin/xyz/dnstt/app/multipath/MultipathLauncher.kt android/app/src/test/kotlin/xyz/dnstt/app/multipath/MultipathLauncherTest.kt
git commit -m "feat(multipath): launcher with native multi-resolver args"
```

---

## Task 9: MultipathChannel (Kotlin↔Dart MethodChannel)

**Files:**
- Create: `.../multipath/MultipathChannel.kt`
- Modify: app's `MainActivity`/engine config (path from INTEGRATION-NOTES.md) to register the channel.
- Test: `.../test/kotlin/xyz/dnstt/app/multipath/MultipathChannelTest.kt`

- [ ] **Step 1: Write the failing test** — assert `MultipathChannel.handle("enumerate", args)` returns a JSON list of candidates given injected fakes (enumerator/gate/prober). (Mirror the structure of Task 6/7 tests with MockK fakes; full test code: assert the returned `List<Map<String,Any>>` has keys `ip,port,source,passed,working,rttMs`.)

```kotlin
package xyz.dnstt.app.multipath
import org.junit.Assert.*
import org.junit.Test
class MultipathChannelTest {
    @Test fun discover_returns_serialized_results() {
        val ch = MultipathChannel(
            enumerate = { listOf(Candidate("77.88.8.8",53,Source.SYSTEM)) },
            gate = { GateResult(it, true, false, "ok") },
            probe = { ProbeResult(it, true, 120) })
        val out = ch.discover(manual = listOf("1.1.1.1"))
        assertTrue(out.any { it["ip"]=="77.88.8.8" && it["working"]==true })
    }
}
```

- [ ] **Step 2: Run test, verify FAIL** — `./gradlew :app:testDebugUnitTest --tests "*MultipathChannelTest*"` → unresolved.

- [ ] **Step 3: Implement**

```kotlin
package xyz.dnstt.app.multipath

class MultipathChannel(
    private val enumerate: () -> List<Candidate>,
    private val gate: suspend (Candidate) -> GateResult,
    private val probe: (Candidate) -> ProbeResult
) {
    fun discover(manual: List<String>): List<Map<String, Any>> {
        val cands = enumerate()  // already includes manual via ResolverEnumerator wiring
        val results = ArrayList<Map<String, Any>>()
        var port = 39000
        for (c in cands) {
            val g = kotlinx.coroutines.runBlocking { gate(c) }
            if (!g.passed) { results += row(c, g, null); continue }
            val p = probe(c.copy()).let { it } // ephemeral handled inside prober wiring
            results += row(c, g, p)
        }
        return results
    }
    private fun row(c: Candidate, g: GateResult, p: ProbeResult?) = mapOf(
        "ip" to c.ip, "port" to c.port, "source" to c.source.name,
        "passed" to g.passed, "hijacked" to g.hijacked,
        "working" to (p?.working ?: false), "rttMs" to (p?.rttMs ?: -1L))
}
```

- [ ] **Step 4: Run test, verify PASS.**

- [ ] **Step 5: Register the MethodChannel** in the Flutter engine entrypoint (exact file from INTEGRATION-NOTES.md). Add a `MethodChannel("xyz.dnstt.app/multipath")` with methods `discover`, `start`, `stop`, `diagnostics`, wiring real `ResolverEnumerator.collectSystem`, `UdpDnsResolver` (with `VpnService.protect`), `HandshakeProber`, `MultipathLauncher`. Manual rebuild + `flutter run` smoke that `discover` returns data on device.

- [ ] **Step 6: Commit**

```bash
git add android/app/src/main/kotlin/xyz/dnstt/app/multipath/MultipathChannel.kt android/app/src/test/kotlin/xyz/dnstt/app/multipath/MultipathChannelTest.kt <engine-entrypoint-file>
git commit -m "feat(multipath): method channel + engine wiring"
```

---

## Task 10: Dart models + platform + DiscoveryOrchestrator

**Files:**
- Create: `lib/multipath/multipath_models.dart`, `lib/multipath/multipath_platform.dart`, `lib/multipath/discovery_orchestrator.dart`
- Test: `test/multipath/discovery_orchestrator_test.dart`

- [ ] **Step 1: Write the failing test**

```dart
import 'package:flutter_test/flutter_test.dart';
import 'package:mocktail/mocktail.dart';
import 'package:dnstt/multipath/discovery_orchestrator.dart';
import 'package:dnstt/multipath/multipath_platform.dart';

class _FakePlat extends Mock implements MultipathPlatform {}

void main() {
  test('caches per network key and retriggers on key change', () async {
    final plat = _FakePlat();
    when(() => plat.discover(any())).thenAnswer((_) async =>
        [Resolver(ip: '77.88.8.8', working: true)]);
    final o = DiscoveryOrchestrator(plat);
    await o.ensureDiscovered(networkKey: 'wifi:AA', manual: []);
    await o.ensureDiscovered(networkKey: 'wifi:AA', manual: []);
    verify(() => plat.discover(any())).called(1);          // cached
    await o.ensureDiscovered(networkKey: 'cell:1', manual: []);
    verify(() => plat.discover(any())).called(1);          // new key → re-run
  });
  test('fails closed when zero working', () async {
    final plat = _FakePlat();
    when(() => plat.discover(any())).thenAnswer((_) async =>
        [Resolver(ip: 'x', working: false)]);
    final o = DiscoveryOrchestrator(plat);
    final r = await o.ensureDiscovered(networkKey: 'k', manual: []);
    expect(r.working, isEmpty);
    expect(r.shouldConnect, isFalse);   // fail-closed: do not bring tunnel up
  });
}
```

- [ ] **Step 2: Run test, verify FAIL** — `flutter test test/multipath/discovery_orchestrator_test.dart` → unresolved imports.

- [ ] **Step 3: Implement models + platform + orchestrator**

```dart
// multipath_models.dart
class Resolver {
  final String ip; final int port; final String source;
  final bool passed; final bool hijacked; final bool working; final int rttMs;
  Resolver({required this.ip, this.port = 53, this.source = 'SYSTEM',
    this.passed = true, this.hijacked = false, this.working = false, this.rttMs = -1});
  factory Resolver.fromMap(Map m) => Resolver(ip: m['ip'], port: m['port'] ?? 53,
    source: m['source'] ?? 'SYSTEM', passed: m['passed'] ?? false,
    hijacked: m['hijacked'] ?? false, working: m['working'] ?? false, rttMs: m['rttMs'] ?? -1);
}
class DiscoveryResult { final List<Resolver> working;
  DiscoveryResult(this.working); bool get shouldConnect => working.isNotEmpty; }
```

```dart
// multipath_platform.dart
import 'package:flutter/services.dart';
import 'multipath_models.dart';
class MultipathPlatform {
  final _ch = const MethodChannel('xyz.dnstt.app/multipath');
  Future<List<Resolver>> discover(List<String> manual) async {
    final r = await _ch.invokeMethod('discover', {'manual': manual});
    return (r as List).map((e) => Resolver.fromMap(e)).toList();
  }
  Future<void> start(List<Resolver> w, {String cc='bbr', bool gso=true}) =>
    _ch.invokeMethod('start', {'resolvers': w.map((e)=>'${e.ip}:${e.port}').toList(),
      'cc': cc, 'gso': gso});
  Future<void> stop() => _ch.invokeMethod('stop');
  Future<String> diagnostics() async => await _ch.invokeMethod('diagnostics');
}
```

```dart
// discovery_orchestrator.dart
import 'multipath_models.dart';
import 'multipath_platform.dart';
class DiscoveryOrchestrator {
  final MultipathPlatform _p; DiscoveryOrchestrator(this._p);
  String? _key; DiscoveryResult? _cache; DateTime? _at;
  static const _ttl = Duration(minutes: 10);
  Future<DiscoveryResult> ensureDiscovered(
      {required String networkKey, required List<String> manual}) async {
    final fresh = _at != null && DateTime.now().difference(_at!) < _ttl;
    if (_key == networkKey && _cache != null && fresh) return _cache!;
    final all = await _p.discover(manual);
    _key = networkKey; _at = DateTime.now();
    _cache = DiscoveryResult(all.where((r) => r.working).toList());
    return _cache!;
  }
  void invalidate() { _cache = null; _key = null; }
}
```

- [ ] **Step 4: Run test, verify PASS** — `flutter test test/multipath/discovery_orchestrator_test.dart` (both pass).

- [ ] **Step 5: Commit**

```bash
git add lib/multipath/ test/multipath/discovery_orchestrator_test.dart
git commit -m "feat(multipath): dart models, platform channel, discovery orchestrator"
```

---

## Task 11: Connect flow + statuses + fail-closed wiring

**Files:**
- Modify: connect-screen widget + `DnsttVpnService` (paths from INTEGRATION-NOTES.md).
- Test: `test/multipath/connect_flow_test.dart`

- [ ] **Step 1: Write the failing widget/logic test**

```dart
import 'package:flutter_test/flutter_test.dart';
import 'package:dnstt/multipath/connect_controller.dart';
void main() {
  test('status sequence and fail-closed when no working', () async {
    final c = ConnectController(
      discover: () async => DiscoveryOutcome(working: const [], statuses: const
        ['Поиск рабочего DNS…','Проверка путей…']));
    final seen = <String>[];
    c.onStatus = seen.add;
    final ok = await c.connect();
    expect(ok, isFalse);                       // fail-closed
    expect(seen, contains('Поиск рабочего DNS…'));
    expect(c.lastError, contains('Не найдено рабочего DNS'));
  });
}
```

- [ ] **Step 2: Run test, verify FAIL** — `flutter test test/multipath/connect_flow_test.dart`.

- [ ] **Step 3: Implement `ConnectController`** (`lib/multipath/connect_controller.dart`): drives `DiscoveryOrchestrator` → if `shouldConnect` call `platform.start()` then signal VpnService up; else set `lastError='Не найдено рабочего DNS в этой сети'` and DO NOT bring tunnel up (fail-closed). Emits statuses `Поиск рабочего DNS…`→`Проверка путей…`→`Подключение…`→`Защищено · выход: Германия`; on reconnect emit `Переподключение… интернет временно заблокирован для безопасности`.

- [ ] **Step 4: Run test, verify PASS.**

- [ ] **Step 5: Wire into existing connect screen + VpnService.** Replace the upstream single-DNS connect path: connect button → `ConnectController.connect()`. In `DnsttVpnService`, confirm the spawned multipath process inherits the existing app-uid routing exclusion (the upstream "app is excluded from routing"); add fail-closed: do NOT establish tun / do NOT route until a working multipath client is confirmed; on all-paths-down keep tun down (no leak) and trigger orchestrator re-discovery with exponential backoff (cap retries → error state). Manual device smoke: connect on Wi-Fi → "Защищено · выход: Германия"; egress=49.13.201.110.

- [ ] **Step 6: Commit**

```bash
git add lib/multipath/connect_controller.dart test/multipath/connect_flow_test.dart <connect-screen-file> <DnsttVpnService-file>
git commit -m "feat(multipath): connect flow, statuses, fail-closed lifecycle"
```

---

## Task 12: Advanced screen (manual DNS, CC, GSO, resolver list)

**Files:**
- Create/Modify: advanced settings widget (path from INTEGRATION-NOTES.md).
- Test: `test/multipath/advanced_settings_test.dart`

- [ ] **Step 1: Failing test** — manual DNS input validation: valid `1.1.1.1`, `[2a02::1]:53` accepted; `abc`, `999.1.1.1` rejected with message; saved list is passed into `discover(manual:)`.

```dart
import 'package:flutter_test/flutter_test.dart';
import 'package:dnstt/multipath/manual_dns.dart';
void main() {
  test('validates manual dns entries', () {
    expect(ManualDns.valid('1.1.1.1'), isTrue);
    expect(ManualDns.valid('[2a02::1]:53'), isTrue);
    expect(ManualDns.valid('abc'), isFalse);
    expect(ManualDns.valid('999.1.1.1'), isFalse);
  });
}
```

- [ ] **Step 2: Run test, verify FAIL** — `flutter test test/multipath/advanced_settings_test.dart`.

- [ ] **Step 3: Implement** `lib/multipath/manual_dns.dart` (`ManualDns.valid`, persistence via existing app prefs store from INTEGRATION-NOTES.md) and the Advanced screen: manual DNS list (augments auto — passed as `manual` to `discover`), congestion control selector (bbr default / dcubic), GSO toggle (on default), read-only discovered-resolvers list with `passed/working/rttMs`.

- [ ] **Step 4: Run test, verify PASS.**

- [ ] **Step 5: Commit**

```bash
git add lib/multipath/manual_dns.dart test/multipath/advanced_settings_test.dart <advanced-screen-file>
git commit -m "feat(multipath): advanced screen, manual dns augmenting auto"
```

---

## Task 13: Diagnostics export

**Files:**
- Create: `.../multipath/Diagnostics.kt`, `lib/multipath/diagnostics_screen.dart`
- Test: `.../test/kotlin/.../DiagnosticsTest.kt`

- [ ] **Step 1: Failing test**

```kotlin
package xyz.dnstt.app.multipath
import org.junit.Assert.*
import org.junit.Test
class DiagnosticsTest {
    @Test fun blob_contains_key_sections() {
        val b = Diagnostics.build(
            netType="cellular", ipVersion="IPv6",
            results=listOf(mapOf("ip" to "77.88.8.8","passed" to true,"working" to true,"rttMs" to 120L)),
            clientLogTail=listOf("Connection ready."))
        assertTrue(b.contains("net=cellular")); assertTrue(b.contains("IPv6"))
        assertTrue(b.contains("77.88.8.8")); assertTrue(b.contains("Connection ready."))
    }
}
```

- [ ] **Step 2: Run test, verify FAIL.**

- [ ] **Step 3: Implement** `Diagnostics.build(...)` returning a plain-text blob (net type, IP version, each resolver ip/source/passed/hijacked/working/rttMs, last N client log lines, app+slipstream commit). Add channel method `diagnostics` returning it; Dart `diagnostics_screen.dart` with a "Поделиться диагностикой" button → `Share.share(text)`.

- [ ] **Step 4: Run test, verify PASS.**

- [ ] **Step 5: Manual device check** — trigger a connect, open Diagnostics, Share → text contains resolvers + log tail.

- [ ] **Step 6: Commit**

```bash
git add android/app/src/main/kotlin/xyz/dnstt/app/multipath/Diagnostics.kt android/app/src/test/kotlin/xyz/dnstt/app/multipath/DiagnosticsTest.kt lib/multipath/diagnostics_screen.dart
git commit -m "feat(multipath): diagnostics export (field-feedback channel)"
```

---

## Task 14: Release build, signing, distribution doc

**Files:**
- Create: `DISTRIBUTION.md` (fork root), release keystore (not committed; documented).

- [ ] **Step 1: Generate a release keystore (kept out of git)**

```bash
keytool -genkey -v -keystore release.keystore -alias multipath -keyalg RSA -keysize 2048 -validity 10000
echo "release.keystore" >> .gitignore
```

- [ ] **Step 2: Build signed arm64-v8a release APK**

```bash
flutter build apk --release --target-platform android-arm64
```

- [ ] **Step 3: Verify slipstream lib present + run full test suite**

```bash
unzip -l build/app/outputs/flutter-apk/app-release.apk | grep lib/arm64-v8a/libslipstream_client.so
./gradlew :app:testDebugUnitTest && flutter test
```
Expected: lib line present; all unit tests PASS.

- [ ] **Step 4: Write DISTRIBUTION.md** — exact steps for the non-tech RU tester: download link location, "allow install from this source", MIUI `INSTALL_FAILED_USER_RESTRICTED` → push + tap-install, how to set Domain `e.moskva.live`, that resolvers are auto (manual optional), where "Поделиться диагностикой" is, and the field-test matrix (open Wi-Fi / RU-mobile+whitelist / IPv6-only / captive) with the one-line pass criterion "выход = Германия".

- [ ] **Step 5: Commit**

```bash
git add DISTRIBUTION.md .gitignore
git commit -m "docs: release/distribution guide for remote tester"
```

---

## Self-Review

**Spec coverage:**
- Discovery sources (system/DHCP/gateway/manual) → Task 5. Eligibility gate + anti-hijack → Task 6. Real mini-handshake criterion → Task 7 (+ fallback C escalation gate). Multipath over ALL working → Task 8. Manual augments auto → Tasks 9/10/12. Orchestrator + cache + retrigger → Task 10. Fail-closed default + lifecycle/backoff → Task 11. One-tap UX + statuses → Task 11. Advanced (manual/CC/GSO/list) → Task 12. Diagnostics export → Task 13. Anti-loop (app-uid exclusion) → Task 11 Step 5. Protected probe sockets → Task 6 Step 5 / Task 9 Step 5. arm64-v8a + lib presence → Tasks 1,14. Submodule commit pinned to server → Task 1. Integration-against-real-server, never mock server → Task 7 Step 5. Distribution/MIUI → Task 14. Phase 2 explicitly out of scope — no tasks (correct).
- No spec requirement left without a task.

**Placeholder scan:** Integration-edit anchors deliberately reference `INTEGRATION-NOTES.md` (Task 3) rather than fabricated line numbers for upstream files we cannot see pre-clone — this is a sequenced dependency, not a placeholder; every NEW file has full code. No "TBD/handle edge cases/similar to" left.

**Type consistency:** `Candidate.endpoint()` (v6 bracketed) used consistently in Tasks 5/7/8. `GateResult`/`ProbeResult`/`Resolver` field names (`passed,hijacked,working,rttMs`) consistent across Kotlin (Tasks 6,7,9,13) and Dart (`Resolver.fromMap`, Task 10). `MethodChannel("xyz.dnstt.app/multipath")` identical in Task 9 and Task 10. `--resolver` repetition contract identical in Tasks 7/8.

Issues found & fixed inline: none required.
