import sys
import json

import argparse
from verifier import cc_admin

GPU_ARCH = "HOPPER"


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Collect GPU Evidence")

    parser.add_argument(
        "--public_key", help="Hex string representation of Nonce", required=True
    )
    parser.add_argument(
        "--no_gpu_mode",
        help=" Represents if the function should run in No GPU (test) mode",
        action="store_true",
    )

    args = parser.parse_args()
    gpu_evidence_list = cc_admin.collect_gpu_evidence_remote(
        args.public_key, args.no_gpu_mode
    )
    data = {
        "nonce": args.public_key,
        "evidence_list": gpu_evidence_list,
        "arch": GPU_ARCH,
    }

    print(json.dumps(data))
