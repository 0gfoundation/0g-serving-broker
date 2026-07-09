# OpenAI SDK contract test client (video translator)

Real `openai` npm SDK client used by the Go `openaicontract` test suite
(`../openai_sdk_contract_test.go`), for
[issue #582](https://github.com/0gfoundation/0g-serving-broker/issues/582).

This is a separate package from
`api/inference/integration_test/openai_sdk_client` (the chat-completions
contract client), not a shared dependency: the official `openai` SDK only
added a `videos` resource in its 6.x line, while the chat contract client
intentionally pins the older 5.8.2 it was built against. Bumping that shared
client to 6.x to get video support would risk silently changing behavior for
the unrelated chat/models contract tests — not worth it for what is otherwise
an unrelated protocol surface.

The Go SDK (`github.com/openai/openai-go`, latest v1.12.0 as of writing) has
no video/Sora support at all yet, so a Go-native contract test isn't
possible today — this is why the client here is Node/JS like its chat
counterpart, not Go.

## Running locally

```bash
npm ci
cd ../../../ # api/
go test -tags openaicontract ./videotranslator/integration_test/... -v
```

The `openai` SDK version is pinned exactly (no `^`) so an upstream SDK
release doesn't cause a false failure here — bump it deliberately.
