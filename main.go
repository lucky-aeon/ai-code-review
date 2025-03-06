package main

import (
	"bot-code-review/config"
	"bot-code-review/handlers"
	"fmt"
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

// 用于测试的函数，只在正常模式运行时可用
func test() {
	array := [5]int{1, 2, 3, 4, 5}

	fmt.Println(array[10])
	fmt.Println(12333)
}
