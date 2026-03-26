#!/usr/bin/env python3
"""Deploy FineTuningServing, InferenceServing, LedgerManager to 0G testnet via BeaconProxy."""

import json
import sys
from web3 import Web3

RPC = "https://evmrpc-testnet.0g.ai"
CHAIN_ID = 16602
DEPLOYER_KEY = "ec5b24ecf97b9a5a80cdf014f86c4a582fd16cc238fb83faa4515e63eb4baa61"

ARTIFACTS_DIR = "api/libs/0g-serving-contract/deployments/zgTestnetV4"
PROXY_ARTIFACTS = "/tmp/proxy_artifacts.json"

w3 = Web3(Web3.HTTPProvider(RPC))
deployer = w3.eth.account.from_key(DEPLOYER_KEY)
print(f"Deployer: {deployer.address}")
print(f"Balance:  {w3.from_wei(w3.eth.get_balance(deployer.address), 'ether'):.4f} A0GI")
print(f"Chain ID: {w3.eth.chain_id}")

with open(PROXY_ARTIFACTS) as f:
    proxy_arts = json.load(f)

UB_ABI = proxy_arts["ub_abi"]
UB_BYTECODE = proxy_arts["ub_bytecode"]
BP_ABI = proxy_arts["bp_abi"]
BP_BYTECODE = proxy_arts["bp_bytecode"]


def load_artifact(filename):
    with open(f"{ARTIFACTS_DIR}/{filename}") as f:
        d = json.load(f)
    return d["abi"], d["bytecode"]


def deploy_raw(name, abi, bytecode, constructor_args=None):
    contract = w3.eth.contract(abi=abi, bytecode=bytecode)
    tx = contract.constructor(*(constructor_args or [])).build_transaction({
        "from": deployer.address,
        "nonce": w3.eth.get_transaction_count(deployer.address),
        "gas": 8_000_000,
        "gasPrice": w3.eth.gas_price,
        "chainId": CHAIN_ID,
    })
    signed = deployer.sign_transaction(tx)
    tx_hash = w3.eth.send_raw_transaction(signed.raw_transaction)
    receipt = w3.eth.wait_for_transaction_receipt(tx_hash, timeout=120)
    if receipt.status != 1:
        print(f"  {name} deploy FAILED! tx: {tx_hash.hex()}")
        sys.exit(1)
    addr = receipt.contractAddress
    print(f"  {name} -> {addr}")
    return addr


def deploy_beacon_proxy(label, impl_abi, impl_bytecode):
    """Deploy: Impl -> UpgradeableBeacon -> BeaconProxy, return (proxy_addr, impl_abi)."""
    print(f"\n=== {label} ===")
    impl_addr = deploy_raw(f"{label}Impl", impl_abi, impl_bytecode)
    beacon_addr = deploy_raw(f"{label}Beacon", UB_ABI, UB_BYTECODE, [impl_addr])
    proxy_addr = deploy_raw(f"{label}Proxy", BP_ABI, BP_BYTECODE, [beacon_addr, b""])
    return proxy_addr, impl_abi


def send_tx(contract, fn_name, *args, value=0):
    fn = getattr(contract.functions, fn_name)(*args)
    tx = fn.build_transaction({
        "from": deployer.address,
        "nonce": w3.eth.get_transaction_count(deployer.address),
        "gas": 3_000_000,
        "gasPrice": w3.eth.gas_price,
        "chainId": CHAIN_ID,
        "value": value,
    })
    signed = deployer.sign_transaction(tx)
    tx_hash = w3.eth.send_raw_transaction(signed.raw_transaction)
    receipt = w3.eth.wait_for_transaction_receipt(tx_hash, timeout=120)
    if receipt.status != 1:
        print(f"  {fn_name} FAILED! tx: {tx_hash.hex()}")
        sys.exit(1)
    print(f"  {fn_name} OK")
    return receipt


ledger_abi, ledger_bc = load_artifact("LedgerManagerImpl.json")
ft_abi, ft_bc = load_artifact("FineTuningServing_v1.1Impl.json")
inf_abi, inf_bc = load_artifact("InferenceServing_v1.0Impl.json")

ledger_addr, _ = deploy_beacon_proxy("LedgerManager", ledger_abi, ledger_bc)
ft_addr, _ = deploy_beacon_proxy("FineTuningServing", ft_abi, ft_bc)
inf_addr, _ = deploy_beacon_proxy("InferenceServing", inf_abi, inf_bc)

ledger = w3.eth.contract(address=ledger_addr, abi=ledger_abi)
ft = w3.eth.contract(address=ft_addr, abi=ft_abi)
inf = w3.eth.contract(address=inf_addr, abi=inf_abi)

print("\n=== Initialize ===")
send_tx(ledger, "initialize", deployer.address)
send_tx(ft, "initialize", 86400, ledger_addr, deployer.address, 30)
send_tx(inf, "initialize", 86400, ledger_addr, deployer.address)

print("\n=== Register services ===")
send_tx(ledger, "registerService", "fine-tuning", "v1.0", ft_addr, "Fine-tuning serving")
send_tx(ledger, "registerService", "inference", "v1.0", inf_addr, "Inference serving")
send_tx(ledger, "setRecommendedService", "fine-tuning", "v1.0")
send_tx(ledger, "setRecommendedService", "inference", "v1.0")

print()
print("=" * 60)
print("DEPLOYED TO 0G TESTNET (chain 16602)")
print("=" * 60)
print(f"LEDGER_MANAGER = {ledger_addr}")
print(f"FT_SERVING     = {ft_addr}")
print(f"INF_SERVING    = {inf_addr}")
print(f"DEPLOYER/OWNER = {deployer.address}")
print("=" * 60)
