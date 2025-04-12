# Telegram Fal Bot (电报 Fal AI 机器人)

一个集成了 Fal AI API 的 Telegram 机器人，可以直接在 Telegram 聊天中提供 AI 驱动的功能，如图像生成和图像描述。

## 功能特性

*   **Fal AI 集成:** 利用 Fal AI 实现以下功能：
    *   图像生成 (可能使用不同的 LoRA 模型)。
    *   图像描述。
*   **Telegram Bot 接口:** 使用标准的 Telegram 命令与机器人交互。
*   **配置:** 通过 `config.toml` 文件轻松配置。
*   **授权:** 通过用户 ID 控制谁可以使用机器人。
*   **持久化:** 使用数据库 (例如 SQLite) 存储用户数据和可能的使用情况/余额。
*   **命令:**
    *   `/start`: 开始与机器人交互。
    *   `/help`: 获取帮助信息。
    *   `/cancel`: 取消当前操作。
    *   `/balance`: 查询使用余额 (如果启用)。
    *   `/loras`: 查看可用的 LoRA 模型/风格。
    *   `/version`: 显示机器人版本信息。
    *   `/myconfig`: 查看或设置个人生成参数。
    *   `/set`: (管理员) 管理用户和权限。

## 开始使用

**(注意: 在此添加具体的设置说明)**

1.  **先决条件:** Go 环境等。
2.  **配置:**
    *   将 `config.example.toml` (如果提供) 复制为 `config.toml`。
    *   在 `config.toml` 中填写你的 `BotToken`, `FalAIKey`, 授权用户 ID, 管理员用户 ID, 以及 Fal API 端点。
    *   根据需要配置数据库路径 (`DBPath`) 和其他设置。
3.  **构建:** `go build -o telegram-fal-bot` (或使用提供的 Makefile/justfile)。
4.  **运行:** `./telegram-fal-bot start ./config.toml`

## 配置 (`config.toml`)

机器人的行为由 `config.toml` 文件控制。以下是关键部分和选项：

*   **`botToken` (字符串, 必需):** 你从 BotFather 获取的 Telegram 机器人令牌。
*   **`falAIKey` (字符串, 必需):** 你从 Fal.ai 获取的 API 密钥。
*   **`telegramAPIURL` (字符串, 可选):** 自定义 Telegram API 端点。默认为官方 Telegram API URL。
*   **`dbPath` (字符串, 必需):** 用于存储用户数据、余额等的 SQLite 数据库文件路径 (例如 `botdata.db`)。
*   **`[logConfig]` (日志配置):**
    *   `level` (字符串): 日志级别 (`"debug"`, `"info"`, `"warn"`, `"error"`)。
    *   `format` (字符串): 日志输出格式 (`"json"` 或 `"text"`)。
    *   `file` (字符串): 日志文件路径。如果为空，则日志输出到控制台。
*   **`[apiEndpoints]` (API 端点):**
    *   `baseURL` (字符串): Fal.ai API 的基础 URL (例如 `"https://queue.fal.run"`)。
    *   `fluxLora` (字符串): Fal.ai Flux LoRA 端点的相对路径/标识符 (例如 `"fal-ai/flux-lora"`)。
    *   `florenceCaption` (字符串): Fal.ai Florence Caption 端点的相对路径/标识符 (例如 `"fal-ai/florence-2-base"`)。
*   **`[auth]` (授权):**
    *   `authorizedUserIDs` ([]int64, 必需): 允许与此机器人交互的 Telegram 用户 ID 列表。
*   **`[admins]` (管理员):**
    *   `adminUserIDs` ([]int64, 必需): 被指定为管理员的 Telegram 用户 ID 列表。管理员会收到详细的错误消息。
*   **`[[userGroups]]` (用户组, 可选):** 定义用户组以管理 LoRA 访问权限。
    *   `name` (字符串): 组名 (例如 `"vip"`)。
    *   `userIDs` ([]int64): 属于此组的用户 ID 列表。
*   **`[balance]` (余额系统, 可选):** 配置使用余额系统。
    *   `initialBalance` (浮点数): 分配给新用户的初始余额。
    *   `costPerGeneration` (浮点数): 每次图像生成扣除的费用。设置为 0 或更小以禁用余额跟踪。
*   **`[defaultGenerationSettings]` (默认生成设置):** 图像生成的默认参数。
    *   `imageSize` (字符串): 默认图像宽高比 (例如 `"portrait_16_9"`, `"square"`)。
    *   `numInferenceSteps` (整数): 默认推理步数 (1-49)。
    *   `guidanceScale` (浮点数): 默认引导比例 (0-15)。
    *   `numImages` (整数): 每次请求默认生成的图像数量。
*   **`[[baseLoRAs]]` (基础 LoRA, 可选):** 定义可能被隐式应用的基础 LoRA。
    *   `name` (字符串): 内部名称。
    *   `url` (字符串): Fal.ai URL/标识符。
    *   `weight` (浮点数): LoRA 权重。
    *   `allowGroups` ([]string): 将隐式使用限制在特定组。
*   **`[[loras]]` (可选 LoRA 风格, 必需 - 至少一个 LoRA 或 BaseLoRA):** 定义用户可选择的 LoRA 风格。
    *   `name` (字符串): 在机器人中显示的用户友好名称。
    *   `url` (字符串): 此 LoRA 在 Fal.ai 上的 URL/标识符。
    *   `weight` (浮点数): 此风格的默认权重。
    *   `allowGroups` ([]string): 将此风格的可见性/使用限制在特定用户组（空表示对所有授权用户公开）。

请确保将 `"YOUR_TELEGRAM_BOT_TOKEN_HERE"` 和 `"YOUR_FAL_AI_KEY_HERE"` 等占位符值替换为你的实际凭据。

## 使用方法

在你的 Telegram 聊天中使用可用命令与机器人进行交互。

## 贡献

*(如果适用，请添加贡献指南)*

## 许可证

本项目采用 MIT 许可证授权。
