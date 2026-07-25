"""HTTP route backing the node button, proxying to the viewer app.

The call is made from ComfyUI's Python process rather than the browser so the
viewer app needs no CORS handling and can live on another machine.
"""

import asyncio
import json
import logging

import aiohttp
from aiohttp import web
from server import PromptServer

from .nodes import CONCEPTS, DEFAULT_SERVER_URL, TARGET_MODELS, get_capture

# The viewer app allows its LLM call up to 6 minutes.
REQUEST_TIMEOUT_SECONDS = 420

ROUTE = "/civitai_prompt_bridge/generate"


@PromptServer.instance.routes.post(ROUTE)
async def generate_prompt(request):
    try:
        payload = await request.json()
    except (json.JSONDecodeError, ValueError):
        return web.json_response({"error": "Invalid request body"}, status=400)

    capture = get_capture(payload.get("node_id"))
    if capture is None:
        return web.json_response(
            {"error": "Queue the workflow once so this node receives an image, then click again."},
            status=409,
        )

    target_model = payload.get("target_model") or capture["target_model"]
    concept = payload.get("concept") or capture["concept"]
    if target_model not in TARGET_MODELS:
        return web.json_response({"error": "Unknown target model %s" % target_model}, status=400)
    if concept not in CONCEPTS:
        return web.json_response({"error": "Unknown concept %s" % concept}, status=400)

    server_url = (payload.get("server_url") or capture["server_url"] or DEFAULT_SERVER_URL).strip()
    if not server_url.startswith(("http://", "https://")):
        server_url = "http://" + server_url
    url = server_url.rstrip("/") + "/api/comfy/generate-prompt"

    body = {
        "prompt": capture["prompt"],
        "image_base64": capture["image_base64"],
        "target_model": target_model,
        "concept": concept,
        "steering": (payload.get("steering") or "").strip() or capture["steering"],
    }

    timeout = aiohttp.ClientTimeout(total=REQUEST_TIMEOUT_SECONDS)
    try:
        async with aiohttp.ClientSession(timeout=timeout) as session:
            async with session.post(url, json=body) as response:
                status = response.status
                text = await response.text()
    except asyncio.TimeoutError:
        return web.json_response(
            {"error": "The viewer app did not answer within %d seconds" % REQUEST_TIMEOUT_SECONDS},
            status=504,
        )
    except aiohttp.ClientError as error:
        return web.json_response({"error": "Could not reach %s (%s)" % (url, error)}, status=502)

    try:
        result = json.loads(text)
    except json.JSONDecodeError:
        logging.warning("Civitai prompt bridge: unexpected response from %s: %s", url, text[:500])
        return web.json_response(
            {"error": "Unexpected response from the viewer app (status %d)" % status},
            status=502,
        )

    prompt = (result.get("prompt") or "").strip()
    if status != 200 or not prompt:
        error = result.get("error") or "The viewer app returned status %d" % status
        return web.json_response({"error": error}, status=502)

    return web.json_response({"prompt": prompt, "target_model": target_model, "concept": concept})
