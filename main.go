package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
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

// PullRequestEvent GitHub webhook事件结构
type PullRequestEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		URL    string `json:"url"`
		ID     int    `json:"id"`
		Number int    `json:"number"`
		State  string `json:"state"`
		Title  string `json:"title"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
	} `json:"pull_request"`
	Repository struct {
		ID       int    `json:"id"`
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}

// GitHubDiff 表示GitHub API返回的差异信息
type GitHubDiff struct {
	Filename    string `json:"filename"`
	Status      string `json:"status"`
	Additions   int    `json:"additions"`
	Deletions   int    `json:"deletions"`
	Changes     int    `json:"changes"`
	Patch       string `json:"patch"`
	BlobURL     string `json:"blob_url"`
	RawURL      string `json:"raw_url"`
	ContentsURL string `json:"contents_url"`
}

var config Config

func main() {
	// 加载配置
	configFile, err := os.Open("config.json")
	if err != nil {
		log.Fatalf("无法打开配置文件: %v", err)
	}
	defer configFile.Close()

	if err := json.NewDecoder(configFile).Decode(&config); err != nil {
		log.Fatalf("解析配置文件失败: %v", err)
	}

	// 设置默认值
	if config.WebhookPort == "" {
		config.WebhookPort = "8080"
	}
	if config.GithubHost == "" {
		config.GithubHost = "https://api.github.com"
	}
	if config.Model == "" {
		config.Model = "gpt-4"
	}

	// 验证必需的配置
	if config.GithubToken == "" {
		log.Fatal("缺少必需的配置: github_token")
	}
	if config.APIKey == "" {
		log.Fatal("缺少必需的配置: api_key")
	}

	http.HandleFunc("/webhook", handleWebhook)

	log.Printf("🚀 GitHub代码审查机器人启动在端口 %s", config.WebhookPort)
	log.Fatal(http.ListenAndServe(":"+config.WebhookPort, nil))
}

func handleWebhook(w http.ResponseWriter, r *http.Request) {
	// 限制请求体大小
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Error reading request body", http.StatusInternalServerError)
		return
	}

	// 验证请求头
	event := r.Header.Get("X-GitHub-Event")
	if event == "" {
		http.Error(w, "Missing X-GitHub-Event header", http.StatusBadRequest)
		return
	}

	// 只处理pull request事件
	if event != "pull_request" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 解析webhook事件
	var prEvent PullRequestEvent
	if err := json.Unmarshal(body, &prEvent); err != nil {
		http.Error(w, "Error parsing webhook payload", http.StatusBadRequest)
		return
	}

	// 只处理pull request事件，且必须是opened或synchronize状态
	if prEvent.Action != "opened" && prEvent.Action != "synchronize" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 启动代码评审流程
	go handleCodeReview(prEvent)

	w.WriteHeader(http.StatusOK)
}

// 用于存储文件评论的结构
type FileComments struct {
	Path     string
	Comments []Comment
}

type Comment struct {
	Line    int
	Content string
}

func handleCodeReview(event PullRequestEvent) {
	log.Printf("🚀 开始评审 MR !%d 在项目 %s",
		event.PullRequest.Number, event.Repository.FullName)

	// 获取MR的变更
	changes, err := getMRChanges(event)
	if err != nil {
		log.Printf("❌ 获取MR变更失败: %v", err)
		return
	}

	log.Printf("📝 发现 %d 个文件需要评审", len(changes))

	// 评审每个文件
	reviewedFiles := 0
	commentCount := 0

	for _, change := range changes {
		comments, err := reviewFileChange(change)
		if err != nil {
			log.Printf("❌ 评审文件 %s 失败: %v", change.Filename, err)
			continue
		}

		if len(comments) > 0 {
			reviewedFiles++
			commentCount += len(comments)
			log.Printf("🔍 在 %s 中发现 %d 个问题", change.Filename, len(comments))
		} else {
			log.Printf("✅ 文件 %s 未发现问题", change.Filename)
		}

		// 对每个评论创建单独的note
		for _, comment := range comments {
			if err := createNote(event, change, comment); err != nil {
				log.Printf("❌ 创建评论失败 %s 行 %d: %v", change.Filename, comment.Line, err)
			} else {
				log.Printf("💬 已在 %s 行 %d 创建评论", change.Filename, comment.Line)
			}
		}
	}

	log.Printf("🏁 评审完成: %d 个文件有问题, 共发布 %d 条评论",
		reviewedFiles, commentCount)
}

func getMRChanges(event PullRequestEvent) ([]GitHubDiff, error) {
	// 构建GitHub API URL，获取Pull Request的文件变更
	url := fmt.Sprintf("%s/repos/%s/pulls/%d/files",
		config.GithubHost,
		event.Repository.FullName,
		event.PullRequest.Number)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	// 设置GitHub API认证头
	req.Header.Set("Authorization", fmt.Sprintf("token %s", config.GithubToken))
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API错误 %d: %s", resp.StatusCode, string(body))
	}

	// 直接解析GitHub API返回的文件列表
	var files []GitHubDiff
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, err
	}

	return files, nil
}

func reviewFileChange(change GitHubDiff) ([]Comment, error) {
	log.Printf("🔍 正在评审文件: %s", change.Filename)
	log.Printf("📄 代码片段预览:\n%s", getPreviewDiff(change.Patch))

	// 获取文件语言类型
	language := getLanguageFromPath(change.Filename)

	// 修改提示词，使用简单分隔符格式
	prompt := fmt.Sprintf(`# 代码审查专家

## 审查要求

- 请对以下代码文件进行详细且全面的审查，识别并分析潜在的编码问题。
- 审查过程中，您需要关注以下方面，并给予详细反馈：
  - **逻辑错误**：检查代码中可能存在的逻辑漏洞或错误的实现，确保代码行为符合预期。
  - **编程规范和最佳实践**：查找违反行业标准的部分，如不合适的命名、不清晰的函数设计或不合理的代码结构。关注代码是否符合目标编程语言的社区标准。
  - **可维护性**：分析代码是否易于后续的维护、扩展与修改，是否有冗余代码、重复逻辑等。
  - **可读性**：确保代码结构清晰、命名规范，易于理解和调试。注释是否足够清晰、完整。
  - **性能**：检查是否有明显的性能瓶颈或不必要的复杂度，例如低效的算法、重复的计算等。
  - **安全性**：检查是否存在可能的安全漏洞，例如 SQL 注入、跨站脚本攻击（XSS）、敏感数据泄露等问题。
- 请避免关注代码的格式、空格以及代码风格（如缩进、空行等）。
- 对数据库相关问题（如迁移、表结构设计、索引、唯一性约束等）不做评审，这些内容不需要识别。
- 对于每个问题，请提供具体的行号、问题描述以及详细的解决方案。建议提供修改后的代码示例或进一步的优化建议。
- 请在审查时考虑到代码的可扩展性与团队协作，确保审查结果对团队长期开发有帮助。
- 请确保评审语言为中文，且清晰表达审查结果。
- 输出时请严格按照以下格式，每行一个问题：
  ISSUE|行号|问题描述|解决方案
  例如：
  ISSUE|42|这里变量命名不清晰|建议将变量改为更具描述性的名称
  
  如果没有发现问题，请输出：NOISSUES

## 语言: %s
## 文件: %s
## 代码:
%s`, language, change.Filename, change.Patch)

	aiResp, err := callAI(prompt)
	if err != nil {
		return nil, err
	}

	log.Printf("🤖 AI响应: %s", aiResp)

	// 解析AI响应获取评论
	comments := parseAIResponseJSON(aiResp, change.Patch)
	log.Printf("📋 解析得到评论: %+v", comments)

	// 仅校验行号的有效性
	var validComments []Comment
	diffLines := strings.Split(change.Patch, "\n")
	lineRanges := parseLineRanges(diffLines)

	for _, comment := range comments {
		if isLineInRanges(comment.Line, lineRanges) {
			validComments = append(validComments, comment)
			log.Printf("✅ 有效评论 - 行 %d: %s", comment.Line, comment.Content)
		} else {
			log.Printf("❌ 行号无效 - 行 %d: %s", comment.Line, comment.Content)
		}
	}

	log.Printf("🎯 最终有效评论数: %d", len(validComments))
	return validComments, nil
}

// parseLineRanges 解析diff文本获取新文件中的行范围
func parseLineRanges(diffLines []string) []struct{ Start, End int } {
	var ranges []struct{ Start, End int }
	currentLine := 0
	inHeader := true

	for _, line := range diffLines {
		if inHeader && strings.HasPrefix(line, "@@") {
			// 解析diff头部获取起始行号
			re := regexp.MustCompile(`@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
			matches := re.FindStringSubmatch(line)
			if len(matches) >= 2 {
				start, _ := strconv.Atoi(matches[1])
				currentLine = start - 1 // 减1是因为下面会先增加
				inHeader = false
			}
			continue
		}

		if !inHeader {
			if strings.HasPrefix(line, "+") {
				// 新增行
				currentLine++
				// 如果当前没有正在处理的范围，或者最后一个范围已经结束，添加新范围
				if len(ranges) == 0 || ranges[len(ranges)-1].End < currentLine-1 {
					ranges = append(ranges, struct{ Start, End int }{Start: currentLine, End: currentLine})
				} else {
					// 否则，扩展最后一个范围
					ranges[len(ranges)-1].End = currentLine
				}
			} else if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "\\") {
				// 上下文行
				if strings.HasPrefix(line, " ") {
					currentLine++
				}
			}
		}
	}

	return ranges
}

// isLineInRanges 检查行号是否在任何一个有效范围内
func isLineInRanges(lineNum int, ranges []struct{ Start, End int }) bool {
	for _, r := range ranges {
		if lineNum >= r.Start && lineNum <= r.End {
			return true
		}
	}
	return false
}

// extractLineContent 从diff中提取特定行的内容
func extractLineContent(diff string, lineNum int) string {
	lines := strings.Split(diff, "\n")
	currentLine := 0
	inHeader := true

	for _, line := range lines {
		if inHeader && strings.HasPrefix(line, "@@") {
			// 解析diff头部获取起始行号
			re := regexp.MustCompile(`@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
			matches := re.FindStringSubmatch(line)
			if len(matches) >= 2 {
				start, _ := strconv.Atoi(matches[1])
				currentLine = start - 1
				inHeader = false
			}
			continue
		}

		if !inHeader {
			if strings.HasPrefix(line, "+") {
				currentLine++
				if currentLine == lineNum {
					return strings.TrimPrefix(line, "+")
				}
			} else if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "\\") {
				// 上下文行
				if strings.HasPrefix(line, " ") {
					currentLine++
					if currentLine == lineNum {
						return strings.TrimPrefix(line, " ")
					}
				}
			}
		}
	}

	return "[找不到该行代码]"
}

// 修改解析函数以处理新的简单分隔符格式
func parseAIResponseJSON(aiResp string, diff string) []Comment {
	var comments []Comment

	lines := strings.Split(aiResp, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ISSUE|") {
			parts := strings.Split(line, "|")
			if len(parts) >= 4 {
				lineNum, err := strconv.Atoi(parts[1])
				if err != nil {
					log.Printf("❌ 无效的行号: %s", parts[1])
					continue
				}

				problem := strings.TrimSpace(parts[2])
				solution := strings.TrimSpace(parts[3])

				comment := Comment{
					Line:    lineNum,
					Content: fmt.Sprintf("%s|%s", problem, solution),
				}
				comments = append(comments, comment)
				log.Printf("  🔹 解析评论 - 行 %d: %s|%s", lineNum, problem, solution)
			}
		} else if line == "NOISSUES" {
			log.Printf("✅ 未发现问题")
			return comments
		}
	}

	return comments
}

// 备用解析函数也使用相同逻辑，以兼容旧格式的响应
func parseAIResponseFallback(aiResp string, diff string) []Comment {
	var comments []Comment

	// 尝试常见的行号模式
	linePattern := regexp.MustCompile(`(\d+)\s*[:：]\s*([^|]+)(?:\|(.+))?`)
	lines := strings.Split(aiResp, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == "NOISSUES" || strings.HasPrefix(line, "```") {
			continue
		}

		// 首先尝试解析 ISSUE| 格式
		if strings.HasPrefix(line, "ISSUE|") {
			parts := strings.Split(line, "|")
			if len(parts) >= 4 {
				lineNum, err := strconv.Atoi(parts[1])
				if err != nil {
					continue
				}

				problem := strings.TrimSpace(parts[2])
				solution := strings.TrimSpace(parts[3])

				comment := Comment{
					Line:    lineNum,
					Content: fmt.Sprintf("%s|%s", problem, solution),
				}

				comments = append(comments, comment)
				log.Printf("  🔹 从备用解析评论 - 行 %d: %s", lineNum, problem)
				continue
			}
		}

		// 然后尝试其他常见格式
		matches := linePattern.FindStringSubmatch(line)
		if len(matches) >= 3 {
			lineNum, err := strconv.Atoi(matches[1])
			if err != nil {
				continue
			}

			problem := strings.TrimSpace(matches[2])
			solution := ""
			if len(matches) >= 4 && matches[3] != "" {
				solution = strings.TrimSpace(matches[3])
			}

			comment := Comment{
				Line:    lineNum,
				Content: fmt.Sprintf("%s|%s", problem, solution),
			}

			comments = append(comments, comment)
			log.Printf("  🔹 从备用解析评论 - 行 %d: %s", lineNum, problem)
		}
	}

	return comments
}

// 更新 callAI 函数，加入完整实现
func callAI(prompt string) (string, error) {
	// 准备请求体，不再添加 JSON 格式指导
	requestBody := map[string]interface{}{
		"model":    config.Model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}

	// 创建HTTP请求
	req, err := http.NewRequest("POST", config.BaseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.APIKey))

	// 发送请求
	client := &http.Client{
		Timeout: 60 * time.Second, // 需要重新导入time包
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// 解析响应
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	responseText := string(body)
	previewLen := 200
	if len(responseText) > previewLen {
		log.Printf("API响应前%d字符: %s...", previewLen, responseText[:previewLen])
	} else {
		log.Printf("API响应: %s", responseText)
	}

	// 使用更灵活的解析方法
	var rawJSON map[string]interface{}
	if err := json.Unmarshal(body, &rawJSON); err != nil {
		// 如果是直接返回的文本（不是JSON），直接返回
		return responseText, nil
	}

	// 尝试标准的OpenAI格式
	if choices, ok := rawJSON["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := choice["message"].(map[string]interface{}); ok {
				if content, ok := message["content"].(string); ok {
					return content, nil
				}
			}
		}
	}

	// 如果找不到标准路径，检查是否直接有content字段
	if content, ok := rawJSON["content"].(string); ok {
		return content, nil
	}

	// 返回原始响应
	return responseText, nil
}

func createNote(event PullRequestEvent, change GitHubDiff, comment Comment) error {
	// 格式化问题和解决方案
	parts := strings.Split(comment.Content, "|")
	var formattedNote string
	if len(parts) >= 2 {
		problem := strings.TrimSpace(parts[0])
		solution := strings.TrimSpace(parts[1])
		formattedNote = fmt.Sprintf("**问题**: %s\n\n**建议**: %s", problem, solution)
	} else {
		formattedNote = comment.Content
	}

	// 获取PR的提交SHA
	_, headSHA, err := getMRCommitInfo(event)
	if err != nil {
		return fmt.Errorf("无法获取PR提交信息: %v", err)
	}

	// 准备请求体 - 创建单个评论
	url := fmt.Sprintf("%s/repos/%s/pulls/%d/comments",
		config.GithubHost,
		event.Repository.FullName,
		event.PullRequest.Number)

	// 构建评论请求
	reviewPayload := map[string]interface{}{
		"commit_id": headSHA,
		"path":      change.Filename,
		"body":      formattedNote,
		"position":  getDiffPosition(change.Patch, comment.Line),
	}

	payloadBytes, err := json.Marshal(reviewPayload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("token %s", config.GithubToken))
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API错误 %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// getDiffPosition 计算GitHub差异中的位置
func getDiffPosition(patch string, newLine int) int {
	// GitHub的position是在差异中的行号，而不是文件中的行号
	// 需要计算从patch开始的第几行
	lines := strings.Split(patch, "\n")
	position := 0
	currentLine := 0

	for i, line := range lines {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			currentLine++
			if currentLine == newLine {
				position = i + 1 // GitHub position是1-indexed
				break
			}
		} else if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			currentLine++
		}
	}

	return position
}

// getPRCommitInfo 获取PR的提交信息
func getMRCommitInfo(event PullRequestEvent) (string, string, error) {
	// 使用Pull Request的head.sha作为head提交SHA
	headSHA := event.PullRequest.Head.SHA
	baseSHA := event.PullRequest.Base.SHA

	// 如果SHA为空，则通过API获取
	if headSHA == "" || baseSHA == "" {
		url := fmt.Sprintf("%s/repos/%s/pulls/%d",
			config.GithubHost,
			event.Repository.FullName,
			event.PullRequest.Number)

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return "", "", err
		}

		req.Header.Set("Authorization", fmt.Sprintf("token %s", config.GithubToken))
		req.Header.Set("Accept", "application/vnd.github.v3+json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := ioutil.ReadAll(resp.Body)
			return "", "", fmt.Errorf("GitHub API错误 %d: %s", resp.StatusCode, string(body))
		}

		var pr struct {
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				SHA string `json:"sha"`
			} `json:"base"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
			return "", "", err
		}

		headSHA = pr.Head.SHA
		baseSHA = pr.Base.SHA
	}

	return baseSHA, headSHA, nil
}

// 检查指定行是否是新增行
func isAddedLine(diff string, lineNum int) bool {
	lines := strings.Split(diff, "\n")
	currentLine := 0
	inHeader := true

	for _, line := range lines {
		if inHeader && strings.HasPrefix(line, "@@") {
			re := regexp.MustCompile(`@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
			matches := re.FindStringSubmatch(line)
			if len(matches) >= 2 {
				start, _ := strconv.Atoi(matches[1])
				currentLine = start - 1
				inHeader = false
			}
			continue
		}

		if !inHeader {
			if strings.HasPrefix(line, "+") {
				currentLine++
				if currentLine == lineNum {
					return true // 这是一个新增行
				}
			} else if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "\\") {
				if strings.HasPrefix(line, " ") {
					currentLine++
				}
			}
		}
	}

	return false
}

// 添加辅助函数：根据文件路径判断语言类型
func getLanguageFromPath(path string) string {
	ext := filepath.Ext(path)
	switch strings.ToLower(ext) {
	case ".go":
		return "Go"
	case ".py":
		return "Python"
	case ".js":
		return "JavaScript"
	case ".ts":
		return "TypeScript"
	case ".java":
		return "Java"
	case ".php":
		return "PHP"
	case ".c", ".cpp", ".h", ".hpp":
		return "C/C++"
	case ".cs":
		return "C#"
	case ".rb":
		return "Ruby"
	case ".swift":
		return "Swift"
	case ".kt":
		return "Kotlin"
	case ".rs":
		return "Rust"
	case ".html", ".htm":
		return "HTML"
	case ".css":
		return "CSS"
	case ".sql":
		return "SQL"
	default:
		return "Unknown"
	}
}

// 添加函数，生成代码差异的简短预览
func getPreviewDiff(diff string) string {
	lines := strings.Split(diff, "\n")

	// 限制预览长度
	maxLines := 10
	if len(lines) > maxLines {
		return strings.Join(lines[:maxLines], "\n") + "\n... (更多行省略)"
	}

	return diff
}

// 改进isLikelyCode函数，更准确地识别代码片段
func isLikelyCode(content string) bool {
	// 常见代码模式的特征
	codePatterns := []string{
		`^\s*(\$[\w\d_]+\s*=|\w+\s*\(|\s*if\s*\(|function\s+\w+|class\s+\w+)`,
		`^[a-z0-9_$]+\s*\([^)]*\)\s*[{;]?\s*$`,
		`^(var|let|const|echo|print|return|import|package)\s+`,
		`^[a-z0-9_$]+\s*:\s*function`,
		`^\s*\{\s*$`,
		`^\s*\}\s*$`,
	}

	for _, pattern := range codePatterns {
		if regexp.MustCompile(pattern).MatchString(content) {
			return true
		}
	}

	// 太短且只有代码结构的可能是代码
	if len(content) < 15 && regexp.MustCompile(`^[;{}()\[\]]*$`).MatchString(content) {
		return true
	}

	return false
}
