package main

import (
	"log"
	"net/http"
	"os"

	"bot-code-review/config"
	"bot-code-review/handlers"
)

func main() {
	// 读取配置
	if err := config.LoadConfig(); err != nil {
		log.Fatalf("无法加载配置: %v", err)
	}

	// 检查测试模式
	testMode := os.Getenv("TEST_MODE")
	if testMode == "true" {
		log.Println("🧪 启动测试模式")
		handlers.TestArrayBoundsCheck()
		return
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

	// 正确的数组访问
	log.Println("访问有效索引:", array[4]) // 使用有效的索引4（最后一个元素）

	// 下面是错误的数组访问示例，已注释掉以避免编译错误
	// 但保留为测试用例，用于验证代码审查能力
	/*
		log.Println("访问有效索引:", array[10]) // 索引越界：数组长度为5，索引应为0-4
		log.Println("访问有效索引:", array[12]) // 索引越界：数组长度为5，索引应为0-4
	*/

	// 安全的数组访问示例
	index := 10
	if index < len(array) {
		log.Println("安全访问:", array[index])
	} else {
		log.Println("索引", index, "超出范围，数组长度为", len(array))
	}
}
