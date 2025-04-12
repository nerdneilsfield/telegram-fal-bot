# Telegram Fal Bot (电报 Fal AI 机器人)

[English]((https://github.com/nerdneilsfield/telegram-fal-bot/blob/master/README.md)) | 中文

<div align="center">

<a href="https://pkg.go.dev/github.com/nerdneilsfield/telegram-fal-bot" target="_blank"><img src="https://pkg.go.dev/badge/github.com/nerdneilsfield/telegram-fal-bot/bot.svg" alt="Go Reference"></a>
<a href="https://goreportcard.com/badge/github.com/nerdneilsfield/telegram-fal-bot" target="_blank"><img src="https://goreportcard.com/badge/github.com/nerdneilsfield/telegram-fal-bot" alt="Go Report Card"></a>
<a href="https://github.com/nerdneilsfield/telegram-fal-bot/actions/workflows/goreleaser.yml" target="_blank"><img src="https://github.com/nerdneilsfield/telegram-fal-bot/actions/workflows/goreleaser.yml/badge.svg" alt="CI" /></a>
</div>

<div align="center">

<a href="https://hub.docker.com/r/nerdneils/telegram-fal-bot" target="_blank"><img src="https://img.shields.io/docker/pulls/nerdneils/telegram-fal-bot" alt="Docker Pulls"></a>
<a href="https://hub.docker.com/r/nerdneils/telegram-fal-bot" target="_blank"><img src="https://img.shields.io/docker/image-size/nerdneils/telegram-fal-bot/latest" alt="Docker Image Size"></a>
<a href="https://github.com/nerdneilsfield/telegram-fal-bot/pkgs/container/telegram-fal-bot" target="_blank"><img src="https://img.shields.io/github/docker/image-size/nerdneilsfield/telegram-fal-bot?label=GHCR%20Image%20Size&logo=github&registry_uri=ghcr.io" alt="GHCR Image Size" /></a>
<a href="https://github.com/nerdneilsfield/telegram-fal-bot/releases/latest" target="_blank"><img src="https://img.shields.io/github/release/nerdneilsfield/telegram-fal-bot.svg" alt="GitHub Release" /></a>
<a href="https://github.com/nerdneilsfield/telegram-fal-bot/releases/latest" target="_blank"><img src="https://img.shields.io/github/release-date/nerdneilsfield/telegram-fal-bot.svg" alt="GitHub Release Date" /></a>
<a href="https://github.com/nerdneilsfield/telegram-fal-bot/releases/latest" target="_blank"><img src="https://img.shields.io/github/downloads/nerdneilsfield/telegram-fal-bot/total.svg" alt="GitHub Downloads" /></a>

</div>

一个集成了 Fal AI API 的 Telegram 机器人，可以直接在 Telegram 聊天中提供 AI 驱动的功能，如图像生成和图像描述。

## 功能特性

* **Fal AI 集成:** 利用 Fal AI 实现以下功能：
  * 图像生成 (可能使用不同的 LoRA 模型)。
  * 图像描述。
* **Telegram Bot 接口:** 使用标准的 Telegram 命令和内联键盘与机器人交互。
* **配置:** 通过 `config.toml` 文件轻松配置。
* **授权:** 通过用户 ID 和用户组控制机器人访问。管理员拥有特殊权限。
* **多语言支持:** 可以为不同的语言（例如英语、中文）配置机器人回复。
* **持久化:** 使用 SQLite 数据库 (`dbPath`) 存储用户数据、生成设置和余额。
* **状态管理:** 处理多步骤操作，如 LoRA 选择和配置更新。
* **余额系统 (可选):** 使用可选的余额系统 (`[balance]`) 跟踪用户使用情况。
* **灵活的 LoRA 管理:**
  * 定义多种标准 LoRA 风格 (`[[loras]]`)，包含名称、URL、权重和基于组的访问控制 (`allowGroups`)。
  * 定义基础 LoRA (`[[baseLoRAs]]`)，可以被选择性应用（当前由后端逻辑隐式处理或由管理员显式选择）。
* **可定制生成:** 用户可以通过 `/myconfig` 设置个人默认生成参数，覆盖全局默认设置 (`[defaultGenerationSettings]`)。

## 命令

* `/start`: 向用户问好并提供初始说明。
* `/help`: 显示详细的帮助信息，概述用法和命令。
* `/cancel`: 取消当前的多步骤操作（例如 LoRA 选择、配置更新）。
* `/balance`: 显示用户当前的使用余额（如果启用）。管理员还可以看到底层的 Fal.ai 账户余额。
* `/loras`: 列出用户根据其组权限可用的 LoRA 风格。管理员可以看到所有标准和基础 LoRA。
* `/version`: 显示机器人的版本、构建日期和 Go 运行时版本。
* `/myconfig`: 允许用户通过交互式菜单查看和修改其个人生成设置（图像尺寸、推理步数、引导比例、图像数量、语言）。这些设置会覆盖全局默认值。
* `/set`: (仅管理员) 用于未来管理员命令的占位符（例如管理用户、余额或机器人设置）。目前正在开发中。

## 开始使用

1. **先决条件:**
    * Go 编程语言环境（安装请参考 Go 官方文档）。
    * Telegram Bot Token（通过 Telegram 上的 @BotFather 创建机器人获取）。
    * Fal.ai API Key（在 [https://fal.ai/](https://fal.ai/) 注册获取）。
2. **克隆仓库:**

    ```bash
    git clone <repository_url>
    cd <repository_directory>
    ```

3. **配置 (`config.toml`):**
    * 复制或重命名 `config.example.toml` 为 `config.toml`。
    * **填写必需值:**
        * `botToken`: 你的 Telegram Bot Token。
        * `falAIKey`: 你的 Fal.ai API Key。
        * `dbPath`: SQLite 数据库文件的路径（例如 `"botdata.db"`）。
        * `auth.authorizedUserIDs`: 允许使用机器人的 Telegram 用户 ID 列表（可通过 @userinfobot 等机器人获取 ID）。
        * `admins.adminUserIDs`: 指定为管理员的 Telegram 用户 ID 列表。
        * `[[loras]]`: 定义至少一个可选的 LoRA 风格，包含其 `name` 和 `url`。
    * **检查并调整可选值:**
        * `telegramAPIURL`: 除非需要自定义 Telegram API 端点，否则使用默认值。
        * `defaultLanguage`: 设置机器人回复的默认语言（例如 `"en"`, `"zh"`）。
        * `[logConfig]`: 配置日志级别、格式和可选的文件输出。
        * `[apiEndpoints]`: 验证或更新 `baseURL`, `fluxLora`, `florenceCaption` 端点，如果它们与默认值不同。
        * `[[userGroups]]`: 如果需要基于组的 LoRA 访问控制，定义用户组并分配用户 ID。
        * `[balance]`: 如果使用余额系统，配置 `initialBalance` 和 `costPerGeneration`。
        * `[defaultGenerationSettings]`: 设置 `imageSize`, `numInferenceSteps`, `guidanceScale`, `numImages` 的全局默认值。
        * `[[baseLoRAs]]`: 如果你的工作流程使用基础 LoRA，请定义它们。
4. **构建:**

    ```bash
    go build -o telegram-fal-bot ./cmd/bot/
    ```

    （如果需要，根据你的项目结构调整构建命令）。
5. **运行:**

    ```bash
    ./telegram-fal-bot start ./config.toml
    ```

## Docker 使用

预构建的 Docker 镜像可在 Docker Hub 和 GitHub Container Registry 上获取：

* Docker Hub: `nerdneils/telegram-fal-bot:latest`
* GHCR: `ghcr.io/nerdneilsfield/telegram-fal-bot:latest`

要使用 Docker 运行机器人：

1. **准备你的 `config.toml` 文件**，如"开始使用"部分所述。
2. 在你的主机上**创建用于持久化数据（数据库和日志，如果已配置）的目录**：

    ```bash
    mkdir -p ./bot_data ./bot_logs
    ```

3. **运行容器**，挂载你的配置和数据目录：

    ```bash
    docker run -d --name fal-bot \
      -v $(pwd)/config.toml:/app/config.toml:ro \
      -v $(pwd)/bot_data:/app/data \
      -v $(pwd)/bot_logs:/app/logs \
      --restart unless-stopped \
      nerdneils/telegram-fal-bot:latest start /app/config.toml
    ```

    * 如果使用 GHCR，请将 `nerdneils/telegram-fal-bot:latest` 替换为 `ghcr.io/nerdneilsfield/telegram-fal-bot:latest`。
    * `-d`: 在后台模式运行。
    * `--name fal-bot`: 为容器分配一个名称。
    * `-v $(pwd)/config.toml:/app/config.toml:ro`: 将你本地的 `config.toml` 以只读方式挂载到容器内的 `/app/config.toml`。
    * `-v $(pwd)/bot_data:/app/data`: 将本地目录 `bot_data` 挂载到容器内的 `/app/data`。**请确保你的 `config.toml` 中的 `dbPath` 指向此目录下的文件（例如 `dbPath = "data/botdata.db"`）**。
    * `-v $(pwd)/bot_logs:/app/logs`: 将本地目录 `bot_logs` 挂载到容器内的 `/app/logs`。**如果你在 `config.toml` 中配置了文件日志，请确保 `logConfig.file` 路径指向此目录内（例如 `file = "logs/bot.log"`）**。
    * `--restart unless-stopped`: 除非手动停止，否则自动重启容器。
    * 最后的参数 `start /app/config.toml` 告诉容器内的机器人可执行文件使用挂载的配置文件启动。

**重要注意事项:**

* **`config.toml` 中的文件路径:** 在 Docker 内部运行时，`config.toml` 中为 `dbPath` 和 `logConfig.file` 指定的路径**必须**是相对于容器文件系统的路径，特别是在挂载的卷内。上面的示例（`data/botdata.db`、`logs/bot.log`）假定你分别将主机目录挂载到了 `/app/data` 和 `/app/logs`。
* **权限:** 确保 Docker 守护进程具有读取你的 `config.toml` 以及向主机上的 `bot_data` 和 `bot_logs` 目录写入的必要权限。

## 配置 (`config.toml`) 详解

机器人的行为由 `config.toml` 文件控制。

* **`botToken` (字符串, 必需):** 你的 Telegram Bot Token。
* **`falAIKey` (字符串, 必需):** 你的 Fal.ai API Key。
* **`telegramAPIURL` (字符串, 可选):** 自定义 Telegram API 端点（默认：`"https://api.telegram.org/bot%s/%s"`）。`%s` 占位符分别用于 token 和方法。
* **`dbPath` (字符串, 必需):** SQLite 数据库文件的路径（例如 `"botdata.db"`）。
* **`defaultLanguage` (字符串, 必需):** 机器人回复的默认语言代码（例如 `"en"`, `"zh"`）。必须与 i18n 包中的语言文件匹配。

* **`[logConfig]` (日志配置):**
  * `level` (字符串): 日志级别 (`"debug"`, `"info"`, `"warn"`, `"error"`)。
  * `format` (字符串): 日志格式 (`"json"` 或 `"text"`)。
  * `file` (字符串, 可选): 日志文件路径。如果为空则输出到控制台。

* **`[apiEndpoints]` (API 端点):** Fal.ai 服务的 URL。
  * `baseURL` (字符串): Fal.ai API 的基础 URL（例如 `"https://queue.fal.run"`）。
  * `fluxLora` (字符串): 图像生成端点的相对路径/标识符（例如 `"fal-ai/flux-lora"`）。
  * `florenceCaption` (字符串): 图像描述端点的相对路径/标识符（例如 `"fal-ai/florence-2-base"`）。

* **`[auth]` (授权):** 授权设置。
  * `authorizedUserIDs` ([]int64, 必需): 允许使用机器人的 Telegram 用户 ID 列表。

* **`[admins]` (管理员):** 管理员设置。
  * `adminUserIDs` ([]int64, 必需): 拥有管理员权限的 Telegram 用户 ID 列表（接收详细错误、访问管理员命令）。

* **`[[userGroups]]` (用户组, 可选数组):** 定义用户组以实现精细访问控制。
  * `name` (字符串): 组的唯一名称（例如 `"vip"`, `"testers"`）。
  * `userIDs` ([]int64): 属于此组的 Telegram 用户 ID 列表。

* **`[balance]` (余额系统, 可选):** 配置使用余额系统。
  * `initialBalance` (浮点数): 分配给新用户的余额。
  * `costPerGeneration` (浮点数): 每次 LoRA 生成请求扣除的费用。设置 <= 0 以禁用余额跟踪。

* **`[defaultGenerationSettings]` (默认生成设置):** 图像生成的默认参数，在用户未通过 `/myconfig` 设置个人默认值时使用。
  * `imageSize` (字符串): 默认宽高比（例如 `"portrait_16_9"`, `"square"`, `"landscape_16_9"`）。
  * `numInferenceSteps` (整数): 默认推理步数（例如 25）。范围通常为 1-50。
  * `guidanceScale` (浮点数): 默认引导比例（例如 7.5）。范围通常为 0-15。
  * `numImages` (整数): 每次请求默认生成的图像数量（例如 1）。范围通常为 1-10。

* **`[[baseLoRAs]]` (基础 LoRA, 可选数组):** 定义基础 LoRA。这些可能由生成逻辑隐式应用或显式选择（例如由管理员）。
  * `name` (字符串): 内部或面向用户的名称。
  * `url` (字符串): 基础 LoRA 在 Fal.ai 上的 URL/标识符。
  * `weight` (浮点数): 此基础 LoRA 的默认权重/比例。
  * `allowGroups` ([]string, 可选): 将隐式使用或可见性限制在特定用户组（在 `[[userGroups]]` 中定义）。

* **`[[loras]]` (LoRA 风格, 必需数组 - 至少一个):** 定义主要的、可选择的 LoRA 风格。
  * `name` (字符串): 在机器人的选择键盘中显示的用户友好名称。
  * `url` (字符串): 此特定 LoRA 在 Fal.ai 上的 URL/标识符。
  * `weight` (浮点数): 此 LoRA 风格的默认权重/比例。
  * `allowGroups` ([]string, 可选): 将此风格的可见性/选择限制在特定用户组。如果为空或省略，则该风格对所有授权用户可用。

## 使用流程

1. **启动:** 与机器人开始聊天或使用 `/start`。
2. **图像输入:**
    * 直接向机器人发送图像。
    * 机器人将尝试使用 `florenceCaption` 端点生成描述。
    * 它将显示描述并通过内联按钮（`确认生成`, `取消`）请求确认。
    * 如果确认，则进入 LoRA 选择（步骤 4）。
3. **文本输入:**
    * 直接向机器人发送文本提示。
    * 直接进入 LoRA 选择（步骤 4）。
4. **LoRA 选择:**
    * 机器人显示一个内联键盘，其中包含对你可用的标准 LoRA 风格 (`[[loras]]`)。选定的 LoRA 会标有复选标记。
    * 选择一个或多个标准 LoRA。
    * 点击"下一步"按钮。
5. **基础 LoRA 选择 (可选/管理员):**
    * 如果适用（例如，你是管理员或配置了特定的基础 LoRA 可见性），则会出现第二个键盘。
    * 最多选择 **一个** 基础 LoRA (`[[baseLoRAs]]`) 或选择"跳过/取消选择"。
    * 点击"确认生成"按钮。
6. **生成:**
    * 机器人确认所选的提示和 LoRA 组合。
    * 它向 `fluxLora` 端点提交生成请求。如果启用，将在此处检查并扣除余额。
    * 状态消息指示进度。
7. **结果:**
    * 生成的图像被发送回来（作为单张照片或媒体组）。
    * 最终消息包括原始提示、成功/失败的 LoRA 组合、生成时间以及剩余余额（如果适用）。
    * 报告生成过程中的错误。

**其他交互:**

* 随时使用 `/myconfig` 管理你的个人生成设置。
* 在 LoRA 选择或配置输入期间使用 `/cancel` 中止该过程。
* 使用 `/help` 获取命令和用法的提醒。

## 贡献

欢迎贡献！本项目使用 Go 开发。我们使用 `just`（参见 `justfile`）来简化常见的开发任务。

**开始贡献:**

1. Fork 本仓库。
2. 克隆你的 fork：`git clone <your-fork-url>`
3. 进入项目目录：`cd telegram-fal-bot`
4. 设置依赖项（如果需要，请检查 `just bootstrap`）：`go mod tidy`
5. 进行你的修改。

**开发流程:**

* **格式化:** 运行 `just fmt` 以根据项目标准格式化你的代码（使用 `gofumpt` 和 `gci`）。
* **代码检查 (Linting):** 运行 `just lint` 以使用 `golangci-lint` 检查代码风格问题。
* **构建:** 运行 `just build` 来编译应用程序。
* **测试:** 运行 `just test` 来执行测试套件并检查覆盖率。在提交 Pull Request 前请确保所有测试通过。
* **清理:** 运行 `just clean` 来移除构建产物。

**贡献类型:**

* **功能建议与实现:** 对新功能有想法？欢迎提交 Issue 进行讨论或直接提交包含你实现的 Pull Request。
* **Bug 修复:** 如果你发现了一个 Bug，请提交 Issue 详细说明问题或提交包含修复的 Pull Request。
* **翻译:** 通过添加新的语言文件（遵循现有的 i18n 结构）或修正现有翻译，帮助改进机器人的多语言支持。

**Pull Request:**

1. 为你的功能或 Bug 修复创建一个新的分支。
2. 使用清晰简洁的消息提交你的更改。
3. 确保你的代码已格式化 (`just fmt`) 并通过了代码检查 (`just lint`)。
4. 确保所有测试通过 (`just test`)。
5. 将你的分支推送到你的 fork。
6. 针对原始仓库的 `main` 分支开启一个 Pull Request。

感谢你帮助我们改进这个机器人！

## 许可证

本项目采用 MIT 许可证授权。

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=nerdneilsfield/telegram-fal-bot/&type=Date)](https://star-history.com/nerdneilsfield/telegram-fal-bot&Date)
