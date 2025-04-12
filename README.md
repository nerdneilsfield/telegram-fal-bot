# Telegram Fal Bot

A Telegram bot integrated with the Fal AI API to provide AI-powered features like image generation and captioning directly within Telegram chats.

## Features

*   **Fal AI Integration:** Leverages Fal AI for functionalities such as:
    *   Image generation (potentially using different LoRA models).
    *   Image captioning.
*   **Telegram Bot Interface:** Interact with the bot using standard Telegram commands.
*   **Configuration:** Easily configurable via a `config.toml` file.
*   **Authorization:** Control who can use the bot via user ID authorization.
*   **Persistence:** Uses a database (e.g., SQLite) to store user data and potentially track usage/balance.
*   **Commands:**
    *   `/start`: Start interacting with the bot.
    *   `/help`: Get help information.
    *   `/cancel`: Cancel the current operation.
    *   `/balance`: Check usage balance (if enabled).
    *   `/loras`: List available LoRA models/styles.
    *   `/version`: Show bot version information.
    *   `/myconfig`: View or set personal generation parameters.
    *   `/set`: (Admin) Manage users and permissions.

## Getting Started

**(Note: Add specific setup instructions here)**

1.  **Prerequisites:** Go environment, etc.
2.  **Configuration:**
    *   Copy `config.example.toml` (if provided) to `config.toml`.
    *   Fill in your `BotToken`, `FalAIKey`, authorized user IDs, admin user IDs, and Fal API endpoints in `config.toml`.
    *   Configure database path (`DBPath`) and other settings as needed.
3.  **Build:** `go build -o telegram-fal-bot` (or use provided Makefile/justfile).
4.  **Run:** `./telegram-fal-bot start ./config.toml`

## Configuration (`config.toml`)

The bot's behavior is controlled by the `config.toml` file. Here are the key sections and options:

*   **`botToken` (string, Required):** Your Telegram Bot Token obtained from BotFather.
*   **`falAIKey` (string, Required):** Your API key from Fal.ai.
*   **`telegramAPIURL` (string, Optional):** Custom Telegram API endpoint. Defaults to the official Telegram API URL.
*   **`dbPath` (string, Required):** Path to the SQLite database file used for storing user data, balances, etc. (e.g., `botdata.db`).
*   **`[logConfig]`:**
    *   `level` (string): Logging verbosity (`"debug"`, `"info"`, `"warn"`, `"error"`).
    *   `format` (string): Log output format (`"json"` or `"text"`).
    *   `file` (string): Path to a log file. If empty, logs are printed to the console.
*   **`[apiEndpoints]`:**
    *   `baseURL` (string): Base URL for the Fal.ai API (e.g., `"https://queue.fal.run"`).
    *   `fluxLora` (string): Relative path/identifier for the Fal.ai Flux LoRA endpoint (e.g., `"fal-ai/flux-lora"`).
    *   `florenceCaption` (string): Relative path/identifier for the Fal.ai Florence Caption endpoint (e.g., `"fal-ai/florence-2-base"`).
*   **`[auth]`:**
    *   `authorizedUserIDs` ([]int64, Required): A list of Telegram user IDs allowed to interact with the bot.
*   **`[admins]`:**
    *   `adminUserIDs` ([]int64, Required): A list of Telegram user IDs designated as administrators. Admins receive detailed error messages.
*   **`[[userGroups]]` (Optional):** Define groups to manage LoRA access.
    *   `name` (string): The name of the group (e.g., `"vip"`).
    *   `userIDs` ([]int64): List of user IDs belonging to this group.
*   **`[balance]` (Optional):** Configure the usage balance system.
    *   `initialBalance` (float64): The starting balance assigned to new users.
    *   `costPerGeneration` (float64): The cost deducted for each image generation. Set to 0 or less to disable balance tracking.
*   **`[defaultGenerationSettings]`:** Default parameters for image generation.
    *   `imageSize` (string): Default aspect ratio (e.g., `"portrait_16_9"`, `"square"`).
    *   `numInferenceSteps` (int): Default number of inference steps (1-49).
    *   `guidanceScale` (float64): Default guidance scale (0-15).
    *   `numImages` (int): Default number of images to generate per request.
*   **`[[baseLoRAs]]` (Optional):** Define base LoRAs that might be applied implicitly.
    *   `name` (string): Internal name.
    *   `url` (string): Fal.ai URL/identifier.
    *   `weight` (float64): LoRA weight.
    *   `allowGroups` ([]string): Restrict implicit usage to specific groups.
*   **`[[loras]]` (Required - At least one LoRA or BaseLoRA):** Define selectable LoRA styles.
    *   `name` (string): User-friendly name displayed in the bot.
    *   `url` (string): Fal.ai URL/identifier for the LoRA.
    *   `weight` (float64): Default weight for this style.
    *   `allowGroups` ([]string): Restrict visibility/usage of this style to specific user groups (empty means public for all authorized users).

Make sure to replace placeholder values like `"YOUR_TELEGRAM_BOT_TOKEN_HERE"` and `"YOUR_FAL_AI_KEY_HERE"` with your actual credentials.

## Usage

Interact with the bot in your Telegram chat using the available commands.

## Contributing

*(Add contribution guidelines if applicable)*

## License

This project is licensed under the MIT License.
