package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config 结构体用于存储配置信息
type Config struct {
	GitlabToken string `json:"gitlab_token"`
	WebhookPort string `json:"webhook_port"`
	GitlabHost  string `json:"gitlab_host"`
	APIKey      string `json:"api_key"`
	Model       string `json:"model"`
	BaseURL     string `json:"base_url"`
}

// MergeRequestEvent GitLab webhook事件结构
type MergeRequestEvent struct {
	ObjectKind string `json:"object_kind"`
	Project    struct {
		ID                int    `json:"id"`
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	ObjectAttributes struct {
		ID           int    `json:"id"`
		IID          int    `json:"iid"`
		TargetBranch string `json:"target_branch"`
		SourceBranch string `json:"source_branch"`
		State        string `json:"state"`
	} `json:"object_attributes"`
}

// GitLabDiff 表示GitLab API返回的差异信息
type GitLabDiff struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Diff    string `json:"diff"`
}

var config Config

func main() {
	// 读取配置文件
	configData, err := ioutil.ReadFile("config.json")
	if err != nil {
		log.Fatal("Error reading config file:", err)
	}

	if err := json.Unmarshal(configData, &config); err != nil {
		log.Fatal("Error parsing config:", err)
	}

	// 设置webhook处理路由
	http.HandleFunc("/webhook", handleWebhook)

	fmt.Printf("Starting webhook server on port %s...\n", config.WebhookPort)
	log.Fatal(http.ListenAndServe(":"+config.WebhookPort, nil))
}

func handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 读取请求体
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Error reading request body", http.StatusInternalServerError)
		return
	}

	// 解析webhook事件
	var event MergeRequestEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "Error parsing webhook payload", http.StatusBadRequest)
		return
	}

	// 只处理merge request事件，且必须是开放状态的
	if event.ObjectKind != "merge_request" || event.ObjectAttributes.State != "opened" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 异步处理代码评审
	go handleCodeReview(event)

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

func handleCodeReview(event MergeRequestEvent) {
	log.Printf("🚀 开始评审 MR !%d 在项目 %s",
		event.ObjectAttributes.IID, event.Project.PathWithNamespace)

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
			log.Printf("❌ 评审文件 %s 失败: %v", change.NewPath, err)
			continue
		}

		if len(comments) > 0 {
			reviewedFiles++
			commentCount += len(comments)
			log.Printf("🔍 在 %s 中发现 %d 个问题", change.NewPath, len(comments))
		} else {
			log.Printf("✅ 文件 %s 未发现问题", change.NewPath)
		}

		// 对每个评论创建单独的note
		for _, comment := range comments {
			if err := createNote(event, change, comment); err != nil {
				log.Printf("❌ 创建评论失败 %s 行 %d: %v", change.NewPath, comment.Line, err)
			} else {
				log.Printf("💬 已在 %s 行 %d 创建评论", change.NewPath, comment.Line)
			}
		}
	}

	log.Printf("🏁 评审完成: %d 个文件有问题, 共发布 %d 条评论",
		reviewedFiles, commentCount)
}

func getMRChanges(event MergeRequestEvent) ([]GitLabDiff, error) {
	// 添加更多查询参数以获取完整的diff信息
	url := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/changes?access_raw_diffs=true",
		config.GitlabHost,
		event.Project.ID,
		event.ObjectAttributes.IID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("PRIVATE-TOKEN", config.GitlabToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Changes []GitLabDiff `json:"changes"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.Changes, nil
}

func reviewFileChange(change GitLabDiff) ([]Comment, error) {
	log.Printf("🔍 正在评审文件: %s", change.NewPath)
	log.Printf("📄 代码片段预览:\n%s", getPreviewDiff(change.Diff))

	// 获取文件语言类型
	language := getLanguageFromPath(change.NewPath)

	// 修改提示词，要求返回JSON格式
	prompt := fmt.Sprintf(`# 代码评审专家

## 要求
- 分析下面的代码文件并识别潜在问题
- 特别关注：数组越界、安全漏洞、未处理异常、SQL注入、资源泄漏等
- 返回格式必须为下面的JSON格式：
  {"comments": [{"line": 行号, "problem": "问题描述", "solution": "解决方案"}]}
- 如果没有发现问题，则返回：{"comments": []}
- 不要返回任何其他文本，只返回JSON

## 语言: %s
## 文件: %s
## 代码:
%s`, language, change.NewPath, change.Diff)

	aiResp, err := callAI(prompt)
	if err != nil {
		return nil, err
	}

	log.Printf("🤖 AI响应: %s", aiResp)

	// 解析AI响应获取评论
	comments := parseAIResponseJSON(aiResp, change.Diff)
	log.Printf("📋 解析得到评论: %+v", comments)

	// 仅校验行号的有效性
	var validComments []Comment
	diffLines := strings.Split(change.Diff, "\n")
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

// parseAIResponseJSON 尝试解析AI响应为JSON格式
func parseAIResponseJSON(aiResp string, diff string) []Comment {
	// 清理响应，提取JSON部分
	jsonStart := strings.Index(aiResp, "{")
	jsonEnd := strings.LastIndex(aiResp, "}")

	if jsonStart >= 0 && jsonEnd >= 0 && jsonEnd > jsonStart {
		aiResp = aiResp[jsonStart : jsonEnd+1]
	}

	// 尝试解析JSON
	var result struct {
		Comments []struct {
			Line     int    `json:"line"`
			Problem  string `json:"problem"`
			Solution string `json:"solution"`
		} `json:"comments"`
	}

	err := json.Unmarshal([]byte(aiResp), &result)
	if err != nil {
		log.Printf("❌ 解析JSON失败: %v，尝试备用解析", err)
		return parseAIResponseFallback(aiResp, diff)
	}

	var comments []Comment
	for _, c := range result.Comments {
		comment := Comment{
			Line:    c.Line,
			Content: fmt.Sprintf("%s|%s", c.Problem, c.Solution),
		}
		comments = append(comments, comment)
		log.Printf("  🔹 从JSON解析评论 - 行 %d: %s|%s", c.Line, c.Problem, c.Solution)
	}

	return comments
}

// parseAIResponseFallback 在JSON解析失败时的备用解析方法
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

// 4. 改进AI调用函数，更好地处理各种响应格式
func callAI(prompt string) (string, error) {
	// 准备请求体
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
		Timeout: 60 * time.Second,
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

func createNote(event MergeRequestEvent, change GitLabDiff, comment Comment) error {
	// 提取代码行内容
	codeContent := extractLineContent(change.Diff, comment.Line)
	codeBlock := ""
	if codeContent != "[找不到该行代码]" {
		codeBlock = fmt.Sprintf("```\n%s\n```", codeContent)
	}

	// 格式化问题和解决方案
	parts := strings.Split(comment.Content, "|")
	formattedComment := ""

	if len(parts) >= 2 {
		problem := strings.TrimSpace(parts[0])
		solution := strings.TrimSpace(parts[1])
		formattedComment = fmt.Sprintf("**问题**: %s\n\n**优化**: %s", problem, solution)
	} else {
		formattedComment = fmt.Sprintf("**问题**: %s", comment.Content)
	}

	// 完整评论内容
	fullComment := fmt.Sprintf("%s\n\n**代码行**: \n%s", formattedComment, codeBlock)

	// 获取MR的提交信息
	base_sha, head_sha, err := getMRCommitInfo(event)
	if err != nil {
		log.Printf("⚠️ 无法获取MR提交信息: %v, 使用默认值", err)
		base_sha = "HEAD~"
		head_sha = "HEAD"
	}

	log.Printf("MR提交信息: base_sha=%s, head_sha=%s", base_sha, head_sha)

	// 准备请求体
	url := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/discussions",
		config.GitlabHost,
		event.Project.ID,
		event.ObjectAttributes.IID)

	// 构建position参数 - 使用text类型而不是提供line_code
	position := map[string]interface{}{
		"base_sha":      base_sha,
		"start_sha":     base_sha, // 使用相同的base_sha
		"head_sha":      head_sha,
		"position_type": "text",
		"new_path":      change.NewPath,
		"new_line":      comment.Line,
	}

	// 如果不是新文件，设置old_path和old_line
	if change.OldPath != "/dev/null" {
		position["old_path"] = change.NewPath
		// 对于修改的文件，旧行号可以与新行号相同
		// 对于添加的行，不设置old_line
		if !isAddedLine(change.Diff, comment.Line) {
			position["old_line"] = comment.Line
		}
	}

	payload := map[string]interface{}{
		"body":     fullComment,
		"position": position,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	log.Printf("Creating discussion with payload: %s", string(payloadBytes))

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("PRIVATE-TOKEN", config.GitlabToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := ioutil.ReadAll(resp.Body)
	log.Printf("Response status: %d, body: %s", resp.StatusCode, string(respBody))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("API返回非成功状态码: %d", resp.StatusCode)
	}

	return nil
}

// 获取MR的提交信息
func getMRCommitInfo(event MergeRequestEvent) (string, string, error) {
	url := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/versions",
		config.GitlabHost,
		event.Project.ID,
		event.ObjectAttributes.IID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", "", err
	}

	req.Header.Set("PRIVATE-TOKEN", config.GitlabToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("获取MR版本信息失败，状态码: %d", resp.StatusCode)
	}

	var versions []struct {
		HeadCommitSHA string `json:"head_commit_sha"`
		BaseCommitSHA string `json:"base_commit_sha"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&versions); err != nil {
		return "", "", err
	}

	if len(versions) == 0 {
		return "", "", fmt.Errorf("未找到MR版本信息")
	}

	// 使用最新版本的提交SHA
	latestVersion := versions[0]
	return latestVersion.BaseCommitSHA, latestVersion.HeadCommitSHA, nil
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
