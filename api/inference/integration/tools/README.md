# Provider tools

Standalone helpers for provider operators. These run against the **upstream**
model endpoint directly (the `service.targetUrl` you would configure), not
through the broker.

## `probe_model_capabilities.py`

Discovers a model's real capabilities so `service.modelInfo` does not have to
be guessed by hand: context length, which sampling parameters are honoured,
reasoning support, tool/function calling, JSON mode, streaming, and whether the
endpoint also speaks the Anthropic Messages format. Prints a ready-to-paste
`modelInfo` YAML snippet.

Standard library only — no `pip install` needed.

```bash
python3 probe_model_capabilities.py \
  --base-url https://your-host/v1 \
  --model meta-llama/Llama-3.1-8B-Instruct \
  --api-key "$UPSTREAM_API_KEY"     # optional, only if the upstream needs auth

# only the YAML snippet:
python3 probe_model_capabilities.py --base-url ... --model ... --quiet --yaml

# machine-readable:
python3 probe_model_capabilities.py --base-url ... --model ... --quiet --json
```

Flags can also be supplied via `PROBE_BASE_URL`, `PROBE_MODEL`, `PROBE_API_KEY`.
Add custom headers with repeatable `--header 'Key:Value'`; skip the Anthropic
probe with `--no-anthropic`.

### How to read the results

Each parameter is classified:

| Mark  | Meaning                                                                 |
|-------|-------------------------------------------------------------------------|
| `[+]` | **VERIFIED** — a crafted request produced the expected observable effect |
| `[~]` | **ACCEPTED** — server returned 200 but the effect could not be asserted   |
| `[-]` | **REJECTED** — server returned 4xx rejecting the field                    |
| `[?]` | **INCONCLUSIVE** — transport/server error, or an ambiguous 400            |

Important caveat: many OpenAI-compatible servers (vLLM especially) **accept
unknown parameters and silently ignore them**, returning 200. So `[~] ACCEPTED`
means "not rejected", *not* "definitely honoured". Sampling knobs
(`temperature`, `top_p`, `top_k`, penalties, `seed`, ...) whose effect is not
deterministically observable can only ever reach `ACCEPTED` or `REJECTED`. The
suggested `supportedParameters` list includes everything VERIFIED **or**
ACCEPTED, matching the OpenRouter `supported_parameters` convention — review it
before publishing.

The probe always reviews `TODO` placeholders it cannot determine (description,
modality for non-text models). Modality is assumed `text->text`; adjust it
manually for multimodal models.
