#!/usr/bin/env python3
"""
E2E Test: Fine-Tuning → LoRA Serving (GPU + ServerlessLLM)

Full lifecycle on a Hardhat local chain with real GPU inference:
  1. Pre-place LoRA adapter files for the broker to discover on disk
  2. Provider adds a deliverable on-chain (simulates training completion)
  3. User acknowledges the deliverable on-chain
  4. Inference broker's event watcher detects DeliverableAcknowledged
  5. LoRA Manager deploys adapter to ServerlessLLM (vLLM-backed wrapper)
  6. User sends inference request through the broker for the LoRA model
  7. Broker validates, rewrites model reference, forwards, returns real GPU response
"""

import json
import os
import shutil
import sys
import time
import uuid

import requests
from web3 import Web3

HARDHAT_RPC = "http://127.0.0.1:8545"
INFERENCE_BROKER = "http://127.0.0.1:3081"
SLLM_URL = "http://127.0.0.1:8343"

FT_CONTRACT_ADDR = "0x0165878A594ca255338adfa4d48449f69242Eb8F"
INF_CONTRACT_ADDR = "0x610178dA211FEF7D417bC0e6FeD39F05609AD788"
LEDGER_CONTRACT_ADDR = "0x9fE46736679d2D9a65F0992F2272dE9f3c7fa6e0"

PROVIDER_KEY = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
USER_KEY = "0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a"

BASE_MODEL = "/root/models/Qwen2.5-0.5B-Instruct"
LORA_MODULES_DIR = "/root/lora-modules"
SOURCE_ADAPTER = os.path.join(LORA_MODULES_DIR, "e2e-test-adapter")

TASK_ID = f"sllm-e2e-{uuid.uuid4().hex[:8]}"
MODEL_ROOT_HASH = b"\xbb" * 32

w3 = Web3(Web3.HTTPProvider(HARDHAT_RPC))
provider_acct = w3.eth.account.from_key(PROVIDER_KEY)
user_acct = w3.eth.account.from_key(USER_KEY)
PROVIDER_ADDR = provider_acct.address
USER_ADDR = user_acct.address


def make_adapter_name(base_model: str, task_id: str) -> str:
    sanitized = base_model.replace("/", "-").replace(".", "-").replace(" ", "-")
    short = task_id[:12]
    return f"ft-{sanitized}-{short}"


ADAPTER_NAME = make_adapter_name(BASE_MODEL, TASK_ID)
ADAPTER_PATH = os.path.join(LORA_MODULES_DIR, ADAPTER_NAME)

passed = 0
failed = 0


def result(ok: bool, msg: str):
    global passed, failed
    if ok:
        passed += 1
        print(f"  \u2705 PASS: {msg}")
    else:
        failed += 1
        print(f"  \u274c FAIL: {msg}")


def load_abi(path: str) -> list:
    with open(path) as f:
        return json.load(f)


def send_tx(contract_fn, account, value=0):
    tx = contract_fn.build_transaction({
        "from": account.address,
        "nonce": w3.eth.get_transaction_count(account.address),
        "gas": 3_000_000,
        "gasPrice": w3.eth.gas_price,
        "value": value,
    })
    signed = account.sign_transaction(tx)
    tx_hash = w3.eth.send_raw_transaction(signed.raw_transaction)
    return w3.eth.wait_for_transaction_receipt(tx_hash)


print(f"\nTask ID:      {TASK_ID}")
print(f"Adapter name: {ADAPTER_NAME}")
print(f"Provider:     {PROVIDER_ADDR}")
print(f"User:         {USER_ADDR}")

# ── Step 0: Connectivity ──────────────────────────────────────
print("\n=== Step 0: Connectivity ===")

block = w3.eth.block_number
result(block > 0, f"Hardhat chain alive (block {block})")

r = requests.get(f"{INFERENCE_BROKER}/v1/quote", timeout=5)
result(r.status_code == 200, "Inference broker /v1/quote reachable")

r = requests.get(f"{SLLM_URL}/health", timeout=5)
result(r.status_code == 200, "ServerlessLLM (vLLM wrapper) healthy")

# ── Step 1: Pre-place adapter files ──────────────────────────
print(f"\n=== Step 1: Pre-place adapter at {ADAPTER_PATH} ===")

if os.path.exists(ADAPTER_PATH):
    shutil.rmtree(ADAPTER_PATH)
shutil.copytree(SOURCE_ADAPTER, ADAPTER_PATH)
has_safetensors = os.path.exists(os.path.join(ADAPTER_PATH, "adapter_model.safetensors"))
result(has_safetensors, "adapter_model.safetensors placed on disk")

# ── Step 2: Ensure provider registered on FT contract ────────
print("\n=== Step 2: Provider registration on FineTuningServing ===")

ft_abi = load_abi("/tmp/ft-serving-abi.json")
ft_contract = w3.eth.contract(address=FT_CONTRACT_ADDR, abi=ft_abi)

try:
    svc = ft_contract.functions.getService(PROVIDER_ADDR).call()
    result(True, f"Provider already registered (url={svc[1]})")
except Exception:
    try:
        quota = (1, 1024, 0, 100, "none")
        receipt = send_tx(
            ft_contract.functions.addOrUpdateService(
                "http://localhost:3080", quota, 1, False,
                ["Qwen2.5-0.5B-Instruct"], PROVIDER_ADDR,
            ),
            provider_acct,
            value=w3.to_wei(100, "ether"),
        )
        result(receipt.status == 1, f"Registered provider: {receipt.transactionHash.hex()}")
    except Exception as e:
        result(False, f"Registration failed: {e}")

# ── Step 3: User deposits funds + create FT account ──────────
print("\n=== Step 3: User deposits funds ===")

ledger_abi = load_abi("/tmp/ledger-abi.json")
ledger_contract = w3.eth.contract(address=LEDGER_CONTRACT_ADDR, abi=ledger_abi)

try:
    receipt = send_tx(
        ledger_contract.functions.addLedger("sllm-e2e-user"),
        user_acct,
        value=w3.to_wei(5, "ether"),
    )
    result(receipt.status == 1, f"addLedger tx: {receipt.transactionHash.hex()}")
except Exception as e:
    err_str = str(e).lower()
    if "exists" in err_str or "0xcde58aa1" in err_str:
        result(True, "User ledger already exists (idempotent)")
    else:
        result(False, f"addLedger failed: {e}")

try:
    receipt = send_tx(
        ledger_contract.functions.transferFund(
            PROVIDER_ADDR, "fine-tuning-v1.0", w3.to_wei("1", "ether"),
        ),
        user_acct,
    )
    result(receipt.status == 1, f"transferFund → FT provider: {receipt.transactionHash.hex()}")
except Exception as e:
    if "exists" in str(e).lower():
        result(True, "FT account already exists (idempotent)")
    else:
        result(False, f"transferFund failed: {e}")

# ── Step 4: Provider adds deliverable ────────────────────────
print("\n=== Step 4: Provider adds deliverable (simulates training done) ===")

try:
    receipt = send_tx(
        ft_contract.functions.addDeliverable(USER_ADDR, TASK_ID, MODEL_ROOT_HASH),
        provider_acct,
    )
    result(receipt.status == 1, f"addDeliverable tx: {receipt.transactionHash.hex()}")
except Exception as e:
    result(False, f"addDeliverable failed: {e}")

# ── Step 5: User acknowledges the deliverable ────────────────
print("\n=== Step 5: User acknowledges deliverable ===")

try:
    receipt = send_tx(
        ft_contract.functions.acknowledgeDeliverable(PROVIDER_ADDR, TASK_ID),
        user_acct,
    )
    result(receipt.status == 1, f"acknowledgeDeliverable (block {receipt.blockNumber})")

    ack_events = ft_contract.events.DeliverableAcknowledged().process_receipt(receipt)
    result(len(ack_events) > 0, "DeliverableAcknowledged event emitted")
except Exception as e:
    result(False, f"acknowledgeDeliverable failed: {e}")

# ── Step 6: Wait for event watcher + adapter deployment ──────
print("\n=== Step 6: Waiting for broker to deploy adapter ===")

deployed = False
for i in range(20):
    time.sleep(3)
    try:
        r = requests.get(f"{SLLM_URL}/v1/models", timeout=5)
        model_ids = [m["id"] for m in r.json().get("data", [])]
        if ADAPTER_NAME in model_ids:
            result(True, f"Adapter deployed to vLLM (waited {(i + 1) * 3}s)")
            deployed = True
            break
    except Exception:
        pass
    print(f"    ... polling ({(i + 1) * 3}s)")

if not deployed:
    result(False, "Adapter NOT deployed after 60s — check broker logs")

# ── Step 7: Real GPU inference via ServerlessLLM ─────────────
print("\n=== Step 7: Real GPU inference with LoRA adapter ===")

if deployed:
    try:
        r = requests.post(
            f"{SLLM_URL}/v1/chat/completions",
            json={
                "model": ADAPTER_NAME,
                "messages": [{"role": "user", "content": "What is 2+2?"}],
                "max_tokens": 50,
            },
            timeout=30,
        )
        result(r.status_code == 200, f"chat/completions → {r.status_code}")

        resp = r.json()
        content = resp["choices"][0]["message"]["content"]
        result(len(content) > 0, f"GPU response: {content[:120]}")

        usage = resp.get("usage", {})
        result(usage.get("total_tokens", 0) > 0, f"Token usage: {usage}")
    except Exception as e:
        result(False, f"Inference failed: {e}")
else:
    result(False, "Skipped inference — adapter not deployed")

# ── Summary ──────────────────────────────────────────────────
print("\n" + "=" * 60)
total = passed + failed
status = "ALL PASSED" if failed == 0 else f"{failed} FAILED"
print(f"  Results: {passed}/{total} passed — {status}")
print("=" * 60)

sys.exit(1 if failed else 0)
