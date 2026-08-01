# Credential conformance correction

## RED

At HEAD `b013b48`, the unchanged Task17 test
`TestConformanceCredentialFormatsAreOneWay` returned HTTP 422 with
`privacy_input_blocked` for exactly four structured assignments:

| Form | Transformed request observed before the block | Residual entity |
|---|---|---|
| JSON password | `{"password":"[SECRET:PASSWORD_B9F107BF7960]"}` | `PASSWORD` |
| YAML client secret | `client_secret: [SECRET:CLIENT_SECRET_73EAB9983A14]` | `CLIENT_SECRET` |
| dotenv refresh token | `REFRESH_TOKEN=[SECRET:REFRESH_TOKEN_591AF4FCD7FC]` | `REFRESH_TOKEN` |
| CLI access token | `--access-token=[SECRET:ACCESS_TOKEN_7D17A1F7D9B5]` | `ACCESS_TOKEN` |

Bearer, basic, GitHub, OpenAI, credentialed URL, and PEM private-key rows
already passed. Focused owning-package tests reproduced the four false blocks
and also exposed that a caller-forged marker inside a credential assignment
was rewritten into a newly authorized nested label instead of being rejected.

## Root cause

Recognition, byte offsets, arbitration, and the first rewrite were correct:
each raw credential was replaced at exactly its value span. The independent
residual classifier then treated the generated one-way label as another raw
structured credential. `isRedactedAssignment` exempted only `[REDACTED]`, and
the structured-assignment scanner also scanned the `SECRET:` syntax inside a
valid one-way marker.

The correction gives the classifier and residual validator one shared
one-way-label grammar. Structured credential assignments treat a valid
`[SECRET:<ENTITY>_<12 HEX>]` value as already redacted, and the assignment
scanner precomputes valid marker spans once and advances monotonically past
them. This preserves linear scanning and adds no lock. The residual token pass
remains the authority for provenance: service-generated occurrences pass;
caller-forged occurrences remain untouched and return
`privacy_input_blocked`.

## GREEN

- Focused classifier/service tests pass for JSON, YAML, dotenv, and CLI.
- Every transformed value omits the raw credential and has the expected typed
  one-way label.
- Scope inspection is empty for every credential case, proving neither the
  credential nor its original enters the reversible mapping ledger.
- A forged one-way marker in a password assignment remains byte-identical and
  is blocked by residual occurrence authorization.
- The unchanged Task17 credential conformance row passes all ten formats.
- Existing standard compatibility, classifier overlap, arbiter, and privacy
  suites pass.

## Verification

```text
go test ./internal/privacy ./internal/plugin/pii ./tests/privacy -count=1
PASS

go test -race ./internal/privacy ./tests/privacy -run 'TestSecretClassifierPreservesOneWayCredentialAssignmentMarkers|TestServiceStrict_(InboundCredentialAssignmentsRemainOneWay|ResidualRejectsForgedCredentialAssignmentMarker)|TestConformanceCredentialFormatsAreOneWay' -count=20
PASS

go test ./tests/privacy -run '^TestConformanceCredentialFormatsAreOneWay$' -count=1 -v
PASS (10/10 subtests)

go vet ./...
PASS

git diff --check
PASS
```

## Final state

Production changes are confined to `internal/privacy`: the shared credential
marker grammar and linear structured-assignment scan. Focused regression tests
are also confined to the owning privacy package. Task17's untracked conformance
files were neither edited nor staged.
