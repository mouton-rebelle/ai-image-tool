# Civitai Viewer · Prompt Bridge (ComfyUI node)

A destination-only ComfyUI node that sends a freshly rendered image and the
prompt behind it to the viewer app, then shows the rewritten Anima or Krea 2
prompt on the node so it can be copied back into the workflow.

The node deliberately has no outputs: it is an endpoint, never a source.

## Install

Copy (or symlink) the `civitai_prompt_bridge` directory into ComfyUI's
`custom_nodes` folder, then restart ComfyUI:

```
ComfyUI/custom_nodes/civitai_prompt_bridge/
```

No extra Python packages are needed — `aiohttp`, `numpy`, and `Pillow` already
ship with ComfyUI.

## Wire it up

Add **Civitai Viewer · Prompt Bridge** (category `civitai viewer`) and connect:

- `image` — the rendered image, e.g. the `VAEDecode` output (also fine to feed
  it from a `PreviewImage` branch)
- `prompt` — the positive prompt. Leave it as a text widget and paste, or
  right-click the node and convert the widget to an input to link the same
  string that feeds `CLIPTextEncode`.

Then set:

- `target_model` — `anima` or `krea-2`
- `concept` — `remix`, `describe`, `next`, or `before`
- `steering` — optional creative direction
- `server_url` — where the viewer app runs, e.g. `http://192.168.1.78:8081`.
  Defaults to the `CIVITAI_VIEWER_URL` environment variable, otherwise
  `http://127.0.0.1:8081`. Set this to the Mac's LAN address when ComfyUI runs
  on another machine — the viewer app listens on `0.0.0.0` by default.

## Use it

1. Queue the workflow. The node captures the first image of the batch (resized
   to 1536px, JPEG) together with the prompt it received.
2. Click **Generate prompt**. The button shows the elapsed time while the
   request runs; generation typically takes between 30 seconds and a few
   minutes.
3. The result appears in the read-only text box. **Copy prompt** puts it on the
   clipboard, or select the text manually.

`target_model`, `concept`, `steering`, and `server_url` are read when the button
is clicked, so several variations can be generated from a single run without
re-queueing. Only the image and prompt come from the last execution.

## How it works

- `nodes.py` — the node itself. It stores the last capture per node id in
  memory; `IS_CHANGED` returns `NaN` so a re-queue always refreshes the capture,
  even with an unchanged seed.
- `api.py` — registers `POST /civitai_prompt_bridge/generate` on ComfyUI's own
  server. It reads the capture, forwards it to
  `POST /api/comfy/generate-prompt` on the viewer app, and returns the prompt.
  Going through ComfyUI's Python process means the browser never talks to the
  viewer app directly, so no CORS setup is required.
- `js/civitai_prompt_bridge.js` — adds the buttons and the result box. It uses
  the classic `app.registerExtension` / `addWidget` APIs, so it works with the
  legacy node renderer.

Captures live in memory only and are dropped when ComfyUI restarts; nothing is
written to disk on the ComfyUI side.
