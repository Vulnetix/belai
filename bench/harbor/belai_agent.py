"""Harbor installed-agent adapter that runs a locally built Belai binary.

Harbor (https://github.com/laude-institute/harbor) copies nothing from the
host by default, so this adapter uploads the binary named by the ``binary``
option into the task container and runs one headless turn:

    belai -trust-dir -ask-permission=false -prompt "<task>" -usage-json /logs/agent/belai-usage.json

``-ask-permission=false`` resolves permission asks to allow, which is the
only way a headless run can edit files; the task container is the boundary.
Guardrails stay on, so the classifier's tokens are part of what is measured.

Run it with ``just bench`` (see docs/benchmarks.md), or directly:

    PYTHONPATH=bench/harbor harbor run -d terminal-bench@2.0 -m anthropic/claude-sonnet-5 \\
        --agent belai_agent:Belai --ak binary=bin/belai-bench-linux-amd64

The usage summary written by ``-usage-json`` is read back after the run and
reported to Harbor as token counts per model. Harbor prices them.
"""

from __future__ import annotations

import json
import shlex
from pathlib import Path
from typing import Annotated, Any, override

from pydantic import Field

from harbor.agents.capabilities import AgentCapabilities
from harbor.agents.installed.base import BaseInstalledAgent, with_prompt_template
from harbor.agents.model_connection import ModelConnectionSpec, ResolvedModelConnection
from harbor.agents.options import Cli, InstalledAgentOptions
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext, ModelUsage

# Where the binary and the optional global settings land in the container.
_BIN = "/installed-agent/belai"
_HOME = "/installed-agent/belai-home"
_USAGE = "belai-usage.json"


class BelaiOptions(InstalledAgentOptions):
    binary: str = Field(
        default="bin/belai-bench-linux-amd64",
        description="Host path of a Linux Belai binary built for the container's architecture.",
    )
    settings: str | None = Field(
        default=None,
        description=(
            "Host path of a global settings.json to install (for example an offload arm). "
            "It becomes $BELAI_HOME/settings.json in the container."
        ),
    )
    mode: Annotated[str | None, Cli("-mode")] = Field(
        default=None, description="agent, plan or goal; default lets Belai classify the prompt."
    )
    effort: Annotated[str | None, Cli("-effort")] = Field(
        default=None, description="Thinking effort: low, medium or high."
    )


class Belai(BaseInstalledAgent):
    """Runs Belai headless inside the task container."""

    capabilities = AgentCapabilities()
    MODEL_CONNECTION = ModelConnectionSpec(passthrough=True)
    options_model = BelaiOptions

    @staticmethod
    @override
    def name() -> str:
        return "belai"

    @property
    @override
    def model_connection(self) -> ResolvedModelConnection:
        return super().model_connection

    @override
    def get_version_command(self) -> str | None:
        return f"{_BIN} -version"

    @override
    def parse_version(self, stdout: str) -> str:
        return stdout.strip().split()[-1] if stdout.strip() else "unknown"

    @override
    async def install(self, environment: BaseEnvironment) -> None:
        opts: BelaiOptions = self.options  # type: ignore[assignment]
        binary = Path(opts.binary).expanduser()
        if not binary.is_file():
            raise RuntimeError(f"belai binary not found at {binary}; run `just build-bench` first")
        await environment.upload_file(binary, _BIN)
        await self.exec_as_root(environment, command=f"chmod 0755 {_BIN} && mkdir -p {_HOME} && chmod 0777 {_HOME}")
        if opts.settings:
            await environment.upload_file(Path(opts.settings).expanduser(), f"{_HOME}/settings.json")
            await self.exec_as_root(environment, command=f"chmod 0644 {_HOME}/settings.json")

    @override
    def populate_context_post_run(self, context: AgentContext) -> None:
        path = self.logs_dir / _USAGE
        try:
            summary: dict[str, Any] = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            self.logger.warning("no belai usage summary at %s", path)
            return
        context.n_input_tokens = summary.get("prompt_tokens", 0)
        context.n_cache_tokens = summary.get("cache_read_tokens", 0)
        context.n_output_tokens = summary.get("completion_tokens", 0)
        context.model_usage = {
            model: ModelUsage(
                n_input_tokens=t.get("prompt_tokens", 0),
                n_cache_tokens=t.get("cache_read_tokens", 0),
                n_output_tokens=t.get("completion_tokens", 0),
            )
            for model, t in (summary.get("by_model") or {}).items()
        }

    @override
    @with_prompt_template
    async def run(self, instruction: str, environment: BaseEnvironment, context: AgentContext) -> None:
        if not self.model_name or "/" not in self.model_name:
            raise ValueError("model must be provider/model, for example anthropic/claude-sonnet-5")
        provider, model = self.model_name.split("/", 1)
        access = self.model_connection
        env = {**access.env, "BELAI_HOME": _HOME, "NO_COLOR": "1"}
        flags = self.build_cli_flags()
        command = " ".join(
            [
                _BIN,
                "-trust-dir",
                "-ask-permission=false",
                f"-provider {shlex.quote(access.provider or provider)}",
                f"-model {shlex.quote(model)}",
                flags,
                f"-usage-json {shlex.quote(str(self.environment_logs_dir / _USAGE))}",
                f"-prompt {shlex.quote(instruction)}",
                f"2>&1 | tee {shlex.quote(str(self.environment_logs_dir / 'belai.txt'))}",
            ]
        )
        await self.exec_as_agent(environment, command=f"set -o pipefail; {command}", env=env)
