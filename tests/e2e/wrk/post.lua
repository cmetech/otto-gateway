-- tests/e2e/wrk/post.lua
--
-- wrk Lua script used by Plan 05-05's perf measurement protocol
-- (see tests/e2e/reports/PHASE5-PERF.md "Measurement Protocol — Latency").
--
-- Sets the HTTP method to POST, the Content-Type to application/json,
-- and reads the request body from /tmp/req.json so the operator can
-- pre-stage a deterministic ACP /api/chat payload outside the script.
--
-- Usage (operator):
--   echo '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}' > /tmp/req.json
--   wrk -t4 -c8 -d30s --latency -s tests/e2e/wrk/post.lua http://127.0.0.1:11434/api/chat
--
-- The script intentionally fails loudly (assert) when /tmp/req.json is
-- missing — silently sending an empty body would skew the measurement.

wrk.method = "POST"
wrk.headers["Content-Type"] = "application/json"

local f = io.open("/tmp/req.json", "rb")
assert(f ~= nil, "wrk/post.lua: /tmp/req.json not found — stage the request body before running wrk")
wrk.body = f:read("*all")
f:close()
