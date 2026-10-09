"use strict";

// The published image owns authentication and the stdio MCP bridge. This
// launcher passes the account token through the environment, never argv.
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const readline = require("node:readline");
const { image } = require("./image.json");
const { enrichMessage, translateRequest } = require("./protocol.cjs");

function accountToken() {
  const configured = process.env.AGEND_API_TOKEN?.trim();
  if (configured && !configured.startsWith("${user_config.")) return configured;
  try {
    const credentials = JSON.parse(
      fs.readFileSync(
        path.join(os.homedir(), ".config", "agend", "credentials.json"),
        "utf8",
      ),
    );
    // Match the CLI's supported store version and active account selection.
    // A custom API URL is not compatible with the public image's defaults.
    if (
      credentials.version !== 3 ||
      (credentials.api_url && credentials.api_url !== "https://api.agend.sh")
    ) {
      return undefined;
    }
    const token = credentials.accounts?.[credentials.active]?.token;
    return typeof token === "string" ? token.trim() : undefined;
  } catch {
    // Missing, unreadable, or invalid credentials are handled by the same
    // actionable message. File contents and tokens never enter the logs.
    return undefined;
  }
}

const token = accountToken();
if (!token) {
  process.stderr.write(
    "agend-sh: run agend login, then restart this bundle; or configure your Agend account token.\n",
  );
  process.exit(1);
}

const child = spawn(
  "docker",
  ["run", "--rm", "-i", "--init", "--env", "AGEND_API_TOKEN", image],
  {
    stdio: ["pipe", "pipe", "inherit"],
    env: { ...process.env, AGEND_API_TOKEN: token },
    windowsHide: true,
  },
);

// Translate only grouped tool names; retain argument values, including raw
// terminal input. Responses use the same definitions as the server card.
const requests = readline.createInterface({
  input: process.stdin,
  crlfDelay: Infinity,
});
requests.on("line", (line) => {
  let output = line;
  try {
    const request = JSON.parse(line);
    const originalName = request.params?.name;
    translateRequest(request);
    if (request.params?.name !== originalName) output = JSON.stringify(request);
  } catch {
    // Let the upstream bridge report malformed requests.
  }
  if (!child.stdin.write(output + "\n")) requests.pause();
});
requests.on("close", () => child.stdin.end());
child.stdin.on("drain", () => requests.resume());
const lines = readline.createInterface({
  input: child.stdout,
  crlfDelay: Infinity,
});
lines.on("line", (line) => {
  let output = line;
  try {
    output = JSON.stringify(enrichMessage(JSON.parse(line)));
  } catch {
    // Preserve upstream framing if a line is not JSON; never log its contents.
  }
  if (!process.stdout.write(output + "\n")) lines.pause();
});
process.stdout.on("drain", () => lines.resume());
child.stdin.on("error", (error) => {
  if (error.code !== "EPIPE" && error.code !== "ERR_STREAM_DESTROYED") {
    process.stderr.write(
      "agend-sh: the Docker input stream closed unexpectedly.\n",
    );
    process.exitCode = 1;
  }
});

child.once("error", (error) => {
  const message =
    error.code === "ENOENT"
      ? "install Docker and make the docker command available to your MCP client"
      : "could not start Docker; check your local Docker installation";
  process.stderr.write(`agend-sh: ${message}.\n`);
  process.exitCode = 1;
});

child.once("close", (code, signal) => {
  requests.close();
  process.stdin.pause();
  process.exitCode = code ?? (signal === "SIGINT" ? 130 : 143);
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.once(signal, () => child.kill(signal));
}
