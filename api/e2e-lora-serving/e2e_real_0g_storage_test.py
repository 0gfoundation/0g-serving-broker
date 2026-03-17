#!/usr/bin/env python3
"""
Real E2E Test: 0G Storage LoRA Adapter Download → vLLM GPU Inference

Full lifecycle with NO MOCKING:
  1. Prepare a real LoRA adapter (from a local tarball or directory)
  2. AES-GCM encrypt the adapter ZIP (matching Go broker format exactly)
  3. Upload the encrypted file to real 0G Storage testnet
  4. ECIES-encrypt the AES key with the provider's secp256k1 public key
  5. Push the encrypted key to the inference broker's HTTP API
  6. Emit addDeliverable + acknowledgeDeliverable on-chain
  7. Broker's event watcher detects the event, downloads from 0G Storage,
     decrypts (ECIES → AES-GCM), unzips, and loads adapter into vLLM
  8. Send a real GPU inference request through the deployed LoRA adapter

Prerequisites:
  - Remote CVM running: vLLM, sllm-wrapper, MySQL, inference broker (mockDeploy=false)
  - Local machine: Python 3.10+, 0g-storage-client binary, LoRA adapter files
  - 0G testnet provider wallet with gas balance
  - Provider registered on FineTuningServing contract
  - ABI file for FineTuningServing contract

Configuration:
  All parameters read from environment variables. Copy .env.example → .env
  and edit, then: set -a && source .env && set +a && python3 e2e_real_0g_storage_test.py

See README.md for full setup instructions.
"""

import json
import os
import re
import secrets
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import uuid
import zipfile

# ---------------------------------------------------------------------------
# Optional imports — check early so the user gets a clear error
# ---------------------------------------------------------------------------
_missing = []
try:
    import requests
except ImportError:
    _missing.append("requests")
try:
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM
except ImportError:
    _missing.append("cryptography")
try:
    from ecies import encrypt as ecies_encrypt
except ImportError:
    _missing.append("eciespy")
try:
    from eth_keys import keys
except ImportError:
    _missing.append("eth_keys")
try:
    from web3 import Web3
except ImportError:
    _missing.append("web3")

if _missing:
    print(f"Missing Python packages: {', '.join(_missing)}")
    print(f"Install with: pip install {' '.join(_missing)}")
    sys.exit(1)


# ============================================================
# Configuration (all from environment variables)
# ============================================================
def env(name, default=None, required=False):
    val = os.environ.get(name, default)
    if required and not val:
        print(f"ERROR: Required environment variable {name} is not set.")
        print(f"Copy .env.example → .env, edit it, then: set -a && source .env && set +a")
        sys.exit(1)
    return val


# -- Chain --
RPC_URL = env("RPC_URL", "https://evmrpc-testnet.0g.ai")
CHAIN_ID = int(env("CHAIN_ID", "16602"))
PROVIDER_PRIVATE_KEY = env("PROVIDER_PRIVATE_KEY", required=True)
FT_CONTRACT_ADDRESS = env("FT_CONTRACT_ADDRESS", required=True)
FT_ABI_PATH = env("FT_ABI_PATH", required=True)

# -- Remote CVM --
CVM_HASH = env("CVM_HASH", required=True)
CVM_BROKER_PORT = env("CVM_BROKER_PORT", "3081")
CVM_GATEWAY = env("CVM_GATEWAY", "dstack-pha-in2.phala.network")
CVM_BROKER_URL = env("CVM_BROKER_URL",
                      f"https://{CVM_HASH}-{CVM_BROKER_PORT}.{CVM_GATEWAY}")
CVM_SLLM_PORT = env("CVM_SLLM_PORT", "8343")
CVM_VLLM_PORT = env("CVM_VLLM_PORT", "8000")

# -- 0G Storage --
STORAGE_CLIENT_PATH = env("STORAGE_CLIENT_PATH", required=True)
STORAGE_INDEXER_URL = env("STORAGE_INDEXER_URL",
                          "https://indexer-storage-testnet-turbo.0g.ai")

# -- Adapter source (provide ONE of these) --
ADAPTER_TARBALL = env("ADAPTER_TARBALL")         # path to .tar.gz with adapter files
ADAPTER_DIR = env("ADAPTER_DIR")                  # path to directory with adapter files

# -- Test parameters --
TASK_ID = env("TASK_ID", f"e2e-{uuid.uuid4().hex[:8]}")
POLL_INTERVAL = int(env("POLL_INTERVAL", "5"))
POLL_TIMEOUT = int(env("POLL_TIMEOUT", "300"))

# -- Derived --
AES_KEY = secrets.token_bytes(32)
CHUNK_SIZE = 64 * 1024 * 1024  # must match Go defaultBufferSize


# ============================================================
# Helpers
# ============================================================
w3 = Web3(Web3.HTTPProvider(RPC_URL))
provider_acct = w3.eth.account.from_key(PROVIDER_PRIVATE_KEY)
PROVIDER_ADDR = provider_acct.address

passed = 0
failed = 0


def result(ok, msg):
    global passed, failed
    if ok:
        passed += 1
        print(f"  \u2705 PASS: {msg}")
    else:
        failed += 1
        print(f"  \u274c FAIL: {msg}")
    return ok


def send_tx(fn, value=0):
    """Build, sign, send, and wait for a transaction."""
    tx = fn.build_transaction({
        "from": provider_acct.address,
        "nonce": w3.eth.get_transaction_count(provider_acct.address),
        "gas": 3_000_000,
        "gasPrice": w3.eth.gas_price,
        "value": value,
        "chainId": CHAIN_ID,
    })
    signed = provider_acct.sign_transaction(tx)
    tx_hash = w3.eth.send_raw_transaction(signed.raw_transaction)
    return w3.eth.wait_for_transaction_receipt(tx_hash, timeout=120)


def ssh_cmd(cmd, timeout=20):
    """Run a command on the CVM via phala ssh."""
    proc = subprocess.run(
        ["bash", "-c",
         f"echo '{cmd}' | timeout {timeout} phala ssh {CVM_HASH}"],
        capture_output=True, text=True, timeout=timeout + 10,
    )
    lines = [l for l in proc.stdout.split("\n")
             if not l.startswith(("Pseudo-terminal", "Warning:", "\u2713 Connection"))]
    return "\n".join(lines).strip()


def aes_encrypt_large_file(key, input_path, output_path):
    """
    AES-GCM chunked encryption matching Go broker's AesEncryptLargeFile:
      [65-byte zero signature][12-byte nonce][chunk1 ciphertext+tag][chunk2 ...]
    Each chunk is up to CHUNK_SIZE (64 MiB) of plaintext.
    The nonce is incremented (big-endian) after each chunk.
    """
    aesgcm = AESGCM(key)
    nonce = secrets.token_bytes(12)

    def inc_nonce(n):
        ba = bytearray(n)
        for i in range(len(ba) - 1, -1, -1):
            ba[i] = (ba[i] + 1) & 0xFF
            if ba[i] != 0:
                break
        return bytes(ba)

    with open(input_path, "rb") as fin, open(output_path, "wb") as fout:
        fout.write(b"\x00" * 65)  # 65-byte signature placeholder
        fout.write(nonce)
        current_nonce = nonce
        while True:
            chunk = fin.read(CHUNK_SIZE)
            if not chunk:
                break
            fout.write(aesgcm.encrypt(current_nonce, chunk, None))
            current_nonce = inc_nonce(current_nonce)


# ============================================================
# Validate config
# ============================================================
if not ADAPTER_TARBALL and not ADAPTER_DIR:
    print("ERROR: Set ADAPTER_TARBALL or ADAPTER_DIR to point at your LoRA adapter.")
    sys.exit(1)

if not os.path.exists(STORAGE_CLIENT_PATH):
    print(f"ERROR: 0g-storage-client not found at {STORAGE_CLIENT_PATH}")
    sys.exit(1)

if not os.path.exists(FT_ABI_PATH):
    print(f"ERROR: FineTuningServing ABI not found at {FT_ABI_PATH}")
    sys.exit(1)


# ============================================================
print(f"\n{'='*64}")
print(f"  REAL E2E TEST — 0G Storage LoRA Adapter → vLLM GPU Inference")
print(f"{'='*64}")
print(f"  Task ID   : {TASK_ID}")
print(f"  Provider  : {PROVIDER_ADDR}")
print(f"  Chain     : {RPC_URL} (chainId={CHAIN_ID})")
print(f"  CVM       : {CVM_HASH[:12]}...")
print(f"  Broker    : {CVM_BROKER_URL}")
print(f"  Storage   : {STORAGE_INDEXER_URL}")
print(f"{'='*64}")


# ============================================================
# Step 0: Connectivity
# ============================================================
print("\n── Step 0: Connectivity ──")

block = w3.eth.block_number
result(True, f"Chain alive (block={block})")

try:
    r = requests.get(f"{CVM_BROKER_URL}/v1/quote", timeout=10)
    result(r.status_code == 200, "Inference broker alive")
except Exception as e:
    result(False, f"Broker unreachable: {e}")

out = ssh_cmd(f"curl -sf http://127.0.0.1:{CVM_VLLM_PORT}/health && echo OK || echo FAIL")
result("OK" in out, "vLLM alive on CVM")


# ============================================================
# Step 1: Prepare real LoRA adapter
# ============================================================
print("\n── Step 1: Prepare LoRA adapter ──")

tmpdir = tempfile.mkdtemp(prefix="e2e-real-")

if ADAPTER_TARBALL:
    with tarfile.open(ADAPTER_TARBALL, "r:gz") as tf:
        tf.extractall(tmpdir)
elif ADAPTER_DIR:
    for fname in os.listdir(ADAPTER_DIR):
        src = os.path.join(ADAPTER_DIR, fname)
        if os.path.isfile(src):
            shutil.copy2(src, os.path.join(tmpdir, fname))

adapter_config = os.path.join(tmpdir, "adapter_config.json")
adapter_weights = os.path.join(tmpdir, "adapter_model.safetensors")

if not os.path.exists(adapter_config) or not os.path.exists(adapter_weights):
    result(False, f"adapter_config.json / adapter_model.safetensors not found in source")
    sys.exit(1)

cfg_size = os.path.getsize(adapter_config)
wt_size = os.path.getsize(adapter_weights)
result(wt_size > 100, f"adapter_config.json={cfg_size}B  safetensors={wt_size}B")

with open(adapter_config) as f:
    acfg = json.load(f)
result(acfg.get("peft_type") == "LORA",
       f"PEFT type={acfg.get('peft_type')}, r={acfg.get('r')}")

# Create ZIP (files under top-level "adapter/" directory, matching broker unzip convention)
zip_path = os.path.join(tmpdir, "adapter.zip")
with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as zf:
    zf.write(adapter_config, "adapter/adapter_config.json")
    zf.write(adapter_weights, "adapter/adapter_model.safetensors")

result(os.path.getsize(zip_path) > 0,
       f"ZIP created: {os.path.getsize(zip_path)} bytes")


# ============================================================
# Step 2: AES-GCM encrypt
# ============================================================
print("\n── Step 2: AES-GCM encrypt (Go-compatible format) ──")

encrypted_path = os.path.join(tmpdir, "adapter_encrypted.bin")
aes_encrypt_large_file(AES_KEY, zip_path, encrypted_path)
enc_size = os.path.getsize(encrypted_path)
result(enc_size > os.path.getsize(zip_path), f"Encrypted: {enc_size} bytes")


# ============================================================
# Step 3: Upload to 0G Storage
# ============================================================
print("\n── Step 3: Upload to 0G Storage ──")

cmd = [
    STORAGE_CLIENT_PATH, "upload",
    "--file", encrypted_path,
    "--indexer", STORAGE_INDEXER_URL,
    "--url", RPC_URL,
    "--key", PROVIDER_PRIVATE_KEY,
    "--fast-mode",
]
print(f"  Uploading {enc_size} bytes ...")
proc = subprocess.run(cmd, capture_output=True, text=True, timeout=300)

root_hash = None
for line in (proc.stdout + proc.stderr).split("\n"):
    m = re.search(r"0x[a-fA-F0-9]{64}", line)
    if m and "root" in line.lower():
        root_hash = m.group(0)
        break

if not result(root_hash is not None, f"Storage root = {root_hash}"):
    print(f"  stdout: {proc.stdout[-300:]}")
    print(f"  stderr: {proc.stderr[-300:]}")
    sys.exit(1)


# ============================================================
# Step 4: ECIES encrypt AES key
# ============================================================
print("\n── Step 4: ECIES encrypt AES key ──")

pk = keys.PrivateKey(bytes.fromhex(PROVIDER_PRIVATE_KEY.replace("0x", "")))
pub_bytes = pk.public_key.to_bytes()
provider_enc_key = ecies_encrypt(pub_bytes, AES_KEY)
result(True, f"ECIES encrypted: {len(provider_enc_key)} bytes")


# ============================================================
# Step 5: Push adapter key
# ============================================================
print("\n── Step 5: Push adapter key to broker ──")

storage_hash_no_prefix = root_hash[2:]
r = requests.post(
    f"{CVM_BROKER_URL}/internal/v1/adapter-keys",
    json={
        "taskId": TASK_ID,
        "storageHash": storage_hash_no_prefix,
        "providerEncKey": provider_enc_key.hex(),
    },
    timeout=15,
)
result(r.status_code == 200, f"Key pushed: {r.json()}")


# ============================================================
# Step 6: On-chain events
# ============================================================
print("\n── Step 6a: addDeliverable (on-chain) ──")

with open(FT_ABI_PATH) as f:
    ft_abi = json.load(f)
ft = w3.eth.contract(address=FT_CONTRACT_ADDRESS, abi=ft_abi)

hash_bytes = bytes.fromhex(storage_hash_no_prefix)
receipt = send_tx(ft.functions.addDeliverable(PROVIDER_ADDR, TASK_ID, hash_bytes))
result(receipt.status == 1, f"addDeliverable block={receipt.blockNumber}")

print("\n── Step 6b: acknowledgeDeliverable (on-chain) ──")
receipt = send_tx(ft.functions.acknowledgeDeliverable(PROVIDER_ADDR, TASK_ID))
result(receipt.status == 1, f"acknowledgeDeliverable block={receipt.blockNumber}")
ack_block = receipt.blockNumber


# ============================================================
# Step 7: Wait for download + deploy
# ============================================================
print("\n── Step 7: Wait for broker to download, decrypt, deploy ──")

expected_prefix = TASK_ID[:12]
print(f"  Waiting for adapter containing '{expected_prefix}' in vLLM model list ...")

deployed = False
deployed_model = ""
deadline = time.time() + POLL_TIMEOUT

while time.time() < deadline:
    time.sleep(POLL_INTERVAL)
    elapsed = int(time.time() + POLL_TIMEOUT - deadline)
    try:
        out = ssh_cmd(
            f"curl -sf http://127.0.0.1:{CVM_SLLM_PORT}/v1/models 2>/dev/null || echo FAIL")
        if out and out != "FAIL":
            data = json.loads(out)
            for m in data.get("data", []):
                mid = m.get("id", "")
                if expected_prefix in mid:
                    deployed_model = mid
                    deployed = True
                    break
        if deployed:
            result(True, f"Adapter live in vLLM: {deployed_model} ({elapsed}s)")
            break
    except Exception:
        pass

    if elapsed % 30 == 0:
        print(f"    ... {elapsed}s elapsed")

if not deployed:
    result(False, f"Adapter NOT in vLLM after {POLL_TIMEOUT}s")
    print("\n  ── Broker server log (last lines with our task) ──")
    out = ssh_cmd(
        f"grep '{TASK_ID[:8]}' /tmp/inference-server.log 2>/dev/null | tail -20")
    print(f"  {out}")
    print("\n  ── SLLM wrapper log ──")
    out = ssh_cmd("docker logs sllm-wrapper --tail 10 2>&1")
    print(f"  {out}")


# ============================================================
# Step 8: Real GPU inference
# ============================================================
if deployed:
    print(f"\n── Step 8: Real GPU inference with LoRA adapter ──")
    try:
        out = ssh_cmd(
            'curl -sf -X POST http://127.0.0.1:'
            + CVM_SLLM_PORT
            + '/v1/chat/completions '
              '-H "Content-Type: application/json" '
              '-d "{\\"model\\":\\"' + deployed_model
            + '\\",\\"messages\\":[{\\"role\\":\\"user\\",\\"content\\":\\"What is 2+2? Answer briefly.\\"}],\\"max_tokens\\":50}"',
            timeout=60,
        )
        json_line = None
        for line in out.split("\n"):
            line = line.strip()
            if line.startswith("{") and "choices" in line:
                json_line = line
                break
        if json_line is None:
            result(False, f"No valid JSON in response: {out[:200]}")
        else:
            resp = json.loads(json_line)
            content = resp["choices"][0]["message"]["content"]
            usage = resp.get("usage", {})
            result(len(content) > 0, f"Response: {content[:120]}")
            result(usage.get("total_tokens", 0) > 0,
                   f"Tokens: prompt={usage.get('prompt_tokens')}, "
                   f"completion={usage.get('completion_tokens')}, "
                   f"total={usage.get('total_tokens')}")
    except Exception as e:
        result(False, f"Inference error: {e}")


# ============================================================
# Summary
# ============================================================
print(f"\n{'='*64}")
total = passed + failed
status = "ALL PASSED \u2705" if failed == 0 else f"{failed} FAILED \u274c"
print(f"  Results     : {passed}/{total} — {status}")
print(f"  Task ID     : {TASK_ID}")
if deployed:
    print(f"  vLLM model  : {deployed_model}")
print(f"  Storage hash: {root_hash}")
print(f"  Ack block   : {ack_block}")
print(f"{'='*64}")

shutil.rmtree(tmpdir, ignore_errors=True)
sys.exit(1 if failed else 0)
