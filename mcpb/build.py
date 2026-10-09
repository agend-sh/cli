#!/usr/bin/env python3
"""Build a reproducible MCPB archive and Smithery payload from pinned source."""

import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import zipfile


ROOT = Path(__file__).resolve().parent.parent


def encoded_json(value):
    return (json.dumps(value, indent=2, ensure_ascii=False) + "\n").encode()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, default=ROOT / "dist" / "mcpb")
    options = parser.parse_args()
    manifest = json.loads((ROOT / "mcpb" / "manifest.json").read_text())
    package = json.loads((ROOT / "mcpb" / "server" / "image.json").read_text())
    if manifest["version"] != package["version"]:
        raise SystemExit("Manifest version must match the pinned image version")

    # Evaluate only the release's embedded tool schemas. No MCP process or
    # hosted tool calls are started while assembling this package.
    source = subprocess.check_output(
        ["git", "show", package["source_commit"] + ":internal/mcp/tools.go"],
        cwd=ROOT,
        text=True,
    )
    with tempfile.TemporaryDirectory(prefix="agend-mcpb-schema-") as directory:
        directory = Path(directory)
        (directory / "tools.go").write_text(source.replace("package mcp", "package main", 1))
        (directory / "main.go").write_text(
            'package main\nimport ("encoding/json"; "os")\n'
            "func main() { if err := json.NewEncoder(os.Stdout).Encode(toolDefinitions()); "
            "err != nil { panic(err) } }\n"
        )
        tools = json.loads(
            subprocess.check_output(
                ["go", "run", str(directory / "main.go"), str(directory / "tools.go")],
                cwd=directory,
            )
        )

    manifest["tools"] = [
        {"name": tool["name"], "description": tool["description"]} for tool in tools
    ]
    manifest["tools_generated"] = True
    card = {
        "serverInfo": {
            "name": "agend",
            "title": manifest["display_name"],
            "version": package["version"],
            "websiteUrl": manifest["homepage"],
            "description": manifest["description"],
        },
        "tools": tools,
        "resources": [],
        "prompts": [],
    }
    config_schema = {
        "type": "object",
        "properties": {
            "api_token": {
                "type": "string",
                "title": manifest["user_config"]["api_token"]["title"],
                "description": manifest["user_config"]["api_token"]["description"],
                "writeOnly": True,
            }
        },
        "required": ["api_token"],
        "additionalProperties": False,
    }
    payload = {
        "type": "stdio",
        "runtime": "node",
        "configSchema": config_schema,
        "serverCard": card,
    }
    files = {
        "manifest.json": encoded_json(manifest),
        "server-card.json": encoded_json(card),
        "server/launch.cjs": (ROOT / "mcpb" / "server" / "launch.cjs").read_bytes(),
        "server/image.json": encoded_json(package),
        "README.md": (ROOT / "docs" / "smithery.md").read_bytes(),
        "LICENSE": (ROOT / "LICENSE").read_bytes(),
        "assets/logo.svg": (ROOT / "assets" / "logo.svg").read_bytes(),
    }
    output = options.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    archive = output / f'agend-sh-{package["version"]}.mcpb'
    with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as bundle:
        for name, data in sorted(files.items()):
            entry = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            entry.compress_type = zipfile.ZIP_DEFLATED
            entry.external_attr = 0o100644 << 16
            bundle.writestr(entry, data)
    (output / "manifest.json").write_bytes(encoded_json(manifest))
    (output / "server-card.json").write_bytes(encoded_json(card))
    (output / "publish-payload.json").write_bytes(encoded_json(payload))
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    (output / "checksums.txt").write_text(f"{digest}  {archive.name}\n")
    print(json.dumps({"bundle": str(archive), "sha256": digest, "tools": len(tools)}))


if __name__ == "__main__":
    main()
