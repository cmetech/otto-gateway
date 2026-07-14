"Gateway Architecture — Unified LLM Gateway"
Subtitle: "Anthropic-, OpenAI-, and Ollama-compatible APIs, governed by configurable guardrails, routed to pooled kiro-cli ACP workers"

Audience: Enterprise executives, platform leadership, AI governance stakeholders. Readable in under 10 seconds. Non-technical-executive friendly.

Style: Premium executive dark-mode infographic, polished enterprise aesthetic, clean and spacious, high contrast, minimal text.
Palette: background #1E1E2E, text #FAFAFA, secondary gray #A0A0A0, panel gray #3A3A4A.
Blue #1174E6 = Gateway components (dominant accent).
Yellow #FAD22D = client applications.
Orange #FF8C0A = guardrails / policy layer (focal point).
Green #0FC373 = kiro-cli ACP workers + approved paths.
Red #FF3232 = rejected requests (blocked at guardrails, never reach kiro-cli).

Main message: "One server. Three API standards. Configurable guardrails. Pooled kiro-cli ACP workers."

LAYOUT: Left-to-right, three zones — CLIENTS → GATEWAY → KIRO-CLI WORKERS. Thin reverse arrow along the bottom for streamed responses.

LEFT — CLIENT APPLICATIONS (yellow), three stacked panels:
Top "Anthropic-compatible clients": OTTO CLI (first-party chat client on the GSD Anthropic fork, label "POST /v1/messages") + "Future Anthropic clients" (Claude Code, MCP hosts, anthropic-sdk consumers). Terminal/chat-bubble icons.
Middle "OpenAI-compatible clients": "Future OpenAI clients" (LangChain, Continue.dev, OpenAI-SDK consumers, label "POST /v1/chat/completions"). Terminal/chat-bubble icons.
Bottom "Ollama-compatible clients": LangFlow server (low-code flows configured for Ollama) + "Other Ollama clients" (Open WebUI, llama-index). Flow-diagram icons.
Caption beneath: "Existing client code keeps working — no SDK changes required."

CENTER — GATEWAY (blue dominant), three vertical bands:

Top band "API surfaces" (blue): three side-by-side adapter blocks — "Anthropic adapter" (/v1/messages), "OpenAI adapter" (/v1/chat/completions, /v1/embeddings, /v1/models), and "Ollama adapter" (/api/chat, /api/generate, /api/embed, /api/tags). Inbound arrows from LEFT zone land on the correct adapter. Note: "All three adapters translate to a single canonical request format."

Middle band "Guardrails / policy chain" (orange, slightly taller than other bands — the focal point): horizontal row of hexagonal hook tiles — Auth, Rate limit, Content moderation, Schema validation, Audit log. Above row: "Configurable — enable/disable per deployment."
Two outcomes leave this band:
- PASS (green arrow) continues down. Label: "Request approved."
- REJECT (red arrow, thinner, semi-transparent) curves back LEFT with a "blocked" badge. Label: "Policy violation — 4xx returned. kiro-cli never invoked."

Bottom band "Engine + pool" (blue): three sub-components in a row — "Canonical engine" (request lifecycle, streaming), "Session pool" (warm kiro-cli slots, default 4), "Embedding registry" (local ONNX, no kiro-cli). Right-edge arrow points to RIGHT zone. Embedding registry has a small arrow curving back LEFT: "Embeddings served locally."

Side callout: "Single process. Single port. Three API standards. One governance surface."

RIGHT — KIRO-CLI ACP WORKER POOL (green):
Panel labeled "kiro-cli ACP Workers" with a 2×2 grid of worker tiles. Each tile: subprocess icon, label "kiro-cli acp", status dot (3 green idle, 1 amber busy).
Channel label above grid: "JSON-RPC 2.0 over stdio."
Active inbound arrow into the busy worker: "session/prompt."
Outbound arrow back toward gateway: "session/update — streamed text, thoughts, tool_calls, plans."
Below grid: "Stateless requests pull from the warm pool. Stateful sessions (X-Session-Id header) get a dedicated worker until TTL expires."

BOTTOM STRIP — response path: thin RIGHT-to-LEFT arrow. Label: "Streamed responses — SSE for Anthropic and OpenAI clients, NDJSON for Ollama. Same canonical chunks, surface-specific encoding."

LEGEND (full-width bottom strip):
Blue = Gateway (Go server) · Yellow = Client applications (no changes) · Orange = Guardrails (configurable) · Green = kiro-cli ACP workers + approved paths · Red = Rejected requests · Solid arrow = request flow · Thin arrow = response stream.

TOP-RIGHT CALLOUTS (muted gray):
• One binary. Cross-compiled for Linux + Windows.
• Three API standards in one process — no per-client deployment.
• Guardrails are the single place to add governance — they cover all three API surfaces.

AESTHETICS: plenty of negative space; consistent tile sizes and corner radius; orange guardrails band slightly taller and visually prominent; thin (~2px) rounded arrows with small chevron terminations; sans-serif type (Inter or IBM Plex Sans); reject path visually subordinate to approved path.
