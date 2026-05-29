---
id: SEED-001
status: dormant
planted: 2026-05-28
planted_during: v1.5 (audit WARNINGs — post Phase 08.1 + v1.5.0/1/2 releases)
trigger_when: cert procurement is unblocked OR partner-facing distribution becomes a requirement OR operator-friction complaints reach a threshold
scope: medium — single milestone (1-3 phases) once cert is in hand
---

# SEED-001: Authenticode code signing for otto-gateway Windows distribution (.exe + .ps1)

## Why This Matters

The v1.5.x release cycle ended with a working but friction-laden Windows install path: every fresh extract of a release zip triggers Mark-of-the-Web (MOTW) tagging on every file, PowerShell `RemoteSigned` policy refuses to run the wrapper scripts, and Windows SmartScreen flags `otto-gateway.exe` as "unrecognized publisher" on first run. We shipped a workaround (`scripts/setup.bat` strips MOTW via `Unblock-File` + sets `CurrentUser` execution policy to `RemoteSigned`) but the workaround is exactly that — a workaround. The structural fix is Authenticode digital signatures on the binary and the PowerShell scripts.

Two distinct user-visible problems, same underlying technology:

| Problem | Current state | Authenticode fix |
|---------|---------------|------------------|
| `.exe` first-run SmartScreen warning | Operator clicks "More info" → "Run anyway" once per machine | Sign the `.exe` with a code-signing cert; EV cert gives instant SmartScreen rep, OV needs warmup |
| `.ps1` execution policy + MOTW (what setup.bat works around) | Operator runs `setup.bat` once after every extract | Sign the `.ps1` files; `RemoteSigned` accepts signed scripts even with MOTW; `setup.bat` becomes a no-op or gets deleted |

The deeper "why" is operator trust: a signed binary tells the operator "this came from CMETech/loop24 team" cryptographically, not just "the SHA matches the file someone gave me." Combined with SHA256SUMS verification (already in place via the publish-script feature), signing closes the trust loop for internal team distribution.

## When to Surface

**Trigger conditions** (any of these makes the seed actionable):

1. **Cert procurement unblocks.** Internal Ericsson PKI confirms availability of Authenticode (codeSigning EKU) certs for service accounts, OR team approves budget for Azure Trusted Signing (~$120/yr) or an EV cert from a public CA.
2. **Distribution scope changes.** Today otto-gateway ships to internal team only (MinIO + Artifactory). If we ever distribute to partners or customers, public-CA signing becomes mandatory (corp PKI won't be trusted outside the AD forest).
3. **Operator friction hits a threshold.** If the `setup.bat` workaround starts producing support tickets (operators forgetting to run it, GPO-locked machines where it fails, etc.) the structural fix becomes higher-priority than the workaround.
4. **Build pipeline migrates to Windows runners.** Currently builds happen on a Mac dev box. If we move cross-compilation to a Windows CI runner, `signtool.exe` becomes natively available and the signing step is a 5-line Makefile addition.

**Will auto-surface during:** `/gsd-new-milestone` whenever a Windows-distribution, security-hardening, or operator-friction milestone is being scoped. Also during `/gsd-roadmap` reviews when v1.6 or later is being planned.

## Scope Estimate

**Medium — single milestone, 1-3 phases.** Once the cert is in hand, the implementation work is mechanical:

| Phase | Estimate | Scope |
|-------|----------|-------|
| Procure + onboard cert | 1-4 weeks (mostly waiting on PKI/CA) | Out-of-band; no code change. Result: cert + private key accessible to the build process. |
| Wire signing into build | 1 phase, ~5 plans | Makefile `sign-windows` target; CI integration; signature verification step; documentation. |
| Migrate setup.bat | 1 phase, ~3 plans | Remove `Unblock-File` step from setup.bat (signed scripts don't need it); optionally delete setup.bat entirely; update operator docs (INSTALL.md, operator-quickstart.md). |
| Add signing for non-Windows targets | optional follow-up | macOS notarization (requires paid Apple Developer ID, separate workflow); Linux has no equivalent reputation system. |

**Total estimate:** 2-3 phases of focused work plus cert procurement lead time. Could fit in a single v1.6 or v1.7 milestone alongside other distribution hardening.

## Breadcrumbs

**Code references this seed would touch:**

- `Makefile:81-118` — `cross-*` and `package-*` targets where signing would inject. Current `codesign_adhoc` macro at lines 152-160 handles macOS ad-hoc signing; the Windows analog would mirror its shape but use `signtool sign /tr <ts-url> /td sha256 /fd sha256 /a`.
- `scripts/setup.bat` — current MOTW workaround. Becomes obsolete after `.ps1` signing.
- `scripts/otto-gw.ps1` — would need `# SIG # Begin signature block` / `# SIG # End signature block` lines appended by `Set-AuthenticodeSignature`. Affects line counts of the file.
- `scripts/otto-gw.bat`, `scripts/start.bat`, `scripts/stop.bat`, `scripts/status.bat` — cmd.exe scripts don't support Authenticode (cmd has no signature verification surface). They remain unsigned; they continue to work because cmd has no execution policy.
- `.github/workflows/ci.yml` — current build job. Would gain a signing step (probably as a separate `release-sign` job downstream of `cross-compile-smoke`).
- `docs/INSTALL.md` — new section on signature verification (`Get-AuthenticodeSignature ./bin/otto-gateway.exe` expected output, `signtool verify /pa /v ./bin/otto-gateway.exe`).
- `docs/operator-quickstart.md` — drop the `setup.bat` step from the Windows quickstart once signing is live.

**Related session work (this v1.5 milestone):**

- v1.5.1 introduced the `.bat` surface (`setup.bat`, `otto-gw.bat`, `start/stop/status.bat`) as the workaround for the unsigned-scripts problem. Commits `86b76ac`, `c034d83`.
- v1.5.0 wrapper hotfixes addressed multiple downstream symptoms: UTF-8 BOM, StrictMode `.Source` guards, IPv4 probe, `kiro-cli` auto-detect. Commits `8645d94`, `567ed80`, `7185293`, `257096b`, `8924464`.
- v1.5.2 shipped `docs/INSTALL.md` with detailed wrapper-choice tradeoffs that signing would obsolete. Commit `07f4f46`.

**Cost-pole references** (from the session research):

- DigiCert KeyLocker (cloud HSM, premium pricing)
- SSL.com eSigner (~$200-400/yr, cloud HSM)
- Sectigo OV with cloud HSM (~$200-300/yr)
- Certum Open Source Code Signing (~$50/yr, OSS-status required — likely not applicable since otto-gateway is internal/proprietary)
- Azure Trusted Signing (~$10/month + per-signature, no hardware tokens, native GHA action `azure/trusted-signing-action`)

## Notes

### Certificate procurement decision tree

**Step 1 — Ask Ericsson platform/IT team first:**

> "Does our internal PKI issue Authenticode (codeSigning EKU) certs to developers or service accounts? If yes, what's the request process and which CA root distributes via GPO so domain-joined Windows machines trust it automatically?"

If YES → ~1 week turnaround, zero ongoing cost, all domain-joined laptops trust it automatically. This is the best path for internal-only distribution.

If NO → proceed to step 2.

**Step 2 — Pick a public-CA path:**

- **Azure Trusted Signing** (recommended modern path) — Microsoft's cloud signing service, ~$10/month + per-signature, no hardware tokens to manage, official GitHub Actions integration, instant SmartScreen reputation.
- **EV cert from public CA** (SSL.com, DigiCert, Sectigo) — ~$300-700/yr, hardware token or cloud HSM, instant SmartScreen reputation. More work to set up than Azure Trusted Signing.
- **OV cert with cloud HSM** — ~$100-400/yr but standard OV certs need SmartScreen reputation warmup (first ~hundreds of downloads still warn).

**Avoid:**

- Self-signed certs (lab-only; not trusted outside the issuing machine).
- Deprecated CA/B Forum pre-2023 standard certs (no longer compliant; hardware token / cloud HSM is now mandatory).

### Implementation sketch

Once the cert is in hand, the Makefile sketch:

```make
# Windows code signing — Authenticode via signtool
SIGNTOOL    ?= signtool
TIMESTAMP_URL ?= http://timestamp.digicert.com
SIGN_CERT_THUMBPRINT ?= $(shell echo $$WINDOWS_CERT_THUMBPRINT)

sign-windows: cross-windows-amd64
	$(SIGNTOOL) sign /tr $(TIMESTAMP_URL) /td sha256 /fd sha256 \
		/sha1 $(SIGN_CERT_THUMBPRINT) \
		$(BUILD_DIR)/$(BINARY)-windows-amd64.exe
	$(SIGNTOOL) verify /pa /v $(BUILD_DIR)/$(BINARY)-windows-amd64.exe

sign-ps1: $(PS1_FILES)
	pwsh -NoProfile -Command "Get-ChildItem scripts/*.ps1 | ForEach-Object { Set-AuthenticodeSignature -FilePath \$$_.FullName -Certificate (Get-ChildItem cert:\CurrentUser\My\$(SIGN_CERT_THUMBPRINT)) -TimestampServer $(TIMESTAMP_URL) }"
```

Build host options for signing:

- **Windows CI runner** (recommended) — `signtool.exe` natively available; integrate via GitHub Actions or self-hosted runner.
- **macOS dev box with osslsigncode** (https://github.com/mtrojnar/osslsigncode) — open-source CLI that signs Windows PE binaries from non-Windows. Useful if we want to keep cross-compile on macOS but add signing.
- **Azure Trusted Signing** (no local cert handling) — entire signing happens in the cloud; build host only needs the Azure CLI and credentials.

### Reading order (when ready to plan)

About 45 minutes total:

1. Microsoft Learn → search `about_Signing` (10 min) — PowerShell .ps1 signing model.
2. Microsoft Learn → search `Authenticode digital signatures` (15 min) — .exe signing model.
3. Microsoft Learn → search `Azure Trusted Signing overview` (10 min) — recommended modern path.
4. Search `signtool dual signing sha256 sha1` (5 min) — only needed if legacy-Windows compat matters.
5. GitHub Marketplace → search `azure/trusted-signing-action` (5 min) — CI integration.

### Open questions for the planning phase

- Internal PKI availability + Authenticode EKU support at Ericsson? (Highest-leverage answer; gates everything else.)
- Distribution scope: internal-only (corp PKI sufficient) vs partner-facing (needs publicly-trusted CA)?
- Build host strategy: macOS dev box with `osslsigncode`, Windows CI runner with `signtool`, or cloud signing via Azure Trusted Signing?
- Re-publish strategy after signing lands: re-tag existing v1.5.x with signed artifacts, or cut v1.5.3+ as the first signed release? (Re-tagging is generally bad practice; cutting a new patch is cleaner.)

### Adjacent items to consider when planning

- **Timestamp server choice.** DigiCert, GlobalSign, Sectigo all run free timestamp authorities (TSAs). Signature stays valid past cert expiry ONLY if timestamped at sign time — critical for long-lived release artifacts.
- **Cert rotation / renewal process** — needs documentation; ideally automated via secret rotation in CI.
- **Signing for non-Windows:**
  - macOS notarization (separate workflow, requires paid Apple Developer ID, ~$99/yr). Currently we ad-hoc-sign which only suppresses LOCAL Gatekeeper, not download-marked Gatekeeper. Operators on macOS still see "cannot be opened because Apple cannot check it" after browser-downloading the tarball.
  - Linux has no equivalent reputation system. Signing optional; SHA256SUMS + GPG sign of SUMMARY file is the closest analog.

### Connection to other deferred items

Touches the **publish script HTTP-302 + abort-after-1 bugs** I noted in the v1.5.0 session — both involve trust-chain failures (expired Cloudflare mTLS cert at Artifactory; SmartScreen reputation for unsigned exe). Worth thinking about as a "trust hardening" milestone bundle rather than separately.

Also relevant: the **Phase 9 distribution `make ci` fmt-check failure** mentioned in `.planning/phases/08.1-…/deferred-items.md`. If we're going to do build-pipeline work for signing anyway, the fmt-check cleanup is a natural co-traveler.

---

_Captured 2026-05-28 during the v1.5 milestone wrap-up, after shipping v1.5.0/1/2 and discussing code signing as the structural fix for the recurring Windows MOTW friction. See session commits ab9d88d..e2b6340._
