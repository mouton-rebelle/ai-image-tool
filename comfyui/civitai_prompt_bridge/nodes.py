"""Destination node that hands a rendered image and its prompt to the viewer app.

The node never produces an output: it only captures whatever reaches it so the
"Generate prompt" button (see ``js/civitai_prompt_bridge.js``) can ask the Go
app for a rewritten prompt at any time, without re-queueing the workflow.
"""

import base64
import io
import json
import os
import time

import numpy as np
from PIL import Image

TARGET_MODELS = ["anima", "krea-2"]
CONCEPTS = ["remix", "describe", "next", "before"]

# The viewer app downscales to 1536px anyway, so shrink before the network hop.
CAPTURE_MAX_DIMENSION = 1536
CAPTURE_JPEG_QUALITY = 88

DEFAULT_SERVER_URL = os.environ.get("CIVITAI_VIEWER_URL", "http://127.0.0.1:8081")

# Node id -> latest capture. Only the most recent run of each node is kept.
_captures = {}
_MAX_CAPTURES = 16


def store_capture(node_id, capture):
    if len(_captures) >= _MAX_CAPTURES and str(node_id) not in _captures:
        oldest = min(_captures, key=lambda key: _captures[key]["captured_at"])
        _captures.pop(oldest, None)
    _captures[str(node_id)] = capture


def get_capture(node_id):
    return _captures.get(str(node_id))


def encode_image(image):
    """Turn the first frame of a ComfyUI IMAGE batch into base64 JPEG data."""
    frame = image[0]
    array = frame.detach().cpu().numpy() if hasattr(frame, "detach") else np.asarray(frame)
    array = np.clip(array * 255.0 + 0.5, 0, 255).astype(np.uint8)
    if array.ndim == 3 and array.shape[2] == 1:
        array = array[:, :, 0]

    picture = Image.fromarray(array)
    if picture.mode != "RGB":
        picture = picture.convert("RGB")
    picture.thumbnail((CAPTURE_MAX_DIMENSION, CAPTURE_MAX_DIMENSION), Image.LANCZOS)

    buffer = io.BytesIO()
    picture.save(buffer, format="JPEG", quality=CAPTURE_JPEG_QUALITY)
    return base64.b64encode(buffer.getvalue()).decode("ascii"), picture.size


class CivitaiPromptBridge:
    """Send the rendered image plus its prompt to the viewer app for a rewrite."""

    @classmethod
    def INPUT_TYPES(cls):
        return {
            "required": {
                "image": ("IMAGE",),
                "target_model": (TARGET_MODELS,),
                "concept": (CONCEPTS,),
            },
            "optional": {
                "prompt": ("STRING", {"multiline": True, "default": "", "dynamicPrompts": False}),
                "steering": ("STRING", {"multiline": True, "default": "", "dynamicPrompts": False}),
                "server_url": ("STRING", {"default": DEFAULT_SERVER_URL}),
            },
            "hidden": {"unique_id": "UNIQUE_ID"},
        }

    RETURN_TYPES = ()
    FUNCTION = "capture"
    OUTPUT_NODE = True
    CATEGORY = "civitai viewer"
    DESCRIPTION = (
        "Captures the incoming image and prompt, then lets you generate an Anima or "
        "Krea 2 prompt from them with the button on the node."
    )

    @classmethod
    def IS_CHANGED(cls, **kwargs):
        # Always run: the button reads back whatever the last queue produced.
        return float("nan")

    def capture(
        self,
        image,
        target_model,
        concept,
        prompt="",
        steering="",
        server_url=DEFAULT_SERVER_URL,
        unique_id=None,
    ):
        encoded, size = encode_image(image)
        store_capture(
            unique_id,
            {
                "image_base64": encoded,
                "prompt": prompt or "",
                "target_model": target_model,
                "concept": concept,
                "steering": steering or "",
                "server_url": server_url or DEFAULT_SERVER_URL,
                "captured_at": time.time(),
            },
        )

        status = json.dumps(
            {
                "width": size[0],
                "height": size[1],
                "prompt_characters": len(prompt or ""),
            }
        )
        return {"ui": {"civitai_capture": [status]}}


NODE_CLASS_MAPPINGS = {"CivitaiPromptBridge": CivitaiPromptBridge}
NODE_DISPLAY_NAME_MAPPINGS = {"CivitaiPromptBridge": "Civitai Viewer · Prompt Bridge"}
