#!/usr/bin/env node

// Real OpenAI SDK contract client for issue #577.
//
// This script is invoked by the Go `openaicontract` test suite
// (../openai_sdk_contract_test.go) once per scenario. The Go test starts a
// real TCP listener in front of the broker's in-process gin engine (backed by
// a real MySQL testcontainer) plus a mocked OpenAI-shaped upstream, then execs
// this script pointed at that listener. Driving the broker through the actual
// `openai` npm package — not Go structs — is the point: it exercises the real
// wire bytes (SSE framing, JSON shape, header casing, SDK error typing) the
// Go-only integration tests never touch.
//
// Each scenario function performs its own assertions using the SDK's own
// parsing/typing and returns a small result object. main() prints exactly one
// JSON line to stdout: {"ok":true,"scenario":...,"result":...} on success, or
// {"ok":false,"error":...} on failure (also causing a non-zero exit code).

const OpenAI = require("openai");

// expectError runs makeRequest(), expecting it to throw, and captures the
// SDK-visible error shape. extraFields optionally derives additional fields
// (e.g. an `instanceof` check) from the caught error. Standalone (not a
// `scenarios` method) because main() calls scenario functions detached from
// their object (`const fn = scenarios[scenario]; ... await fn()`), so a
// `this.expectError()` call from within a scenario would not resolve.
async function expectError(makeRequest, extraFields) {
  try {
    await makeRequest();
  } catch (err) {
    return {
      errorType: err.constructor.name,
      status: err.status,
      ...(extraFields ? extraFields(err) : {}),
    };
  }
  throw new Error("expected request to fail, but it succeeded");
}

async function main() {
  const scenario = process.env.SCENARIO;
  const baseURL = process.env.BASE_URL;
  const authHeader = process.env.AUTH_HEADER || "";
  const model = process.env.MODEL || "gpt-4o";
  const timeout = 30000;

  if (!baseURL) throw new Error("BASE_URL env var is required");

  // Mirrors the working pattern in api/inference/integration/all-in-one/
  // test-serving-capability.js: apiKey is left empty on the client (the
  // broker does not use OpenAI-style API keys) and the real session-token
  // Authorization header is passed per-request instead.
  const client = new OpenAI({ baseURL, apiKey: "", maxRetries: 0 });
  const authedOpts = { timeout, headers: authHeader ? { Authorization: authHeader } : {} };

  const scenarios = {
    async nonstream() {
      const completion = await client.chat.completions.create(
        { model, messages: [{ role: "user", content: "Hi" }] },
        authedOpts,
      );
      const content = completion.choices?.[0]?.message?.content;
      if (content !== "Hello world") {
        throw new Error(`unexpected content: ${JSON.stringify(completion.choices)}`);
      }
      if (!completion.usage || completion.usage.total_tokens !== 12) {
        throw new Error(`unexpected usage: ${JSON.stringify(completion.usage)}`);
      }
      // #184: the upstream's own id must never leak to the client.
      if (!completion.id || !completion.id.startsWith("chatcmpl-") || completion.id === "chatcmpl-001") {
        throw new Error(`expected upstream id rewritten, got ${completion.id}`);
      }
      return { content, usage: completion.usage, id: completion.id };
    },

    async stream() {
      const stream = await client.chat.completions.create(
        {
          model,
          messages: [{ role: "user", content: "Hi" }],
          stream: true,
          stream_options: { include_usage: true },
        },
        authedOpts,
      );
      let content = "";
      let usage = null;
      let chunkCount = 0;
      for await (const chunk of stream) {
        chunkCount++;
        const delta = chunk.choices?.[0]?.delta?.content;
        if (delta) content += delta;
        if (chunk.usage) usage = chunk.usage;
      }
      if (content !== "Hello world") {
        throw new Error(`unexpected streamed content: ${JSON.stringify(content)}`);
      }
      if (!usage || usage.total_tokens !== 12) {
        throw new Error(`expected a stream_options.include_usage usage chunk, got ${JSON.stringify(usage)}`);
      }
      return { content, usage, chunkCount };
    },

    async toolcall() {
      const completion = await client.chat.completions.create(
        {
          model,
          messages: [{ role: "user", content: "What is the weather in Paris?" }],
          tools: [
            {
              type: "function",
              function: {
                name: "get_weather",
                description: "Get the current weather for a location",
                parameters: {
                  type: "object",
                  properties: { location: { type: "string" } },
                  required: ["location"],
                },
              },
            },
          ],
        },
        authedOpts,
      );
      const toolCalls = completion.choices?.[0]?.message?.tool_calls;
      if (!toolCalls || toolCalls.length !== 1 || toolCalls[0].function.name !== "get_weather") {
        throw new Error(`unexpected tool_calls: ${JSON.stringify(toolCalls)}`);
      }
      const args = JSON.parse(toolCalls[0].function.arguments);
      if (args.location !== "Paris") {
        throw new Error(`unexpected tool arguments: ${JSON.stringify(args)}`);
      }
      return { toolCalls };
    },

    async models() {
      const list = await client.models.list(authedOpts);
      const ids = list.data.map((m) => m.id);
      if (!ids.includes(model)) {
        throw new Error(`expected model ${model} in list, got ${JSON.stringify(ids)}`);
      }
      const entry = list.data.find((m) => m.id === model);
      return { ids, supported_parameters: entry.supported_parameters };
    },

    async unauthorized() {
      // Broker-side note: ValidateSession (ctrl/request.go) returns plain
      // errors for every invalid-auth path, never wrapped with a 401 status,
      // so this currently surfaces as OpenAI.BadRequestError/400, not
      // AuthenticationError/401 the way a real OpenAI API would respond to a
      // bad API key. This scenario reports both checks so the Go test can
      // assert current behavior honestly instead of masking the gap.
      return expectError(
        () =>
          client.chat.completions.create(
            { model, messages: [{ role: "user", content: "Hi" }] },
            { timeout, headers: { Authorization: "Bearer app-sk-invalidtoken" } },
          ),
        (err) => ({
          isAuthError: err instanceof OpenAI.AuthenticationError,
          isBadRequest: err instanceof OpenAI.BadRequestError,
        }),
      );
    },

    async badrequest() {
      // A model that does not match the broker's configured model (or any of
      // its aliases) is rejected by ctrl.EnforceConfiguredModel before the
      // upstream is ever called — the same rejection TestChatbot_
      // ModelEnforcement exercises in the Go-only integration suite. (A
      // structurally-invalid field like messages:"not-an-array" is NOT
      // rejected: the broker's translation pipeline decodes bodies into a
      // generic map and never type-checks `messages`, so it would just
      // forward untouched and get a normal 200 back.)
      return expectError(
        () =>
          client.chat.completions.create(
            { model: `${model}-does-not-exist`, messages: [{ role: "user", content: "Hi" }] },
            authedOpts,
          ),
        (err) => ({ isBadRequest: err instanceof OpenAI.BadRequestError }),
      );
    },

    async ratelimit() {
      // The Go harness configures PerUserRPM=1/PerUserBurst=1 for this
      // scenario's env, so the first call consumes the only token and the
      // second is rejected.
      await client.chat.completions.create({ model, messages: [{ role: "user", content: "Hi" }] }, authedOpts);
      return expectError(
        () => client.chat.completions.create({ model, messages: [{ role: "user", content: "Hi" }] }, authedOpts),
        (err) => ({ isRateLimit: err instanceof OpenAI.RateLimitError }),
      );
    },

    async maxtokens() {
      const completion = await client.chat.completions.create(
        { model, messages: [{ role: "user", content: "Hi" }], max_completion_tokens: 123 },
        authedOpts,
      );
      return { debugReceivedBody: completion._debug_received_body };
    },

    async reasoning() {
      const completion = await client.chat.completions.create(
        { model, messages: [{ role: "user", content: "Hi" }], reasoning_effort: "high" },
        authedOpts,
      );
      return { debugReceivedBody: completion._debug_received_body };
    },

    async headers() {
      const { data, response } = await client.chat.completions
        .create({ model, messages: [{ role: "user", content: "Hi" }] }, authedOpts)
        .withResponse();
      const zgResKey = response.headers.get("zg-res-key");
      if (!zgResKey) {
        throw new Error("expected ZG-Res-Key header to be readable via the SDK's raw response");
      }
      return { zgResKey, id: data.id };
    },

    async imagegenerate() {
      const result = await client.images.generate(
        { model, prompt: "a cute cat playing piano", n: 1, size: "1024x1024" },
        authedOpts,
      );
      const urls = result.data.map((d) => d.url);
      if (!urls[0]) {
        throw new Error(`expected an image url, got ${JSON.stringify(result.data)}`);
      }
      return { urls };
    },

    async imageedit() {
      // client.images.edit sends multipart/form-data (unlike generate, which is
      // JSON) — OpenAI.toFile wraps the raw bytes as an Uploadable the SDK can
      // stream as a file part, mirroring how a real caller uploads an image.
      const image = await OpenAI.toFile(Buffer.from("fake-png-bytes"), "image.png", { type: "image/png" });
      const result = await client.images.edit({ model, image, prompt: "make it blue", n: 1 }, authedOpts);
      const urls = result.data.map((d) => d.url);
      if (!urls[0]) {
        throw new Error(`expected an edited image url, got ${JSON.stringify(result.data)}`);
      }
      return { urls };
    },

    async transcription() {
      const file = await OpenAI.toFile(Buffer.from("fake-wav-bytes"), "audio.wav", { type: "audio/wav" });
      const result = await client.audio.transcriptions.create({ model, file }, authedOpts);
      if (result.text !== "Hello world") {
        throw new Error(`unexpected transcription text: ${JSON.stringify(result.text)}`);
      }
      return { text: result.text };
    },
  };

  const fn = scenarios[scenario];
  if (!fn) {
    throw new Error(`unknown scenario: ${scenario} (known: ${Object.keys(scenarios).join(", ")})`);
  }
  const result = await fn();
  console.log(JSON.stringify({ ok: true, scenario, result }));
}

main().catch((err) => {
  console.log(
    JSON.stringify({
      ok: false,
      error: err && err.message,
      errorType: err && err.constructor && err.constructor.name,
      status: err && err.status,
    }),
  );
  process.exit(1);
});
