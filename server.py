import re

import uvicorn
from mcp.server.mcpserver import MCPServer
from mcp.server.transport_security import TransportSecuritySettings
from starlette.datastructures import Headers
from starlette.responses import PlainTextResponse

server = MCPServer("email-mcp", version="0.1.0")
mcp = server.streamable_http_app(
    stateless_http=True,
    json_response=True,
    max_request_body_size=64 * 1024,
    # The ASGI entry point validates Host and Origin for the Tailscale URL.
    transport_security=TransportSecuritySettings(enable_dns_rebinding_protection=False),
)
local_host = re.compile(r"(?:localhost|127\.0\.0\.1)(?::[0-9]+)?")
tailscale_host = re.compile(r"email-mcp\.[a-z0-9-]+\.ts\.net(?::443)?")


async def app(scope, receive, send):
    if scope["type"] == "http":
        headers = Headers(scope=scope)
        host = headers.get("host", "").lower()
        if local_host.fullmatch(host):
            origin = f"http://{host}"
        elif tailscale_host.fullmatch(host):
            origin = f"https://{host}"
        else:
            return await PlainTextResponse("Unexpected Host", status_code=421)(
                scope, receive, send
            )
        if headers.get("origin", origin) != origin:
            return await PlainTextResponse("Unexpected Origin", status_code=403)(
                scope, receive, send
            )
    await mcp(scope, receive, send)


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=8000)
