"""Small HTTP/credential helpers for local import and analytics verification."""
import hashlib
import json
import os
from pathlib import Path
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class API:
    def __init__(self, url, env_file=None, timeout=10):
        parsed = urllib.parse.urlsplit(url)
        if parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username or parsed.password or parsed.path not in ("", "/") or parsed.query or parsed.fragment:
            raise ValueError("URL must be an http(s) origin without credentials, query, or path")
        self.url = url.rstrip("/")
        self.timeout = timeout
        self.credentials = {}
        if env_file:
            for line in Path(env_file).read_text().splitlines():
                if not line.strip() or line.lstrip().startswith("#"):
                    continue
                key, separator, value = line.partition("=")
                if not separator or not key.isidentifier():
                    raise ValueError("invalid environment file")
                self.credentials[key] = value
        self.credentials.update(os.environ)
        self.http = urllib.request.build_opener(NoRedirect())

    def request(self, path, token_key, params=None, body=None):
        token = self.credentials.get(token_key)
        if not token:
            raise ValueError(f"set {token_key} or provide --env-file")
        url = self.url + path
        if params:
            url += "?" + urllib.parse.urlencode(params)
        data = None if body is None else json.dumps(body, ensure_ascii=False, allow_nan=False).encode()
        if data is not None and len(data) > 2 << 20:
            raise ValueError("batch exceeds 2 MiB; use a smaller --batch-size")
        req = urllib.request.Request(url, data=data, headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
        try:
            with self.http.open(req, timeout=self.timeout) as response:
                raw = response.read((8 << 20) + 1)
                if len(raw) > 8 << 20:
                    raise ValueError("API response exceeds 8 MiB")
                result = json.loads(raw)
                if not isinstance(result, dict):
                    raise ValueError("expected a JSON object response")
                return result
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"{path} returned HTTP {error.code}") from None
        except urllib.error.URLError:
            raise RuntimeError(f"cannot reach {path}") from None


def checksum(refs):
    ordered = sorted(refs, key=lambda ref: ref.encode("utf-8"))
    return hashlib.sha256("\n".join(ordered).encode("utf-8")).hexdigest()
