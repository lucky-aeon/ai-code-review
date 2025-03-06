package main

import (
	"bot-code-review/config"
	"bot-code-review/handlers"
	"log"
	"net/http"
)

func main() {
	// 读取配置
	if err := config.LoadConfig(); err != nil {
		log.Fatalf("无法加载配置: %v", err)
	}

	// 设置webhook处理路由
	http.HandleFunc("/webhook", handlers.HandleWebhook)

	// 启动HTTP服务器
	port := config.GlobalConfig.WebhookPort
	log.Printf("🚀 服务器已启动在 :%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}
