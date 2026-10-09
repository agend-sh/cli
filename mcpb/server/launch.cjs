"use strict";

// The published image owns authentication and the stdio MCP bridge. This
// launcher passes the account token through the environment, never argv.
const { spawn } = require("node:child_process");
const { image } = require("./image.json");

if (
  !process.env.AGEND_API_TOKEN ||
  process.env.AGEND_API_TOKEN.startsWith("${user_config.")
) {
  process.stderr.write(
    "agend-sh: configure your Agend account token before starting this bundle.\n",
  );
  process.exit(1);
}

const child = spawn(
  "docker",
  ["run", "--rm", "-i", "--init", "--env", "AGEND_API_TOKEN", image],
  { stdio: "inherit", env: process.env, windowsHide: true },
);

child.once("error", (error) => {
  const message =
    error.code === "ENOENT"
      ? "install Docker and make the docker command available to your MCP client"
      : "could not start Docker; check your local Docker installation";
  process.stderr.write(`agend-sh: ${message}.\n`);
  process.exitCode = 1;
});

child.once("exit", (code, signal) => {
  process.exitCode = code ?? (signal === "SIGINT" ? 130 : 143);
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.once(signal, () => child.kill(signal));
}
