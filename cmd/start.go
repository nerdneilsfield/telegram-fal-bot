package cmd

import (
	"fmt"
	"os"

	"github.com/nerdneilsfield/telegram-fal-bot/internal/bot"
	"github.com/nerdneilsfield/telegram-fal-bot/internal/config"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

func newStartCmd(verbose bool, version string, buildTime string) *cobra.Command {
	return &cobra.Command{
		Use:          "start",
		Short:        "telegram-fal-bot start",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("telegram-fal-bot start")
			fmt.Println("configPath: ", args[0])
			return run(verbose, args[0], version, buildTime)
		},
	}
}

func run(verbose bool, configFile string, version string, buildTime string) error {
	var err error

	// 先初始化一个基本日志记录器，用于记录配置加载过程
	tempLogger, _ := zap.NewProduction()
	defer tempLogger.Sync()

	// 记录是否使用了自定义配置文件
	if configFile != "" {
		tempLogger.Info("使用自定义配置文件", zap.String("path", configFile))
	}

	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		tempLogger.Error("配置文件不存在", zap.String("path", configFile))
	}

	cfg := &config.Config{}

	// 加载配置，优先使用命令行指定的配置文件
	if configFile != "" {
		// check if the file exists
		cfg, err = config.LoadConfig(configFile)
	} else {
		tempLogger.Debug("使用默认配置文件路径")
		cfg, err = config.LoadConfig("./config.toml")
	}

	if err != nil {
		tempLogger.Error("加载配置失败", zap.Error(err))
		return nil
	}

	if err := config.ValidateConfig(cfg); err != nil {
		tempLogger.Error("配置验证失败", zap.Error(err))
		return nil
	}

	if err != nil {
		tempLogger.Error("加载配置失败", zap.Error(err))
		return nil
	}

	bot.StartBot(cfg, version, buildTime)
	return nil
}
