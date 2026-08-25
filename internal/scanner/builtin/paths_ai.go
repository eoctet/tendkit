package builtin

import "github.com/eoctet/tendkit/internal/model"

func AIPathDefinitions() []PathDefinition {
	return []PathDefinition{
		{ID: "codex", Name: "OpenAI Codex CLI", Binary: "codex", VersionCommand: "codex --version", UpdateCommand: "codex update", UpdateProbe: "codex update --help", Provider: model.ProviderNPM, Package: "@openai/codex", Description: "Terminal coding agent from OpenAI.", URL: "https://github.com/openai/codex"},
		{ID: "claude", Name: "Claude Code", Binary: "claude", VersionCommand: "claude --version", UpdateCommand: "claude update", UpdateProbe: "claude update --help", Provider: model.ProviderNPM, Package: "@anthropic-ai/claude-code", Description: "Anthropic's agentic coding tool for the terminal.", URL: "https://github.com/anthropics/claude-code"},
		{ID: "gemini", Name: "Gemini CLI", Binary: "gemini", VersionCommand: "gemini --version", Provider: model.ProviderNPM, Package: "@google/gemini-cli", Description: "Google's open-source terminal AI agent.", URL: "https://github.com/google-gemini/gemini-cli"},
		{ID: "qwen", Name: "Qwen Code", Binary: "qwen", VersionCommand: "qwen --version", Provider: model.ProviderNPM, Package: "@qwen-code/qwen-code", Description: "Terminal coding agent optimized for Qwen models.", URL: "https://github.com/QwenLM/qwen-code"},
		{ID: "aider", Name: "aider", Binary: "aider", VersionCommand: "aider --version", UpdateCommand: "aider --upgrade", UpdateProbe: "aider --help", Provider: model.ProviderPyPI, Package: "aider-chat", Description: "AI pair programmer that edits code in a local Git repository.", URL: "https://github.com/Aider-AI/aider"},
		{ID: "goose", Name: "goose", Binary: "goose", VersionCommand: "goose --version", UpdateCommand: "goose update", UpdateProbe: "goose update --help", Provider: model.ProviderGitHubRelease, Package: "block/goose", Description: "Extensible open-source AI agent for software tasks.", URL: "https://github.com/block/goose"},
		{ID: "opencode", Name: "OpenCode", Binary: "opencode", VersionCommand: "opencode --version", UpdateCommand: "opencode upgrade", UpdateProbe: "opencode upgrade --help", Provider: model.ProviderGitHubRelease, Package: "anomalyco/opencode", Description: "Open-source terminal coding agent with provider choice.", URL: "https://github.com/anomalyco/opencode"},
		{ID: "copilot", Name: "GitHub Copilot CLI", Binary: "copilot", VersionCommand: "copilot --version", UpdateCommand: "copilot update", UpdateProbe: "copilot update --help", Provider: model.ProviderNPM, Package: "@github/copilot", Description: "GitHub's terminal-native agentic coding assistant.", URL: "https://github.com/github/copilot-cli"},
		{ID: "crush", Name: "Crush", Binary: "crush", VersionCommand: "crush --version", Provider: model.ProviderGitHubRelease, Package: "charmbracelet/crush", Description: "Multi-model terminal coding agent from Charm.", URL: "https://github.com/charmbracelet/crush"},
		{ID: "fabric", Name: "Fabric", Binary: "fabric", VersionCommand: "fabric --version", Provider: model.ProviderGitHubRelease, Package: "danielmiessler/fabric", Description: "AI augmentation framework with a composable command-line interface.", URL: "https://github.com/danielmiessler/fabric"},
		{ID: "mods", Name: "Mods", Binary: "mods", VersionCommand: "mods --version", Provider: model.ProviderGitHubRelease, Package: "charmbracelet/mods", Description: "AI command-line tool designed for Unix pipelines.", URL: "https://github.com/charmbracelet/mods"},
		{ID: "llm", Name: "LLM", Binary: "llm", VersionCommand: "llm --version", Provider: model.ProviderPyPI, Package: "llm", Description: "Extensible CLI for prompting and managing language models.", URL: "https://github.com/simonw/llm"},
		{ID: "aichat", Name: "AIChat", Binary: "aichat", VersionCommand: "aichat --version", Provider: model.ProviderGitHubRelease, Package: "sigoden/aichat", Description: "All-in-one LLM CLI with shell and agent features.", URL: "https://github.com/sigoden/aichat"},
		{ID: "shell_gpt", Name: "ShellGPT", Binary: "sgpt", VersionCommand: "sgpt --version", Provider: model.ProviderPyPI, Package: "shell-gpt", Description: "Shell-focused AI assistant for commands, code, and chat.", URL: "https://github.com/TheR1D/shell_gpt"},
		{ID: "ollama", Name: "Ollama", Binary: "ollama", VersionCommand: "ollama --version", Provider: model.ProviderGitHubRelease, Package: "ollama/ollama", Description: "Local model runtime and CLI used in AI development workflows.", URL: "https://github.com/ollama/ollama"},
	}
}
