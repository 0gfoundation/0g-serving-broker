# SLLM-compatible API wrapper over vLLM.
#
# This image bakes sllm-wrapper.py so a TEE/dstack deployment does not
# require a host bind-mount of the Python script (issue #469). Build &
# publish as ghcr.io/0gfoundation/0g-sllm-wrapper:<tag> and reference it
# from docker-compose without any volumes.
FROM python:3.11-slim

WORKDIR /app
COPY sllm-wrapper.py /app/sllm-wrapper.py

ENV PORT=8343 \
    VLLM_BASE=http://vllm:8000 \
    LORA_HOST_PREFIX=/lora-modules/ \
    LORA_CONTAINER_PREFIX=/lora-modules/

EXPOSE 8343

# stdlib-only — no pip install needed.
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=5 \
    CMD python3 -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:'+__import__('os').environ.get('PORT','8343')+'/health')" || exit 1

CMD ["python3", "/app/sllm-wrapper.py"]
