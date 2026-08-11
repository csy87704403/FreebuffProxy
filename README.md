# FreebuffProxy

[English](README.md) | [简体中文](README_zh.md)

FreebuffProxy is an OpenAI-compatible proxy server for [Freebuff](https://freebuff.com). It translates standard OpenAI API requests into Freebuff's backend format, allowing you to use Freebuff's free models with any OpenAI-compatible client, SDK, or CLI tool.

## Features

- **OpenAI Compatible API** — Standard OpenAI endpoints; works with any compatible client out of the box.
- **Stealth Request Handling** — Dynamic, randomized client fingerprints that mimic official Freebuff SDK behavior.
- **Multi-Token Rotation** — Cycle through multiple auth tokens with automatic periodic rotation.
- **HTTP Proxy Support** — Route all outbound traffic through a configurable upstream proxy.
- **Built-in Web UI** — Model pull / latency probe / token status / usage stats / request logs, embedded in a single binary.
- **GHCR Docker Image** — Public image, one-command deployment on any machine.

## Getting Auth Tokens

FreebuffProxy requires one or more Freebuff **auth tokens**. There are two ways to obtain one:

### Method 1 — Web (Recommended)

Visit **[https://freebuff.llm.pm](https://freebuff.llm.pm)**, log in with your Freebuff account, and your auth token will be displayed directly on the page. Copy it as your **AUTH_TOKENS** — no local installation required.

### Method 2 — Freebuff CLI

Install the Freebuff CLI:

```bash
npm i -g freebuff
```

Run `freebuff` in your terminal — on first launch it will guide you through login.

After logging in, your token is saved to a local credentials file:

| OS | Credentials Path |
|---|---|
| Windows | `C:\Users\<username>\.config\manicode\credentials.json` |
| Linux / macOS | `~/.config/manicode/credentials.json` |

The file looks like:

```json
{
  "default": {
    "id": "user_10293847",
    "name": "Zhang San",
    "email": "zhangsan@example.com",
    "authToken": "fa82b5c1-e39d-4c7a-961f-d2b3c4e5f6a7",
    ...
  }
}
```

Only the `authToken` value is needed — copy it as your **AUTH_TOKENS**.

> **Tip:** Log in with multiple accounts and configure all their tokens for higher throughput.

## Configuration

Configuration is managed via a JSON file and/or environment variables. The JSON keys and environment variable names are identical. By default the app looks for `config.json` in the working directory; use `-config` to specify another path.

```json
{
  "LISTEN_ADDR": ":16880",
  "UPSTREAM_BASE_URL": "https://codebuff.com",
  "AUTH_TOKENS": ["eyJhb..."],
  "ROTATION_INTERVAL": "6h",
  "REQUEST_TIMEOUT": "15m",
  "API_KEYS": [],
  "HTTP_PROXY": ""
}
```

### Reference

| Key / Env Var | Description |
|---|---|
| `LISTEN_ADDR` | Proxy listen address (default `:16880`) |
| `UPSTREAM_BASE_URL` | Freebuff backend URL (default `https://codebuff.com`) |
| `AUTH_TOKENS` | Freebuff auth tokens (JSON array or comma-separated env var) |
| `ROTATION_INTERVAL` | Run rotation interval (default `6h`) |
| `REQUEST_TIMEOUT` | Upstream request timeout (default `15m`) |
| `API_KEYS` | Client API keys for proxy auth (empty = open access) |
| `HTTP_PROXY` | HTTP proxy for outbound requests |

Environment variables override JSON values when both are set.

## Deployment

### Docker (GHCR 镜像，推荐)

Pre-built images are available on GHCR (public, no login needed):

```bash
# 1. Pull the image
docker pull ghcr.io/csy87704403/freebuff-proxy:latest

# 2. Create config.json
mkdir -p /opt/freebuff-proxy
cat > /opt/freebuff-proxy/config.json << 'EOF'
{
  "LISTEN_ADDR": ":16880",
  "UPSTREAM_BASE_URL": "https://www.codebuff.com",
  "AUTH_TOKENS": ["put-your-freebuff-token-here"],
  "ROTATION_INTERVAL": "6h",
  "REQUEST_TIMEOUT": "120s",
  "API_KEYS": [],
  "HTTP_PROXY": ""
}
EOF

# 3. Run
docker run -d \
  --name freebuff-proxy \
  --restart unless-stopped \
  -p 16880:16880 \
  -v /opt/freebuff-proxy/config.json:/app/config.json \
  ghcr.io/csy87704403/freebuff-proxy:latest

# 4. Verify
curl http://127.0.0.1:16880/healthz
curl http://127.0.0.1:16880/v1/models
```

**Available tags:**
- `latest` — 最新稳定版
- `v1.0.0` — 首个发布版本

> 💡 **Web UI**: 部署后访问 `http://<你的IP>:16880/` 即可打开管理面板（拉起模型 / 探测延迟 / 账号状态 / 用量统计 / 调用日志）。

### 通过环境变量运行（不用 config.json）

```bash
docker run -d --name freebuff-proxy \
  -p 16880:16880 \
  -e AUTH_TOKENS="token1,token2" \
  -e UPSTREAM_BASE_URL="https://www.codebuff.com" \
  -e HTTP_PROXY="http://x.x.x.x:8078" \
  ghcr.io/csy87704403/freebuff-proxy:latest
```

Build from source:

```bash
docker build -t FreebuffProxy .
docker run -d -p 16880:16880 -e AUTH_TOKENS="token1,token2" FreebuffProxy
```

### Build from Source

**Requirements:** Go 1.23+

```bash
git clone https://github.com/csy87704403/FreebuffProxy.git
cd FreebuffProxy
go build -o FreebuffProxy .
./FreebuffProxy -config config.json
```

## Links

- [linux.do](https://linux.do)

## Disclaimer

This project has no official affiliation with OpenAI, Codebuff, or Freebuff. All related trademarks and copyrights belong to their respective owners.

All contents within this repository are provided solely for communication, experimentation, and learning, and do not constitute production-ready services or professional advice. This project is provided on an "As-Is" basis, and users must use it at their own risk. The author assumes no liability for any direct or indirect damages resulting from the use, modification, or distribution of this project, nor provides any warranties of any kind, express or implied.

## License

MIT
