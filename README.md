# Telegram Fal Bot

English | [中文](https://github.com/nerdneilsfield/telegram-fal-bot/blob/master/README_zh.md)

<div align="center">

<a href="https://pkg.go.dev/github.com/nerdneilsfield/telegram-fal-bot" target="_blank"><img src="https://pkg.go.dev/badge/github.com/nerdneilsfield/telegram-fal-bot/bot.svg" alt="Go Reference"></a>
<a href="https://goreportcard.com/badge/github.com/nerdneilsfield/telegram-fal-bot" target="_blank"><img src="https://goreportcard.com/badge/github.com/nerdneilsfield/telegram-fal-bot" alt="Go Report Card"></a>
<a href="https://github.com/nerdneilsfield/telegram-fal-bot/actions/workflows/goreleaser.yml" target="_blank"><img src="https://github.com/nerdneilsfield/telegram-fal-bot/actions/workflows/goreleaser.yml/badge.svg" alt="CI" /></a>
</div>

A Telegram bot integrated with the Fal AI API to provide AI-powered features like image generation and captioning directly within Telegram chats.

## Features

* **Fal AI Integration:** Leverages Fal AI for functionalities such as:
  * Image generation (potentially using different LoRA models).
  * Image captioning.
* **Telegram Bot Interface:** Interact with the bot using standard Telegram commands and inline keyboards.
* **Configuration:** Easily configurable via a `config.toml` file.
* **Authorization:** Control bot access via user IDs and user groups. Admins have special privileges.
* **Multi-Language Support:** Bot responses can be configured for different languages (e.g., English, Chinese).
* **Persistence:** Uses an SQLite database (`dbPath`) to store user data, generation settings, and balances.
* **State Management:** Handles multi-step operations like LoRA selection and configuration updates.
* **Balance System (Optional):** Track user usage with an optional balance system (`[balance]`).
* **Flexible LoRA Management:**
  * Define multiple standard LoRA styles (`[[loras]]`) with names, URLs, weights, and group-based access control (`allowGroups`).
  * Define Base LoRAs (`[[baseLoRAs]]`) that can be optionally applied (currently implicitly handled by backend logic or explicitly by admins).
* **Customizable Generation:** Users can set personal default generation parameters (`/myconfig`) overriding the global defaults (`[defaultGenerationSettings]`).

## Commands

* `/start`: Greets the user and provides initial instructions.
* `/help`: Displays a detailed help message outlining usage and commands.
* `/cancel`: Cancels the current multi-step operation (e.g., LoRA selection, configuration update).
* `/balance`: Shows the user's current usage balance (if enabled). Admins also see the underlying Fal.ai account balance.
* `/loras`: Lists the LoRA styles available to the user based on their group permissions. Admins see all standard and base LoRAs.
* `/version`: Displays the bot's version, build date, and Go runtime version.
* `/myconfig`: Allows users to view and modify their personal generation settings (Image Size, Inference Steps, Guidance Scale, Number of Images, Language) via an interactive menu. These settings override the global defaults.
* `/set`: (Admin Only) Placeholder for future administrator commands (e.g., managing users, balances, or bot settings). Currently under development.

## Getting Started

1. **Prerequisites:**
    * Go Programming Language environment (refer to official Go documentation for installation).
    * Access to a Telegram Bot Token (create a bot via @BotFather on Telegram).
    * A Fal.ai API Key (sign up at [https://fal.ai/](https://fal.ai/)).
2. **Clone the Repository:**

    ```bash
    git clone <repository_url>
    cd <repository_directory>
    ```

3. **Configuration (`config.toml`):**
    * Copy or rename `config.example.toml` to `config.toml`.
    * **Fill in Required Values:**
        * `botToken`: Your Telegram Bot Token.
        * `falAIKey`: Your Fal.ai API Key.
        * `dbPath`: Path for the SQLite database file (e.g., `"botdata.db"`).
        * `auth.authorizedUserIDs`: List of Telegram user IDs allowed to use the bot (obtain IDs via bots like @userinfobot).
        * `admins.adminUserIDs`: List of Telegram user IDs designated as administrators.
        * `[[loras]]`: Define at least one selectable LoRA style with its `name` and `url`.
    * **Review and Adjust Optional Values:**
        * `telegramAPIURL`: Use default unless you need a custom Telegram API endpoint.
        * `defaultLanguage`: Set the default bot response language (e.g., `"en"`, `"zh"`).
        * `[logConfig]`: Configure logging level, format, and optional file output.
        * `[apiEndpoints]`: Verify or update the `baseURL`, `fluxLora`, and `florenceCaption` endpoints if they differ from the defaults.
        * `[[userGroups]]`: Define user groups and assign user IDs if you need group-based LoRA access control.
        * `[balance]`: Configure the `initialBalance` and `costPerGeneration` if using the balance system.
        * `[defaultGenerationSettings]`: Set global defaults for `imageSize`, `numInferenceSteps`, `guidanceScale`, and `numImages`.
        * `[[baseLoRAs]]`: Define base LoRAs if your workflow utilizes them.
4. **Build:**

    ```bash
    go build -o telegram-fal-bot ./cmd/bot/
    ```

    (Adjust the build command based on your project structure if needed).
5. **Run:**

    ```bash
    ./telegram-fal-bot start ./config.toml
    ```

## Docker Usage

Pre-built Docker images are available on Docker Hub and GitHub Container Registry:

* Docker Hub: `nerdneilsfield/telegram-fal-bot:latest`
* GHCR: `ghcr.io/nerdneilsfield/telegram-fal-bot:latest`

To run the bot using Docker:

1. **Prepare your `config.toml` file** as described in the "Getting Started" section.
2. **Create directories** on your host machine for persistent data (database and logs, if configured):

    ```bash
    mkdir -p ./bot_data ./bot_logs
    ```

3. **Run the container**, mounting your configuration and data directories:

    ```bash
    docker run -d --name fal-bot \
      -v $(pwd)/config.toml:/app/config.toml:ro \
      -v $(pwd)/bot_data:/app/data \
      -v $(pwd)/bot_logs:/app/logs \
      --restart unless-stopped \
      nerdneilsfield/telegram-fal-bot:latest start /app/config.toml
    ```

    * Replace `nerdneilsfield/telegram-fal-bot:latest` with `ghcr.io/nerdneilsfield/telegram-fal-bot:latest` if using GHCR.
    * `-d`: Run in detached mode.
    * `--name fal-bot`: Assign a name to the container.
    * `-v $(pwd)/config.toml:/app/config.toml:ro`: Mount your local `config.toml` as read-only into the container at `/app/config.toml`.
    * `-v $(pwd)/bot_data:/app/data`: Mount a local directory `bot_data` into the container at `/app/data`. **Ensure the `dbPath` in your `config.toml` points to a file within this directory (e.g., `dbPath = "data/botdata.db"`)**.
    * `-v $(pwd)/bot_logs:/app/logs`: Mount a local directory `bot_logs` into the container at `/app/logs`. **If you configure file logging in `config.toml`, ensure the `logConfig.file` path points within this directory (e.g., `file = "logs/bot.log"`)**.
    * `--restart unless-stopped`: Automatically restart the container unless manually stopped.
    * The final arguments `start /app/config.toml` tell the bot executable inside the container to start using the mounted config file.

**Important Considerations:**

* **File Paths in `config.toml`:** When running inside Docker, the paths specified in `config.toml` for `dbPath` and `logConfig.file` **must** be relative to the container's filesystem, specifically within the mounted volumes. The examples above (`data/botdata.db`, `logs/bot.log`) assume you mount host directories to `/app/data` and `/app/logs` respectively.
* **Permissions:** Ensure the Docker daemon has the necessary permissions to read your `config.toml` and write to the `bot_data` and `bot_logs` directories on the host.

## Configuration (`config.toml`) Details

The bot's behavior is controlled by the `config.toml` file.

* **`botToken` (string, Required):** Your Telegram Bot Token.
* **`falAIKey` (string, Required):** Your Fal.ai API Key.
* **`telegramAPIURL` (string, Optional):** Custom Telegram API endpoint (default: `"https://api.telegram.org/bot%s/%s"`). The `%s` placeholders are for the token and method.
* **`dbPath` (string, Required):** Path to the SQLite database file (e.g., `"botdata.db"`).
* **`defaultLanguage` (string, Required):** Default language code for bot responses (e.g., `"en"`, `"zh"`). Must match a language file in your i18n bundle.

* **`[logConfig]`:**
  * `level` (string): Logging level (`"debug"`, `"info"`, `"warn"`, `"error"`).
  * `format` (string): Log format (`"json"` or `"text"`).
  * `file` (string, Optional): Path to log file. Logs to console if empty.

* **`[apiEndpoints]`:** URLs for Fal.ai services.
  * `baseURL` (string): Base URL for Fal.ai API (e.g., `"https://queue.fal.run"`).
  * `fluxLora` (string): Relative path/identifier for the image generation endpoint (e.g., `"fal-ai/flux-lora"`).
  * `florenceCaption` (string): Relative path/identifier for the image captioning endpoint (e.g., `"fal-ai/florence-2-base"`).

* **`[auth]`:** Authorization settings.
  * `authorizedUserIDs` ([]int64, Required): List of Telegram User IDs allowed to use the bot.

* **`[admins]`:** Administrator settings.
  * `adminUserIDs` ([]int64, Required): List of Telegram User IDs with admin privileges (receive detailed errors, access admin commands).

* **`[[userGroups]]` (Optional Array):** Define user groups for fine-grained access control.
  * `name` (string): Unique name for the group (e.g., `"vip"`, `"testers"`).
  * `userIDs` ([]int64): List of Telegram user IDs belonging to this group.

* **`[balance]` (Optional):** Configure the usage balance system.
  * `initialBalance` (float64): Balance assigned to new users.
  * `costPerGeneration` (float64): Cost deducted per LoRA generation request. Set <= 0 to disable balance tracking.

* **`[defaultGenerationSettings]`:** Default parameters for image generation, used if a user hasn't set personal defaults via `/myconfig`.
  * `imageSize` (string): Default aspect ratio (e.g., `"portrait_16_9"`, `"square"`, `"landscape_16_9"`).
  * `numInferenceSteps` (int): Default inference steps (e.g., 25). Range typically 1-50.
  * `guidanceScale` (float64): Default guidance scale (e.g., 7.5). Range typically 0-15.
  * `numImages` (int): Default number of images generated per request (e.g., 1). Range typically 1-10.

* **`[[baseLoRAs]]` (Optional Array):** Define Base LoRAs. These might be applied implicitly by the generation logic or selected explicitly (e.g., by admins).
  * `name` (string): Internal or user-facing name.
  * `url` (string): Fal.ai URL/identifier for the Base LoRA.
  * `weight` (float64): Default weight/scale for this Base LoRA.
  * `allowGroups` ([]string, Optional): Restrict implicit usage or visibility to specific user groups (defined in `[[userGroups]]`).

* **`[[loras]]` (Required Array - At least one):** Define the primary, selectable LoRA styles.
  * `name` (string): User-friendly name displayed in the bot's selection keyboard.
  * `url` (string): Fal.ai URL/identifier for this specific LoRA.
  * `weight` (float64): Default weight/scale for this LoRA style.
  * `allowGroups` ([]string, Optional): Restrict visibility/selection of this style to specific user groups. If empty or omitted, the style is available to all authorized users.

## Usage Flow

1. **Initiate:** Start a chat with the bot or use `/start`.
2. **Image Input:**
    * Send an image directly to the bot.
    * The bot will attempt to generate a caption using the `florenceCaption` endpoint.
    * It will present the caption and ask for confirmation via inline buttons (`Confirm Generation`, `Cancel`).
    * If confirmed, proceeds to LoRA selection (Step 4).
3. **Text Input:**
    * Send a text prompt directly to the bot.
    * Proceeds directly to LoRA selection (Step 4).
4. **LoRA Selection:**
    * The bot displays an inline keyboard showing the standard LoRA styles (`[[loras]]`) available to you. Selected LoRAs are marked with a checkmark.
    * Select one or more standard LoRAs.
    * Click the "Next Step" button.
5. **Base LoRA Selection (Optional/Admin):**
    * If applicable (e.g., you are an admin or specific Base LoRAs are configured for visibility), a second keyboard appears.
    * Select **at most one** Base LoRA (`[[baseLoRAs]]`) or choose to "Skip/Deselect".
    * Click the "Confirm Generation" button.
6. **Generation:**
    * The bot confirms the selected prompt and LoRA combination(s).
    * It submits generation requests to the `fluxLora` endpoint. Balance is checked and deducted here if enabled.
    * A status message indicates progress.
7. **Results:**
    * Generated images are sent back (as single photos or media groups).
    * A final message includes the original prompt, successful/failed LoRA combinations, generation time, and remaining balance (if applicable).
    * Errors during generation are reported.

**Other Interactions:**

* Use `/myconfig` at any time to manage your personal generation settings.
* Use `/cancel` during LoRA selection or configuration input to abort the process.
* Use `/help` for a reminder of commands and usage.

## Contributing

Contributions are welcome! This project is developed using Go. We use `just` (see `justfile`) to streamline common development tasks.

**Getting Started:**

1. Fork the repository.
2. Clone your fork: `git clone <your-fork-url>`
3. Navigate to the project directory: `cd telegram-fal-bot`
4. Set up dependencies (if needed, check `just bootstrap`): `go mod tidy`
5. Make your changes.

**Development Workflow:**

* **Formatting:** Run `just fmt` to format your code according to project standards (uses `gofumpt` and `gci`).
* **Linting:** Run `just lint` to check for code style issues using `golangci-lint`.
* **Building:** Run `just build` to compile the application.
* **Testing:** Run `just test` to execute the test suite and check coverage. Ensure tests pass before submitting a pull request.
* **Cleaning:** Run `just clean` to remove build artifacts.

**Types of Contributions:**

* **Feature Ideas & Implementation:** Have an idea for a new feature? Feel free to open an issue to discuss it or submit a pull request with your implementation.
* **Bug Fixes:** If you find a bug, please open an issue detailing the problem or submit a pull request with the fix.
* **Translations:** Help improve the bot's multi-language support by adding new language files (following the existing i18n structure) or correcting existing translations.

**Pull Requests:**

1. Create a new branch for your feature or bug fix.
2. Commit your changes with clear and concise messages.
3. Ensure your code is formatted (`just fmt`) and linted (`just lint`).
4. Make sure all tests pass (`just test`).
5. Push your branch to your fork.
6. Open a pull request against the `main` branch of the original repository.

We appreciate your help in making this bot better!

## License

This project is licensed under the MIT License.

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=nerdneilsfield/telegram-fal-bot/&type=Date)](https://star-history.com/nerdneilsfield/telegram-fal-bot&Date)
