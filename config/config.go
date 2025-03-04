package config

import (
	"encoding/json"
	"log"
	"os"
)

// Config 结构体用于存储配置信息
type Config struct {
	GithubToken string `json:"github_token"`
	WebhookPort string `json:"webhook_port"`
	GithubHost  string `json:"github_host"`
	APIKey      string `json:"api_key"`
	Model       string `json:"model"`
	BaseURL     string `json:"base_url"`
}

// 全局配置变量
var GlobalConfig Config

// LoadConfig 从配置文件中加载配置
func LoadConfig() error {
	// 读取配置文件
	configFile, err := os.Open("config.json")
	if err != nil {
		return err
	}
	defer configFile.Close()

	// 解码JSON配置到全局config变量
	decoder := json.NewDecoder(configFile)
	if err := decoder.Decode(&GlobalConfig); err != nil {
		return err
	}

	// 设置默认值
	if GlobalConfig.WebhookPort == "" {
		GlobalConfig.WebhookPort = "8080" // 默认端口
	}

	log.Printf("配置加载成功: 端口 %s, 模型 %s", GlobalConfig.WebhookPort, GlobalConfig.Model)
	return nil
}
