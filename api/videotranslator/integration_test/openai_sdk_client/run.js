#!/usr/bin/env node

// Real OpenAI SDK contract client for the DashScope video translator
// (0gfoundation/0g-serving-broker#582).
//
// Invoked by the Go `openaicontract` test suite
// (../openai_sdk_contract_test.go) once per scenario. The Go test puts a real
// TCP listener in front of the translator's in-process gin engine (backed by
// a mocked DashScope-shaped upstream), then execs this script pointed at
// that listener — driving the translator through the actual `openai` npm
// package's videos resource, not hand-built HTTP requests, is the point: it
// proves the translator's output actually parses as a valid OpenAI Video
// object under the real client, not just "looks right" to a Go JSON decoder.
//
// Each scenario prints exactly one JSON line to stdout:
// {"ok":true,"scenario":...,"result":...} on success, or
// {"ok":false,"error":...} on failure (also a non-zero exit code).

const OpenAI = require("openai");

// expectError runs makeRequest(), expecting it to throw, and captures the
// SDK-visible error shape. err.error is the parsed {"code","message"} body
// the translator sends (see handler.writeDashScopeError); the SDK also
// lifts "code" onto the error object itself (see core/error.ts's APIError
// constructor), which is what a real caller would actually branch on.
async function expectError(makeRequest) {
  try {
    await makeRequest();
  } catch (err) {
    return {
      errorType: err.constructor.name,
      status: err.status,
      code: err.code,
      message: err.error && err.error.message,
    };
  }
  throw new Error("expected request to fail, but it succeeded");
}

async function main() {
  const scenario = process.env.SCENARIO;
  const baseURL = process.env.BASE_URL;
  const authHeader = process.env.AUTH_HEADER || "";
  const videoID = process.env.VIDEO_ID || "";
  const timeout = 30000;

  if (!baseURL) throw new Error("BASE_URL env var is required");

  // The translator does not itself validate credentials — apiKey is a dummy;
  // the real DashScope key lives in AUTH_HEADER, mirroring how the broker's
  // AdditionalSecret config injects it per-request (see #582's design).
  const client = new OpenAI({ baseURL, apiKey: "dummy", maxRetries: 0 });
  const authedOpts = { timeout, headers: authHeader ? { Authorization: authHeader } : {} };

  const scenarios = {
    async create() {
      const video = await client.videos.create(
        { model: "happyhorse", prompt: "a cat playing piano on stage", seconds: "4", size: "1280x720" },
        authedOpts,
      );
      if (video.object !== "video") {
        throw new Error(`unexpected object: ${video.object}`);
      }
      return { id: video.id, object: video.object, status: video.status, model: video.model, seconds: video.seconds, size: video.size };
    },

    async createWithSeed() {
      // "seed" has no field in the SDK's typed VideoCreateParams — this
      // exercises the SDK's own documented "undocumented request params"
      // escape hatch (see its README's "Making custom/undocumented
      // requests" section): the library doesn't validate at runtime that
      // the request matches the type, so this extra property is sent as-is
      // in the multipart body, same as a real caller would do with
      // `// @ts-expect-error` in TypeScript.
      const video = await client.videos.create(
        { model: "happyhorse", prompt: "a cat", seconds: "4", seed: 42 },
        authedOpts,
      );
      return { id: video.id, status: video.status };
    },

    async createExpectError() {
      // The Go test's mock DashScope server is configured to reject every
      // create call with a specific 4xx + code/message; this proves that
      // status/code/message survive translation and the real SDK classifies
      // it as the corresponding typed error (e.g. BadRequestError,
      // RateLimitError), not a generic failure.
      return expectError(() =>
        client.videos.create({ model: "happyhorse", prompt: "a cat", seconds: "4" }, authedOpts),
      );
    },

    async createThenRetrieve() {
      // The round trip a real client actually performs: create, then hand the id
      // the SDK gave you straight back to retrieve. The translator reshapes the
      // vendor's task id into the published contract, so this is what proves the
      // published id is usable — asserting the create id alone would not.
      const created = await client.videos.create(
        { model: "happyhorse", prompt: "a cat", seconds: "4" },
        authedOpts,
      );
      const fetched = await client.videos.retrieve(created.id, authedOpts);
      return { createdID: created.id, fetchedID: fetched.id, status: fetched.status };
    },

    async retrieve() {
      if (!videoID) throw new Error("VIDEO_ID env var is required for the retrieve scenario");
      const video = await client.videos.retrieve(videoID, authedOpts);
      // `usage` isn't part of the official Video type (it's a broker-only
      // billing extension the translator adds — see translate.FromGetTaskResponse),
      // so it's read as a plain field rather than a typed one. prompt/created_at/
      // expires_at ARE part of the official typed Video interface, unlike usage.
      const usage = video.usage || null;
      const error = video.error || null;
      return {
        id: video.id,
        status: video.status,
        usage,
        error,
        prompt: video.prompt,
        created_at: video.created_at,
        expires_at: video.expires_at,
      };
    },

    async downloadContent() {
      if (!videoID) throw new Error("VIDEO_ID env var is required for the downloadContent scenario");
      const response = await client.videos.downloadContent(videoID, {}, authedOpts);
      const buf = Buffer.from(await response.arrayBuffer());
      return { byteLength: buf.length, text: buf.toString("utf8"), contentType: response.headers.get("content-type") };
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
