"""Civitai viewer prompt bridge: a ComfyUI destination node for prompt rewrites."""

from . import api  # noqa: F401  (registers the HTTP route used by the node button)
from .nodes import NODE_CLASS_MAPPINGS, NODE_DISPLAY_NAME_MAPPINGS

WEB_DIRECTORY = "./js"

__all__ = ["NODE_CLASS_MAPPINGS", "NODE_DISPLAY_NAME_MAPPINGS", "WEB_DIRECTORY"]
