// Opt-in official `openai` SDK round-trip against a running OTTO Gateway.
//
// Drives the official OpenAI Node SDK at the gateway via OPENAI_BASE_URL,
// exercising both a non-streaming and a streaming /v1/chat/completions call.
// This is the automated form of the Phase 3 Pi-SDK HUMAN-UAT (SC2 / SURF-06):
// Pi (@earendil-works/pi-ai) configures the OpenAI provider with baseUrl=…/v1
// and constructs this exact SDK under the hood, hard-coding stream:true — so
// proving the official SDK accepts the gateway's responses proves the Pi bar.
//
// Invoked automatically by the Go TestE2E_OpenAI_SDK_RoundTrip subtest when
// node + node_modules (or GW_E2E_SDK=1) are present; otherwise that subtest
// skips. Exit 0 on success, 1 on any failure (including SDK parse errors).

import OpenAI from "openai";

const baseURL = process.env.OPENAI_BASE_URL; // includes the /v1 prefix
const apiKey = process.env.OPENAI_API_KEY;

const client = new OpenAI({ baseURL, apiKey });

async function main() {
  // (a) Non-streaming round-trip.
  const completion = await client.chat.completions.create({
    model: "auto",
    messages: [{ role: "user", content: "say hi" }],
    stream: false,
  });
  const choice = completion.choices && completion.choices[0];
  if (
    !choice ||
    !choice.message ||
    choice.message.role !== "assistant" ||
    !choice.message.content
  ) {
    throw new Error(
      `non-streaming response missing assistant content: ${JSON.stringify(completion)}`,
    );
  }
  if (!choice.finish_reason) {
    throw new Error(
      `non-streaming response missing finish_reason: ${JSON.stringify(completion)}`,
    );
  }
  console.log(
    `non-streaming PASS: finish_reason=${choice.finish_reason} text=${String(choice.message.content).slice(0, 60)}`,
  );

  // (b) Streaming round-trip — this is the load-bearing Pi path (stream:true).
  // Accumulate deltas and assert non-empty content plus a terminal
  // finish_reason. The SDK parses the `data: {chunk}` / `data: [DONE]` framing
  // internally; a framing error would throw here.
  const stream = await client.chat.completions.create({
    model: "auto",
    messages: [{ role: "user", content: "say hi" }],
    stream: true,
  });

  let text = "";
  let finishReason = null;
  let sawRole = false;
  for await (const chunk of stream) {
    const d = chunk.choices && chunk.choices[0];
    if (!d) continue;
    if (d.delta && d.delta.role === "assistant") sawRole = true;
    if (d.delta && typeof d.delta.content === "string") text += d.delta.content;
    if (d.finish_reason) finishReason = d.finish_reason;
  }

  if (!sawRole) {
    throw new Error("streaming: never saw a delta with role=assistant");
  }
  if (!text) {
    throw new Error("streaming: accumulated content was empty");
  }
  if (!finishReason) {
    throw new Error("streaming: never received a finish_reason");
  }
  console.log(
    `streaming PASS: finish_reason=${finishReason} text=${String(text).slice(0, 60)}`,
  );

  console.log("E2E OpenAI SDK: PASS");
  process.exit(0);
}

main().catch((err) => {
  console.error(`E2E OpenAI SDK: FAIL: ${err && err.message ? err.message : err}`);
  if (err && err.stack) {
    console.error(err.stack);
  }
  process.exit(1);
});
