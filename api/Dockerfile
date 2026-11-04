FROM python:3.11.2-slim-buster

# Install Python packages
# - huggingface_hub: provides hf CLI for model downloads (used by setup.go)
# - transformers/datasets: ML libraries
# - nv_attestation_sdk/nv-local-gpu-verifier: NVIDIA TEE attestation
#   Pinned to what the B300 rehearsal verified (2026-11-04). nvtrust writes the GPU
#   architecture into each evidence item, which common/tee/parseGpuPayload copies to the
#   payload's top-level arch; a release that drops or renames it would silently send
#   HOPPER again and NRAS would reject Blackwell evidence. Bump deliberately, and
#   re-check a Blackwell payload against NRAS when you do.
RUN pip install --no-cache-dir --upgrade pip && \
    pip install --no-cache-dir huggingface_hub==2.4.0 transformers==5.22.1 datasets==5.1.0 nv_attestation_sdk==2.7.3 nv-local-gpu-verifier==2.7.3
