#!/usr/bin/env python3
"""
ServerlessLLM-compatible API wrapper over vLLM.

Translates the SLLM API surface that the inference broker expects into vLLM
native API calls. Pure Python stdlib — no pip packages required, so it can
be baked into a tiny `python:3.11-slim`-based image and deployed inside a
TEE without runtime bind-mounts (issue #469).

Endpoints:
  GET  /health                -> 200
  POST /v1/models/deploy      -> POST vLLM /v1/load_lora_adapter
  DELETE /v1/models/<name>    -> POST vLLM /v1/unload_lora_adapter
  GET  /v1/models             -> GET  vLLM /v1/models
  POST /v1/chat/completions   -> POST vLLM /v1/chat/completions
                                  (with model name translation)

Configuration (env vars):
  VLLM_BASE             vLLM HTTP base URL.        Default: http://vllm:8000
  LORA_HOST_PREFIX      Host path prefix used in   Default: /lora-modules/
                        the broker's deploy body.
  LORA_CONTAINER_PREFIX vLLM-side container path.  Default: /lora-modules/
                        Set both prefixes to the
                        same value when the broker
                        and vLLM share a volume.
  PORT                  Listen port.               Default: 8343
"""

import json
import os
import sys
import logging
import urllib.request
import urllib.error
from http.server import HTTPServer, BaseHTTPRequestHandler

logging.basicConfig(level=logging.INFO, format="%(asctime)s [sllm-wrapper] %(message)s")
log = logging.getLogger("sllm-wrapper")

VLLM_BASE = os.environ.get("VLLM_BASE", "http://vllm:8000")
HOST_LORA_PREFIX = os.environ.get("LORA_HOST_PREFIX", "/lora-modules/")
CONTAINER_LORA_PREFIX = os.environ.get("LORA_CONTAINER_PREFIX", "/lora-modules/")

_model_cache = {}
_model_cache_ts = 0


def translate_path(host_path):
    """Translate host filesystem path to vLLM container path."""
    if host_path.startswith(HOST_LORA_PREFIX):
        translated = CONTAINER_LORA_PREFIX + host_path[len(HOST_LORA_PREFIX):]
        log.info("Path translated: %s -> %s", host_path, translated)
        return translated
    return host_path


def vllm_request(method, path, body=None, timeout=120):
    url = VLLM_BASE + path
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status, json.loads(resp.read())
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read())
        except Exception:
            return e.code, {"error": str(e)}
    except Exception as e:
        return 502, {"error": str(e)}


def _refresh_model_cache():
    import time
    global _model_cache, _model_cache_ts
    now = time.time()
    if now - _model_cache_ts < 10:
        return
    code, resp = vllm_request("GET", "/v1/models")
    if code == 200 and "data" in resp:
        _model_cache = {}
        for m in resp["data"]:
            full_id = m["id"]
            short_name = full_id.rsplit("/", 1)[-1]
            _model_cache[short_name] = full_id
            _model_cache[full_id] = full_id
        _model_cache_ts = now
        log.info("Model cache refreshed: %s", list(_model_cache.keys()))


def resolve_model_name(name):
    _refresh_model_cache()
    if name in _model_cache:
        return _model_cache[name]
    return name


class Handler(BaseHTTPRequestHandler):

    def _json(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _read_body(self):
        length = int(self.headers.get("Content-Length", 0))
        if length == 0:
            return {}
        return json.loads(self.rfile.read(length))

    def do_GET(self):
        if self.path == "/health":
            self._json(200, {"status": "ok"})
        elif self.path == "/v1/models":
            code, resp = vllm_request("GET", "/v1/models")
            self._json(code, resp)
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
            self._handle_delete(name)
        else:
            self._json(404, {"error": "not found"})

    def _handle_deploy(self):
        body = self._read_body()
        adapters = body.get("lora_adapters", {})
        results = []
        for name, path in adapters.items():
            container_path = translate_path(path)
            code, resp = vllm_request("POST", "/v1/load_lora_adapter", {
                "lora_name": name,
                "lora_path": container_path,
            })
            log.info("deploy %s (input=%s, vllm_path=%s) -> vLLM %d: %s",
                     name, path, container_path, code, resp)
            global _model_cache_ts
            _model_cache_ts = 0
            results.append({
                "name": name,
                "status": "deployed" if code == 200 else "failed",
                "detail": resp,
            })
        self._json(200, {"status": "deployed", "adapters": results})

    def _handle_delete(self, name):
        code, resp = vllm_request("POST", "/v1/unload_lora_adapter",
                                  {"lora_name": name})
        log.info("delete %s -> vLLM %d: %s", name, code, resp)
        global _model_cache_ts
        _model_cache_ts = 0
        if code == 200:
            self._json(200, {"status": "deleted", "model": name})
        else:
            self._json(code, resp)

    def _handle_chat(self):
        body = self._read_body()
        if "model" in body:
            original = body["model"]
            resolved = resolve_model_name(original)
            if original != resolved:
                log.info("Model name translated: %s -> %s", original, resolved)
                body["model"] = resolved
        code, resp = vllm_request("POST", "/v1/chat/completions", body)
        self._json(code, resp)

    def log_message(self, fmt, *args):
        pass


if __name__ == "__main__":
    port = int(os.environ.get("PORT", sys.argv[1] if len(sys.argv) > 1 else 8343))
    server = HTTPServer(("0.0.0.0", port), Handler)
    log.info("SLLM wrapper listening on :%d -> vLLM at %s", port, VLLM_BASE)
    log.info("Path mapping: %s -> %s", HOST_LORA_PREFIX, CONTAINER_LORA_PREFIX)
    server.serve_forever()
