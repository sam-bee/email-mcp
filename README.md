# Email MCP

Placeholder MCP server using Streamable HTTP at `/mcp`. It supports the MCP
handshake and returns an empty tool list. It has no mail access or credentials.

Run locally with `docker build -t email-mcp .` and
`docker run --rm -p 127.0.0.1:8000:8000 email-mcp`. Connect an MCP client to
`http://127.0.0.1:8000/mcp`.

Pushes to `master` publish `ghcr.io/sam-bee/email-mcp:latest` through GitHub
Actions. The containernode repository manages the k3s deployment and Tailscale
Serve endpoint.
