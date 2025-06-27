# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Development Commands

### Building and Running
- `just build` - Build the Go binary with version info embedded
- `just run` - Run the application directly
- `just install` - Install the binary system-wide
- `go run main.go start config.toml` - Run with specific config file

### Code Quality and Testing
- `just fmt` - Format code using gofumpt and gci
- `just lint` - Run golangci-lint with project config
- `just test` - Run tests with coverage report
- `just clean` - Clean build artifacts

### Development Tools
- `just bootstrap` - Install build dependencies
- `just release-test` - Test GoReleaser configuration

Alternative Make commands are also available (`make build`, `make test`, etc.).

## Architecture Overview

This is a Telegram bot that integrates with Fal AI API for image generation and captioning. The architecture follows a clean, modular design:

### Core Components

**Main Entry Point**
- `main.go` - Application entry point with graceful shutdown handling
- `cmd/` - Cobra CLI commands (start, version)

**Bot Core** (`internal/bot/`)
- `bot.go` - Main bot initialization and message handling orchestration
- `handlers.go` - Telegram message and callback handlers
- `falai.go` - Fal AI API integration logic
- `state.go` - Multi-step conversation state management
- `keyboards.go` - Inline keyboard generation for LoRA selection

**Configuration** (`internal/config/`)
- `config.go` - TOML-based configuration loading and validation
- Supports complex configuration including LoRA definitions, user groups, balance system

**Storage Layer** (`internal/storage/`)
- `database.go` - Pure Go SQLite database initialization
- `balance.go` - User balance tracking
- `user_config_storage.go` - User preference persistence
- Uses `modernc.org/sqlite` driver (pure Go, no CGO)

**External APIs** (`pkg/falapi/`)
- `client.go` - HTTP client for Fal AI API
- `generate.go` - Image generation requests
- `caption.go` - Image captioning functionality

**Supporting Services**
- `internal/auth/` - User authorization and admin privileges
- `internal/i18n/` - Multi-language support
- `internal/logger/` - Structured logging with Zap

### Key Architectural Patterns

**State Management**: The bot uses a state machine pattern for multi-step interactions (LoRA selection, configuration updates). States are managed in-memory per user.

**Authorization**: Multi-level authorization system with authorized users, user groups, and admin privileges. LoRA access can be restricted by user groups.

**Configuration-Driven**: Extensive TOML configuration supports multiple LoRAs, base LoRAs, user groups, balance system, and generation parameters.

**Database Schema**: Simple SQLite schema with user balances and generation configs. Uses WAL mode for better concurrency.

**Error Handling**: Structured error handling with different error messages for users vs admins. Sensitive errors only shown to admin users.

## Development Notes

### Configuration
- Main config file is `config.toml` (see README for detailed format)
- The bot validates all config on startup including LoRA URLs and user group references
- Config includes Fal AI endpoints, Telegram bot token, SQLite database path

### Testing
- Run `just test` for full test suite with coverage
- Database tests use temporary SQLite files
- API client tests should mock HTTP responses

### Database Migrations
- Simple migration system in `storage/database.go`
- Manually tracks schema changes (consider proper migration system for production)
- Uses connection pooling and WAL mode for performance

### Internationalization
- Translation files in `internal/i18n/locales/` (TOML format)
- Currently supports English, Chinese, Japanese
- Bot commands and responses are localized

### Error Handling
- Different error verbosity for regular users vs admins
- Structured logging throughout the application
- API errors include request IDs when available

## Important Implementation Details

### LoRA System
- Supports both standard LoRAs and base LoRAs
- Group-based access control for LoRA visibility
- LoRA selection uses Telegram inline keyboards with callback data
- Callback data has length limits (64 bytes) - LoRA IDs are sanitized and truncated

### Balance System
- Optional balance tracking per user
- Configurable cost per generation
- Balance checked before API requests
- Supports admin balance queries to Fal AI account

### Bot Commands
- `/start`, `/help` - Basic bot interaction
- `/loras` - List available LoRAs based on user permissions
- `/myconfig` - Interactive configuration menu
- `/balance` - Show user/admin balance information
- `/version` - Bot version and build info
- `/log`, `/shortlog` - Admin-only logging commands
- `/cancel` - Cancel current multi-step operation

### Docker Support
- Pre-built images available on Docker Hub and GHCR
- Proper volume mounting for config, data, and logs
- Configuration paths must account for container filesystem

## CRITICAL: Required Workflow for All Feature Completion

### 1. Documentation Logging (MANDATORY)
**After completing ANY feature or task, you MUST update `docs/CLAUDE_LOGS.md`:**
- Create a new entry with current timestamp as the title (format: `## YYYY-MM-DD HH:MM:SS`)
- Document what was implemented, changed, or fixed
- Include any important technical details or decisions made
- List files that were modified or created

### 2. Git Commit Requirements (MANDATORY)
**ALL commits MUST be in English:**
- Use clear, descriptive English commit messages
- Follow conventional commit format when possible (feat:, fix:, docs:, etc.)
- Commit message should explain what was done and why
- Never use Chinese or other languages in commit messages

**Example workflow:**
1. Complete feature implementation
2. Update `docs/CLAUDE_LOGS.md` with timestamped entry
3. Commit changes with English message: "feat: add user preference caching system"
4. Ensure all commit messages are in English before pushing