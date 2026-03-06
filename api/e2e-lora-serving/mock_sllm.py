"""
Mock ServerlessLLM Server for E2E Testing (CPU-only).

Implements the minimum ServerlessLLM API surface needed by the inference broker's
LoRA Manager + SLLMClient, plus an OpenAI-compatible /v1/chat/completions endpoint.
"""

import json
import time
import uuid
import logging
from http.server import HTTPServer, BaseHTTPRequestHandler
from threading import Lock

logging.basicConfig(level=logging.INFO, format="%(asctime)s [mock-sllm] %(message)s")
log = logging.getLogger("mock-sllm")

_adapters: dict[str, dict] = {}
_lock = Lock()


class SLLMHandler(BaseHTTPRequestHandler):

    def _json(self, code: int, body: dict):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _read_body(self) -> dict:
        length = int(self.headers.get("Content-Length", 0))
        if length == 0:
            return {}
        return json.loads(self.rfile.read(length))

    # --- Health ---
    def _handle_health(self):
        self._json(200, {"status": "ok"})

    # --- POST /v1/models/deploy ---
    def _handle_deploy(self):
        body = self._read_body()
        adapters = body.get("lora_adapters", {})
        with _lock:
            for name, path in adapters.items():
                _adapters[name] = {"model": body.get("model", "unknown"), "path": path, "status": "active"}
                log.info("deployed adapter %s (base=%s, path=%s)", name, body.get("model"), path)
        self._json(200, {"status": "deployed", "adapters": list(adapters.keys())})

    # --- DELETE /v1/models/<name> ---
    def _handle_delete_model(self, name: str):
        with _lock:
            removed = _adapters.pop(name, None)
        if removed:
            log.info("deleted adapter %s", name)
            self._json(200, {"status": "deleted", "model": name})
        else:
            self._json(404, {"error": f"model {name} not found"})

    # --- GET /v1/models ---
    def _handle_list_models(self):
        with _lock:
            data = [{"model": name, "status": info["status"]} for name, info in _adapters.items()]
        self._json(200, {"data": data})

    # --- POST /v1/chat/completions ---
    def _handle_chat(self):
        body = self._read_body()
        model = body.get("model", "unknown")
        adapter = body.get("lora_adapter_name", "")
        messages = body.get("messages", [])
        user_msg = messages[-1]["content"] if messages else ""

        display_model = adapter if adapter else model
        content = f"[mock-sllm] echo from {display_model}: {user_msg}"

        prompt_tokens = sum(len(m.get("content", "").split()) for m in messages)
        completion_tokens = len(content.split())

        resp = {
            "id": f"chatcmpl-{uuid.uuid4().hex[:12]}",
            "object": "chat.completion",
            "created": int(time.time()),
            "model": model,
            "choices": [
                {
                    "index": 0,
                    "message": {"role": "assistant", "content": content},
                    "finish_reason": "stop",
                }
            ],
            "usage": {
                "prompt_tokens": prompt_tokens,
                "completion_tokens": completion_tokens,
                "total_tokens": prompt_tokens + completion_tokens,
            },
        }
        log.info("chat request: model=%s adapter=%s → %d tokens", model, adapter, prompt_tokens + completion_tokens)
        self._json(200, resp)

    # --- Routing ---
    def do_GET(self):
        if self.path == "/health":
            self._handle_health()
        elif self.path == "/v1/models":
            self._handle_list_models()
        else:
            self._json(404, {"error": "not found"})

    def do_POST(self):
        if self.path == "/v1/models/deploy":
            self._handle_deploy()
        elif self.path == "/v1/chat/completions":
            self._handle_chat()
        else:
            self._json(404, {"error": "not found"})

    def do_DELETE(self):
        if self.path.startswith("/v1/models/"):
            name = self.path[len("/v1/models/"):]
            self._handle_delete_model(name)
        else:
            self._json(404, {"error": "not found"})

    def log_message(self, fmt, *args):
        pass  # suppress default access logs


if __name__ == "__main__":
    port = 8343
    server = HTTPServer(("0.0.0.0", port), SLLMHandler)
    log.info("mock ServerlessLLM listening on :%d", port)
    server.serve_forever()
