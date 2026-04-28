# codeweaver Binary Release Rollback Runbook

Owner: devops-engineer
Last updated: 2026-04-28

---

## When to use this runbook

A published codeweaver binary release needs to be recalled when:

- The binary crashes on startup or during normal operation
- The JSON output is malformed or violates the output schema v1
- A security vulnerability is discovered in the binary or its dependencies
- The binary was built from the wrong commit or with incorrect flags
- The SHA256SUMS manifest is incorrect (hash mismatch between manifest and archive)
- A signing or attestation failure is discovered post-publish

---

## Severity classification

| Severity | Condition | Response posture |
|----------|-----------|-----------------|
| P0 | Crash on first run, data corruption risk, signing-key compromise | Immediate; yank within 15 minutes |
| P1 | Incorrect JSON output, performance regression causing timeouts | Within 1 hour |
| P2 | Cosmetic bug, non-fatal warning, documentation error | Business hours |

---

## Step 1: Yank the channel pointer (NEVER delete assets)

Mark the bad release as pre-release. This demotes it from the `latest` pointer
without deleting the assets. Pinned plugin clients get a clear yanked-version
signal; if assets were deleted they would receive a 404 with no recovery path,
and the forensic evidence for the supply-chain postmortem would be destroyed.

```sh
# Replace X.Y.Z with the version being recalled
gh release edit codeweaver-vX.Y.Z --prerelease --repo endless-galaxy-studios/codeweaver
```

Confirm the `latest` pointer now resolves to the previous stable version:

```sh
gh release view --repo endless-galaxy-studios/codeweaver | head -5
```

---

## Step 2: Update release notes with a recall notice

```sh
# Open the release for editing
gh release edit codeweaver-vX.Y.Z \
  --notes "## RECALL NOTICE

This release has been recalled. Do not use this version.

**Reason:** [brief description of the issue]

**Action required:** [what users should do — upgrade to vX.Y.Z+1, revert to vA.B.C, etc.]

---

[original release notes follow]
..." \
  --repo endless-galaxy-studios/codeweaver
```

---

## Step 3: Decide rollback vs rollforward

**Rollback only (yank pointer) is sufficient when:**
- No plugin client has yet bootstrapped the bad binary (confirmed via plugin
  telemetry — see interservice-engineer for bootstrap success-rate SLI data)
- The issue is cosmetic and does not affect any user session

**Rollforward is also required when:**
- Users have already bootstrapped the bad binary into their plugin cache
- The issue causes sessions to fail or data to be corrupted

For rollforward: publish a new version (e.g., vX.Y.Z+1) with the fix applied.
Coordinate with interservice-engineer on plugin-side cache-busting: the plugin
must know to skip the yanked version and upgrade past it.

---

## Step 4: Publish a fixed version (rollforward path)

```sh
# Tag the fix commit (after the fix is merged to main)
git tag codeweaver-vX.Y.Z+1
git push origin codeweaver-vX.Y.Z+1
```

The release workflow triggers automatically on the tag push. Monitor the
workflow run for all 5 matrix targets, reproducibility check, and asset
verification before the draft is promoted.

After the new version is published, confirm `latest` resolves to it:

```sh
gh release view --repo endless-galaxy-studios/codeweaver | grep "tag:"
```

---

## First-release emergency path

This path applies when the recalled release is the first published version and
there is no known-good prior binary to repoint `latest` to.

**Primary recovery:** Publish a fixed version as quickly as possible (see
Step 4 above). Consumers who have pinned to the bad version should be directed
to upgrade to the fixed version via the recall notice in the release notes.

If no prior stable binary exists to roll back to, there is no alternative
until the fixed binary is published. Prioritize the rollforward (Step 4) and
communicate the ETA clearly in the recall notice.

---

## Signing-key compromise response

If a signing key or Apple Developer ID certificate is suspected to be
compromised:

1. Revoke the certificate immediately:
   - Apple Developer ID: via developer.apple.com → Certificates
   - Authenticode (when implemented): via the issuing CA's revocation portal

2. Yank ALL releases signed with the compromised key (mark as pre-release).

3. Rebuild the signing infrastructure on a clean, isolated runner.

4. Re-sign and re-publish a fresh release from clean infra.

5. Issue a security advisory documenting the compromise timeline and the
   set of releases with repudiated signatures.

6. Coordinate with infosec-engineer for the supply-chain postmortem.

---

## Post-recall checklist

- [ ] Bad release marked as `prerelease: true` (yanked from `latest`)
- [ ] Release notes updated with recall notice and user action
- [ ] Plugin bootstrap failure-rate SLI checked (interservice-engineer)
- [ ] Rollforward decision made and documented
- [ ] Fixed version published (if rollforward required)
- [ ] Plugin-side cache-busting coordinated with interservice-engineer (if users affected)
- [ ] Windows Defender / VirusTotal scan result available for the fixed version
- [ ] Blameless postmortem opened (five-whys, within 48 hours of resolution)

---

## Reference commands

```sh
# View all codeweaver releases (latest first)
gh release list --repo endless-galaxy-studios/codeweaver

# View a specific release
gh release view codeweaver-vX.Y.Z --repo endless-galaxy-studios/codeweaver

# Download an archive for manual inspection
gh release download codeweaver-vX.Y.Z \
  --pattern "codeweaver_vX.Y.Z_linux_amd64.tar.gz" \
  --repo endless-galaxy-studios/codeweaver

# Verify build attestation on a downloaded archive
gh attestation verify codeweaver_vX.Y.Z_linux_amd64.tar.gz \
  --repo endless-galaxy-studios/codeweaver

# Verify SHA256SUMS attestation (primary trust anchor)
gh attestation verify SHA256SUMS --repo endless-galaxy-studios/codeweaver
```
