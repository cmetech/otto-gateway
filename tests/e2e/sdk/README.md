# OTTO Gateway E2E — Node SDK harness

This directory holds the opt-in Node `@anthropic-ai/sdk` round-trip harness
that enables HUMAN-UAT steps 4-5: it proves the gateway's Anthropic surface
is wire-compatible enough that the official Anthropic TypeScript SDK (with
its Zod-validated response parsing) accepts both non-streaming and streaming
`/v1/messages` responses.

It is NOT part of the default Go test run and installs no node modules by
default. `node_modules/` is gitignored; the source files
(`package.json`, `sdk_roundtrip.mjs`, this README) are tracked.

## One-time setup

From the module root:

```
make e2e-sdk-setup
```

or directly:

```
cd tests/e2e/sdk && npm install
```

This installs `@anthropic-ai/sdk` into `tests/e2e/sdk/node_modules/`.

## How it runs

The Go `TestE2E_SDK_RoundTrip` subtest (in `tests/e2e/e2e_test.go`, behind
the `e2e` build tag + `OTTO_E2E=1`) invokes `sdk_roundtrip.mjs` automatically
when BOTH of these hold:

- `node` is on `PATH`, and
- `tests/e2e/sdk/node_modules` exists (or `OTTO_E2E_SDK=1` is set).

When either is missing the subtest skips cleanly with a pointer back to
`make e2e-sdk-setup`. When it runs, the gateway is booted by the Go test and
the harness receives `ANTHROPIC_BASE_URL` + `ANTHROPIC_API_KEY` via the
environment; it exits 0 on success and 1 on any failure (including SDK parse
errors), which the Go test maps to pass/fail.

## Running by hand

With a gateway already running:

```
ANTHROPIC_BASE_URL=http://127.0.0.1:11435 \
ANTHROPIC_API_KEY=your-token \
node sdk_roundtrip.mjs
```
