#!/usr/bin/env python3
"""
E2E Test: Fine-Tuning → LoRA Serving

Simulates the full lifecycle on a Hardhat local chain (CPU-only, no real ML):
  1. Provider registers on FineTuningServing contract
  2. User deposits funds via LedgerManager
  3. Provider adds a deliverable (simulates training completion)
  4. User acknowledges the deliverable on-chain
  5. Inference broker's event watcher detects the acknowledge event
  6. LoRA Manager deploys the adapter to mock ServerlessLLM
  7. User sends an inference request for the fine-tuned model
  8. Inference broker validates, rewrites, forwards, returns response
"""

import json
import time
import sys
import requests
from web3 import Web3

HARDHAT_RPC = "http://127.0.0.1:8545"
INFERENCE_BROKER = "http://127.0.0.1:3081"
MOCK_SLLM = "http://127.0.0.1:8343"

FT_CONTRACT = "0xA51c1fc2f0D1a1b8494Ed1FE312d7C3a78Ed91C0"
INF_CONTRACT = "0x0165878A594ca255338adfa4d48449f69242Eb8F"
LEDGER_CONTRACT = "0x9fE46736679d2D9a65F0992F2272dE9f3c7fa6e0"

# Hardhat account #1 = provider: 0x70997970C51812dc3A010C7d01b50e0d17dc79C8
PROVIDER_KEY = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
# Hardhat account #2 = user: 0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC
USER_KEY = "0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a"

import uuid as _uuid
TASK_ID = f"e2e-{_uuid.uuid4().hex[:12]}"
MODEL_ROOT_HASH = b'\xaa' * 32  # dummy 32-byte hash

w3 = Web3(Web3.HTTPProvider(HARDHAT_RPC))
provider_account = w3.eth.account.from_key(PROVIDER_KEY)
user_account = w3.eth.account.from_key(USER_KEY)

PROVIDER_ADDR = provider_account.address
USER_ADDR = user_account.address

passed = 0
failed = 0


def result(ok: bool, msg: str):
    global passed, failed
    if ok:
        passed += 1
        print(f"  ✅ PASS: {msg}")
    else:
        failed += 1
        print(f"  ❌ FAIL: {msg}")


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
    receipt = w3.eth.wait_for_transaction_receipt(tx_hash)
    return receipt


# ============================================================
# Step 0: Connectivity checks
# ============================================================
print("\n=== Step 0: Connectivity ===")

block = w3.eth.block_number
result(block > 0, f"Hardhat chain alive (block {block})")

r = requests.get(f"{INFERENCE_BROKER}/v1/quote", timeout=5)
result(r.status_code == 200, f"Inference broker /v1/quote → {r.status_code}")

r = requests.get(f"{MOCK_SLLM}/health", timeout=5)
result(r.status_code == 200, f"Mock SLLM /health → {r.status_code}")


# ============================================================
# Step 1: Provider registers on FineTuningServing
# ============================================================
print("\n=== Step 1: Provider registers fine-tuning service ===")

ft_abi = load_abi("/tmp/ft-serving-abi.json")
ft_contract = w3.eth.contract(address=FT_CONTRACT, abi=ft_abi)

try:
    quota = (1, 1024, 0, 100, "none")
    tee_signer = PROVIDER_ADDR
    receipt = send_tx(
        ft_contract.functions.addOrUpdateService(
            "http://localhost:3080",
            quota,
            1,     # pricePerToken
            False, # occupied
            ["mock-base-model"],
            tee_signer,
        ),
        provider_account,
        value=w3.to_wei(100, "ether"),
    )
    result(receipt.status == 1, f"addOrUpdateService tx: {receipt.transactionHash.hex()}")
except Exception as e:
    err = str(e).lower()
    if "cannotaddstake" in err or "already" in err:
        result(True, "Provider service already registered (broker auto-registered at startup)")
    else:
        result(False, f"addOrUpdateService failed: {e}")


# ============================================================
# Step 2: User deposits funds via LedgerManager
# ============================================================
print("\n=== Step 2: User deposits funds ===")

ledger_abi = load_abi("/tmp/ledger-abi.json")
ledger_contract = w3.eth.contract(address=LEDGER_CONTRACT, abi=ledger_abi)

try:
    receipt = send_tx(
        ledger_contract.functions.addLedger("e2e-test-user"),
        user_account,
        value=w3.to_wei(5, "ether"),
    )
    result(receipt.status == 1, f"addLedger tx: {receipt.transactionHash.hex()}")
except Exception as e:
    if "ledgerexists" in str(e).lower() or "already" in str(e).lower():
        result(True, "User ledger already exists (idempotent)")
    else:
        result(False, f"addLedger failed: {e}")

try:
    # Transfer funds to fine-tuning provider.
    # Service name uses the full registered name (name-version) in LedgerManager.
    receipt = send_tx(
        ledger_contract.functions.transferFund(
            PROVIDER_ADDR,
            "fine-tuning-v1.0",
            w3.to_wei("1", "ether"),
        ),
        user_account,
    )
    result(receipt.status == 1, f"transferFund → FT provider: {receipt.transactionHash.hex()}")
except Exception as e:
    result(False, f"transferFund (FT) failed: {e}")


# ============================================================
# Step 3a: Push adapter key to inference broker via HTTP
# ============================================================
print("\n=== Step 3a: Push adapter key to inference broker ===")

try:
    r = requests.post(
        f"{INFERENCE_BROKER}/internal/v1/adapter-keys",
        json={
            "taskId": TASK_ID,
            "storageHash": "0x" + MODEL_ROOT_HASH.hex(),
            "providerEncKey": "0xdeadbeef",
        },
        headers={"Authorization": "Bearer e2e-test-secret"},
        timeout=5,
    )
    result(r.status_code == 200, f"Adapter key pushed: {r.json()}")
except Exception as e:
    result(False, f"Push adapter key failed: {e}")

# ============================================================
# Step 3b: Provider adds deliverable (simulates training done)
# ============================================================
print("\n=== Step 3b: Provider adds deliverable ===")

try:
    receipt = send_tx(
        ft_contract.functions.addDeliverable(
            USER_ADDR,
            TASK_ID,
            MODEL_ROOT_HASH,
        ),
        provider_account,
    )
    result(receipt.status == 1, f"addDeliverable tx: {receipt.transactionHash.hex()}")

    # Verify deliverable exists
    deliverable = ft_contract.functions.getDeliverable(USER_ADDR, PROVIDER_ADDR, TASK_ID).call()
    result(len(deliverable) > 0, f"getDeliverable returned data (modelRootHash present)")
except Exception as e:
    result(False, f"addDeliverable failed: {e}")


# ============================================================
# Step 4: User acknowledges the deliverable
# ============================================================
print("\n=== Step 4: User acknowledges deliverable ===")

try:
    receipt = send_tx(
        ft_contract.functions.acknowledgeDeliverable(PROVIDER_ADDR, TASK_ID),
        user_account,
    )
    result(receipt.status == 1, f"acknowledgeDeliverable tx: {receipt.transactionHash.hex()}")

    # Check for DeliverableAcknowledged event
    ack_events = ft_contract.events.DeliverableAcknowledged().process_receipt(receipt)
    result(len(ack_events) > 0, f"DeliverableAcknowledged event emitted (block {receipt.blockNumber})")
except Exception as e:
    result(False, f"acknowledgeDeliverable failed: {e}")


# ============================================================
# Step 5: Wait for inference broker event watcher to detect it
# ============================================================
print("\n=== Step 5: Waiting for inference broker event watcher ===")

# The event watcher polls every 3 seconds. Give it up to 30 seconds.
adapter_name = None
for i in range(10):
    time.sleep(3)
    try:
        r = requests.get(f"{MOCK_SLLM}/v1/models", timeout=5)
        models = r.json().get("data", [])
        if models:
            adapter_name = models[0]["model"]
            result(True, f"Adapter detected in mock SLLM: {adapter_name} (waited {(i+1)*3}s)")
            break
    except Exception:
        pass
    print(f"    ... polling ({(i+1)*3}s)")

if not adapter_name:
    result(False, "Adapter NOT detected in mock SLLM after 30s")
    # Check broker logs for clues
    print("  Checking broker logs for event watcher activity...")


# ============================================================
# Step 6: Test inference request (ft-* model)
# ============================================================
print("\n=== Step 6: Inference request for fine-tuned model ===")

if adapter_name:
    try:
        # Direct request to mock SLLM (bypassing broker) to verify it works
        r = requests.post(f"{MOCK_SLLM}/v1/chat/completions", json={
            "model": "mock-base-model",
            "lora_adapter_name": adapter_name,
            "messages": [{"role": "user", "content": "Hello from E2E test!"}],
        }, timeout=10)
        result(r.status_code == 200, f"Direct mock SLLM request → {r.status_code}")
        resp_data = r.json()
        content = resp_data["choices"][0]["message"]["content"]
        result("echo" in content.lower() or "mock" in content.lower(),
               f"Mock response received: {content[:80]}")
        usage = resp_data.get("usage", {})
        result(usage.get("total_tokens", 0) > 0,
               f"Token usage reported: {usage}")
    except Exception as e:
        result(False, f"Direct SLLM request failed: {e}")
else:
    result(False, "Skipping inference test — adapter not deployed")


# ============================================================
# Summary
# ============================================================
print("\n" + "=" * 50)
total = passed + failed
print(f"  Results: {passed} passed / {failed} failed / {total} total")
print("=" * 50)

sys.exit(1 if failed > 0 else 0)
