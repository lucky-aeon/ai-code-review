# GitHub AI 代码审查机器人

这是一个基于 GitHub API 的 AI 代码审查机器人，可以自动对 Pull Request 中的代码变更进行审查并提供针对性的评论。

## 功能

- 监听 GitHub Pull Request Webhook 事件
- 获取 PR 中的代码变更
- 使用 AI 模型对代码变更进行智能评审
- 在 PR 的具体代码行上添加详细的审查评论
- 支持多种编程语言

## 安装

1. 克隆仓库
   ```
   git clone <repository-url>
   cd <repository-directory>
   ```

2. 复制配置文件模板
   ```
   cp config.json.example config.json
   ```

3. 编辑配置文件 `config.json`，填入以下信息:
   - `github_token`: GitHub 个人访问令牌，需要有 PR 读写权限
   - `webhook_port`: Webhook 服务监听端口
   - `github_host`: GitHub API 地址，一般是 "https://api.github.com"
   - `api_key`: AI 服务的 API 密钥
   - `model`: 使用的 AI 模型名称
   - `base_url`: AI API 的基础 URL

## 运行

```
go run main.go
```

## 配置 GitHub Webhook

1. 在 GitHub 仓库中进入 Settings > Webhooks > Add webhook
2. 设置 Payload URL 为您的服务器地址，例如 `http://your-server.com:8080/webhook`
3. 选择 Content type 为 `application/json`
4. 在 "Which events would you like to trigger this webhook?" 部分选择 "Let me select individual events"，然后勾选 "Pull requests"
5. 点击 "Add webhook" 完成配置

## 工作流程

1. 当用户创建或更新 Pull Request 时，GitHub 发送 webhook 事件到您的服务器
2. 服务器接收事件并获取 PR 中的文件变更
3. AI 模型分析代码变更并生成评审意见
4. 机器人将评审意见以评论形式发布在 PR 的具体代码行上

## 许可

[MIT License](LICENSE) 