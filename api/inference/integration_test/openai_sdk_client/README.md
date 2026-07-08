# OpenAI SDK contract test client

Real `openai` npm SDK client used by the Go `openaicontract` test suite
(`../openai_sdk_contract_test.go`), added for
[issue #577](https://github.com/0gfoundation/0g-serving-broker/issues/577).

The Go test starts a real TCP listener in front of the broker's in-process
engine plus a mocked OpenAI-shaped upstream, then execs `run.js` against it
once per scenario. Driving the broker with the actual SDK — not Go structs —
is the point: it exercises the real wire bytes (SSE framing, JSON shape,
header casing, SDK error typing) that the rest of the `integration` suite,
which calls `engine.ServeHTTP` in-process, never touches.

## Running locally

```bash
npm ci
cd ../../../ # api/
go test -tags openaicontract ./inference/integration_test/... -run TestOpenAISDK -v
```

Requires Docker (the harness backs the broker with a real MySQL
testcontainer, same as the `integration`-tagged suite) and Node on `PATH`.
The Go test skips itself with a clear message if `node_modules` hasn't been
installed here yet.

The `openai` SDK version is pinned exactly (no `^`) in `package.json` so an
upstream SDK release doesn't cause a false failure here — bump it
deliberately.
