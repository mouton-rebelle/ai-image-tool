import { app } from "../../scripts/app.js";
import { api } from "../../scripts/api.js";
import { ComfyWidgets } from "../../scripts/widgets.js";

const NODE_NAME = "CivitaiPromptBridge";
const GENERATE_LABEL = "Generate prompt";
const COPY_LABEL = "Copy prompt";
const HINT = "Queue the workflow, then click “Generate prompt”.";

function widgetValue(node, name, fallback) {
	const widget = node.widgets?.find((candidate) => candidate.name === name);
	if (!widget || widget.value === undefined || widget.value === null) {
		return fallback;
	}
	return widget.value;
}

async function copyText(text) {
	if (!text) {
		return false;
	}
	try {
		if (navigator.clipboard?.writeText) {
			await navigator.clipboard.writeText(text);
			return true;
		}
	} catch (error) {
		// Plain http origins have no clipboard API, fall through to the helper.
	}
	const helper = document.createElement("textarea");
	helper.value = text;
	helper.style.position = "fixed";
	helper.style.top = "-1000px";
	document.body.appendChild(helper);
	helper.select();
	let copied = false;
	try {
		copied = document.execCommand("copy");
	} catch (error) {
		copied = false;
	}
	helper.remove();
	return copied;
}

function flashLabel(node, widget, label, restore) {
	widget.label = label;
	node.setDirtyCanvas(true, true);
	setTimeout(() => {
		widget.label = restore;
		node.setDirtyCanvas(true, true);
	}, 1500);
}

async function requestPrompt(node) {
	if (node.civitaiPending) {
		return;
	}
	const button = node.civitaiGenerateButton;
	const output = node.civitaiOutputWidget;
	node.civitaiPending = true;

	const startedAt = Date.now();
	const ticker = setInterval(() => {
		const elapsed = Math.round((Date.now() - startedAt) / 1000);
		button.label = `Generating… ${elapsed}s`;
		node.setDirtyCanvas(true, true);
	}, 500);
	button.label = "Generating… 0s";
	node.setDirtyCanvas(true, true);

	try {
		const response = await api.fetchApi("/civitai_prompt_bridge/generate", {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({
				node_id: String(node.id),
				target_model: widgetValue(node, "target_model", "anima"),
				concept: widgetValue(node, "concept", "remix"),
				steering: widgetValue(node, "steering", ""),
				server_url: widgetValue(node, "server_url", ""),
			}),
		});
		let result = {};
		try {
			result = await response.json();
		} catch (error) {
			result = {};
		}
		if (!response.ok || !result.prompt) {
			output.value = `⚠ ${result.error || `Request failed with status ${response.status}`}`;
		} else {
			output.value = result.prompt;
		}
	} catch (error) {
		output.value = `⚠ ${error}`;
	} finally {
		clearInterval(ticker);
		button.label = GENERATE_LABEL;
		node.civitaiPending = false;
		node.setDirtyCanvas(true, true);
	}
}

app.registerExtension({
	name: "civitai.PromptBridge",
	async beforeRegisterNodeDef(nodeType, nodeData) {
		if (nodeData.name !== NODE_NAME) {
			return;
		}

		const onNodeCreated = nodeType.prototype.onNodeCreated;
		nodeType.prototype.onNodeCreated = function () {
			onNodeCreated?.apply(this, arguments);
			const node = this;

			const generateButton = node.addWidget("button", GENERATE_LABEL, null, () => requestPrompt(node), {
				serialize: false,
			});
			const copyButton = node.addWidget(
				"button",
				COPY_LABEL,
				null,
				async () => {
					const text = node.civitaiOutputWidget?.value;
					if (!text) {
						flashLabel(node, copyButton, "Nothing to copy", COPY_LABEL);
						return;
					}
					const copied = await copyText(text);
					flashLabel(node, copyButton, copied ? "Copied ✓" : "Copy failed", COPY_LABEL);
				},
				{ serialize: false },
			);

			const output = ComfyWidgets["STRING"](node, "generated_prompt", ["STRING", { multiline: true }], app).widget;
			output.value = "";
			if (output.inputEl) {
				output.inputEl.readOnly = true;
				output.inputEl.placeholder = HINT;
				output.inputEl.style.opacity = 0.85;
			}

			node.civitaiGenerateButton = generateButton;
			node.civitaiOutputWidget = output;

			const size = node.computeSize();
			node.setSize([Math.max(size[0], 380), Math.max(size[1], 460)]);
		};
	},
});
