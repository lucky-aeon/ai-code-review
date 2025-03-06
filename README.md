# Bot Code Review

这是一个GitHub PR代码评审机器人，使用AI技术自动检查代码问题并添加评论。

## 功能特点

- 通过GitHub Webhook监听Pull Request事件
- 使用AI模型分析代码变更，发现潜在问题
- 自动添加行内评论到Pull Request
- 支持多种编程语言的代码审查
- 特别关注数组越界、变量重复声明等常见问题

## 项目结构

```
.
├── config/          # 配置相关代码
│   └── config.go    # 配置结构和加载函数
├── github/          # GitHub API相关代码
│   └── api.go       # GitHub API调用函数
├── handlers/        # 请求处理相关代码
│   ├── reviewer.go  # 代码审查逻辑
│   └── webhook.go   # Webhook处理函数
├── models/          # 数据模型
│   └── models.go    # 请求和响应结构体
├── utils/           # 工具函数
│   ├── ai.go        # AI调用和结果解析
│   └── diff.go      # 差异分析工具
├── config.json      # 配置文件（需要自行创建）
├── go.mod           # Go模块定义
└── main.go          # 主程序入口
```

## 配置文件

创建`config.json`文件，格式如下：

```json
{
  "github_token": "your_github_personal_access_token",
  "webhook_port": "8080",
  "github_host": "https://api.github.com",
  "api_key": "your_ai_api_key",
  "model": "your_ai_model",
  "base_url": "your_ai_api_endpoint"
}
```

## 如何使用

1. 克隆仓库
```bash
git clone https://github.com/yourusername/bot-code-review.git
cd bot-code-review
```

2. 创建配置文件

3. 启动服务
```bash
go run main.go
```

4. 在GitHub仓库中配置Webhook
   - Webhook URL: `http://your-server-address:8080/webhook`
   - 事件类型: 选择 "Pull requests"

## 依赖项

- Go 1.17+ 