# GCP and AliCloud: integration removed, components retained

**Status:** the broker cannot select, construct or call either backend. The key
derivation that carried the reported vulnerability is deleted outright.
**Supported backends:** Phala/dstack (`NETWORK=phala`, the default) and Mock
(`NETWORK=hardhat`, local development only).

This note exists so a reviewer holding the external audit finding against
`api/common/tee/` can close it out without re-deriving why, and so that the code
still present in the tree is not mistaken for a live code path.

## The split

The vulnerability lived in one function per backend. The rest of each file is a
quote-fetching client with no defect, and for AliCloud the bulk of it is a
generated gRPC component that is expensive to recreate. So the two are treated
differently:

| | |
|---|---|
| **Deleted** | `AliCloudClient.DeriveKey`, `GcpTappdClient.DeriveKey` |
| **Deleted** | `GCP` and `AliCloud` in `tee.ClientType` |
| **Deleted** | the `NETWORK=gcp` / `NETWORK=alicloud` branches in all three entry points |
| **Deleted** | the `alicloud` TEE node in the compose generator — 20 template branches, its vllm image block, its `tee-key-data` volume, and the `TAPP_SERVICE_URL` / `TAPP_APP_ID` prompts and fields only it used |
| **Retained** | `AliCloudClient.TdxQuote` and `GcpTappdClient.TdxQuote` |
| **Retained** | `api/common/tee/alicloud/proto/` — the generated TAPP gRPC bindings |

Retaining the components means re-integrating either backend is a matter of
adding the wiring back and writing a correct derivation, not rewriting the gRPC
layer.

## Why the derivation had to go rather than be fixed in place

Both implementations took a `path` argument and **never read it**, which is the
one thing the E2EE design forbids. `api/common/tee/enckey.go` states the
requirement as a MUST: the enc key's derivation path must be distinct from the
signer's `"/"` so the two keys are independent (0g-pc SPEC §4.1).

- **AliCloud** returned a single cached secret from `/data/tee_key` for *every*
  path. The secp256k1 provider signer and the X25519 HPKE recipient key were
  therefore the **same secret**: disclosing either disclosed the other, and with
  it every prompt ever sealed to that enclave, including previously recorded
  traffic. Reported externally as CVSS 9.0.
- **GCP** minted a fresh `ecdsa.GenerateKey` on every call. The keys did not
  collide, but nothing was reproducible: a restart silently invalidated both the
  signer address published on chain and the `enc_pub` clients had already
  fetched, with no error raised anywhere.

Underneath both sat the reason a mitigation could not finish the job. **Neither
implementation bound its key to the enclave measurement** — the key was a plain
file. Anyone able to read that file (an operator, a volume snapshot, a backup, a
debug shell) could reproduce every derived key on an unattested machine and sign
as the attested identity there, and a relying verifier could not tell the
difference.

That contradicts premise **N10.2** in
[`attestation-trust-chain.md`](./attestation-trust-chain.md): the enc key must
"appear in the hardware-signed report" *and* actually belong to the verified
program. A file-backed key satisfies the first half and not the second — the
quote attests a key that is not, in fact, confined to the enclave.

Adding path separation (HKDF per path over the same file) would have closed the
reported key collapse and left that structural problem untouched. Deleting the
derivation ends both.

## Why this cannot be re-wired by accident

Removing `DeriveKey` means neither type satisfies `tee.TappdClient` any more.
That is deliberate: the compiler rejects any attempt to pass one where a client
is expected, so the backends cannot be reconnected without someone first writing
a derivation. There is no code path in the tree that can produce a
non-measurement-bound key.

## Stale configuration fails loudly

`NETWORK=gcp` and `NETWORK=alicloud` are **rejected by name** at startup in all
three entry points (`inference/cmd/server`, `inference/cmd/event`,
`fine-tuning/cmd/server`) rather than falling through to the `default` Phala
branch.

A deployment still carrying one of those values asked for a backend that no
longer exists; silently running it on a different one is how a configuration
mistake becomes an attestation nobody notices is wrong.

## What a reinstated backend must do

Do not reintroduce a file-backed key.

**AliCloud** already has what is needed, and the deleted implementation never
called it: the TAPP service exposes `GetAppKey` and `GetAppSecretKey`, carrying a
`kbs_resource_uri` and an `additional_data` binding field. A Key Broker Service
releases material only after verifying an attestation, and `additional_data` can
carry the derivation path for domain separation. The removed client used only
`GetEvidence` and `GetAppInfo`.

**GCP** would need an equivalent attestation-gated KMS release. TDX offers no
local sealing primitive, so nothing in `gcp.go` can supply this on its own.

In both cases the check to apply is the one the old code failed: two different
paths must yield material from which neither key can reconstruct the other, and
the material must be unobtainable to a machine that cannot present a valid quote.
