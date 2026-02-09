FROM python:3.11.2-slim-buster

# Install Python packages
# - huggingface_hub: provides huggingface-cli for model downloads (used by setup.go)
# - transformers/datasets: ML libraries
# - nv_attestation_sdk/nv-local-gpu-verifier: NVIDIA TEE attestation
RUN pip install --no-cache-dir --upgrade pip && \
    pip install --no-cache-dir huggingface_hub transformers datasets nv_attestation_sdk nv-local-gpu-verifier
