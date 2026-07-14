// Opt-in @anthropic-ai/sdk round-trip against a running OTTO Gateway.
//
// Drives the official Anthropic TypeScript SDK at the gateway via
// ANTHROPIC_BASE_URL, exercising both a non-streaming and a streaming
// /v1/messages call. This is the automated form of HUMAN-UAT steps 4-5:
// it proves the gateway's Anthropic surface is wire-compatible enough that
// the real SDK (Zod-validated response parsing) accepts the responses.
//
// Invoked automatically by the Go TestE2E_SDK_RoundTrip subtest when node +
// node_modules (or GW_E2E_SDK=1) are present; otherwise that subtest skips.
// Exit 0 on success, 1 on any failure (including SDK Zod parse errors).

import Anthropic from "@anthropic-ai/sdk";

const baseURL = process.env.ANTHROPIC_BASE_URL;
const apiKey = process.env.ANTHROPIC_API_KEY;

const client = new Anthropic({ baseURL, apiKey });

async function main() {
  // (a) Non-streaming round-trip.
  const msg = await client.messages.create({
    model: "auto",
    max_tokens: 256,
    messages: [{ role: "user", content: "say hi" }],
  });
  if (
    !msg.content ||
    !msg.content[0] ||
    msg.content[0].type !== "text" ||
    !msg.content[0].text
  ) {
    throw new Error(
      `non-streaming response missing text content: ${JSON.stringify(msg)}`,
    );
  }
  console.log(
    `non-streaming PASS: ${String(msg.content[0].text).slice(0, 60)}`,
  );

  // (b) Streaming round-trip — collect event types, then assert the final
  // accumulated message has non-empty text and that the canonical
  // message_start / message_stop events were both observed.
  const seen = new Set();
  const stream = client.messages.stream({
    model: "auto",
    max_tokens: 256,
    messages: [{ role: "user", content: "say hi" }],
  });
  stream.on("streamEvent", (ev) => seen.add(ev.type));

  const final = await stream.finalMessage();
  const finalText =
    final.content && final.content[0] && final.content[0].type === "text"
      ? final.content[0].text
      : "";
  if (!finalText) {
    throw new Error(
      `streaming final message has no text: ${JSON.stringify(final)}`,
    );
  }
  if (!seen.has("message_start") || !seen.has("message_stop")) {
    throw new Error(
      `streaming missing canonical events; saw: ${[...seen].join(", ")}`,
    );
  }
  console.log(
    `streaming PASS: events=[${[...seen].join(", ")}] text=${String(finalText).slice(0, 60)}`,
  );

  console.log("E2E SDK: PASS");
  process.exit(0);
}

main().catch((err) => {
  console.error(`E2E SDK: FAIL: ${err && err.message ? err.message : err}`);
  if (err && err.stack) {
    console.error(err.stack);
  }
  process.exit(1);
});
