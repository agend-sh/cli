"use strict";

// These hints describe the bundle's actual tool behavior, including the fact
// that connecting to a guest can wake it. Unknown tools keep conservative MCP
// defaults until their behavior has been reviewed.
function hints(
  title,
  readOnlyHint,
  destructiveHint,
  idempotentHint,
  openWorldHint = true,
) {
  return {
    title,
    readOnlyHint,
    destructiveHint,
    idempotentHint,
    openWorldHint,
  };
}

const annotations = {
  list_environments: hints("List environments", true, false, true),
  shell_exec: hints("Run a command", false, true, false),
  shell_provide_input: hints("Answer a command prompt", false, true, false),
  shell_send_raw: hints("Interact with a terminal", false, true, false),
  shell_resize: hints("Resize a terminal", false, false, true),
  shell_interrupt: hints("Interrupt a command", false, true, false),
  shell_task_output: hints("Read background task output", false, false, true),
  shell_task_stop: hints("Stop a background task", false, true, false),
  file_download: hints("Download a file", false, true, true),
  file_upload: hints("Upload a file", false, true, false),
  file_write: hints("Write a text file", false, true, false),
  file_move: hints("Move a file", false, true, false),
  env_stats: hints("Read environment statistics", false, false, true),
  port_expose: hints("Expose a port through HTTPS", false, false, false),
  port_unexpose: hints("Remove a port preview", false, true, false),
  port_list: hints("List port previews", false, false, true),
  env_create: hints("Create an environment", false, false, false),
  env_update: hints("Update environment metadata", false, true, true),
  profiles_list: hints("List machine profiles", true, false, true),
  env_status: hints("Read environment status", true, false, true),
  env_wake: hints("Wake an environment", false, false, false),
  env_cold_reset: hints("Cold reset an environment", false, true, false),
  reload_config: hints("Reload bridge credentials", false, true, false, false),
};

const outputSchema = {
  type: "object",
  description:
    "The complete tool result as text, also returned in MCP content for compatibility.",
  properties: {
    text: {
      type: "string",
      description:
        "The tool's complete human-readable result, including terminal output, metadata, or a tool error message.",
    },
  },
  required: ["text"],
  additionalProperties: false,
};

function enrichTools(tools) {
  return tools.map((tool) => ({
    ...tool,
    outputSchema,
    annotations: annotations[tool.name] || hints(tool.name, false, true, false),
  }));
}

function enrichMessage(message) {
  if (message.jsonrpc !== "2.0" || !Object.hasOwn(message, "id")) {
    return message;
  }
  const result = message.result;
  if (!result || typeof result !== "object") return message;
  if (Array.isArray(result.tools)) result.tools = enrichTools(result.tools);
  if (
    Array.isArray(result.content) &&
    !Object.hasOwn(result, "structuredContent")
  ) {
    result.structuredContent = {
      text: result.content
        .filter((item) => item.type === "text" && typeof item.text === "string")
        .map((item) => item.text)
        .join("\n"),
    };
  }
  return message;
}

module.exports = { enrichTools, enrichMessage };
