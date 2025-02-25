package main

import (
	"bytes"
	"crypto/md5"
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

	log.Printf("📡 正在请求MR变更: %s", url)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %v", err)
	}

	req.Header.Set("PRIVATE-TOKEN", config.GitlabToken)

	// 添加部分遮掩的令牌到日志
	token := config.GitlabToken
	if len(token) > 8 {
		log.Printf("🔑 使用令牌: %s...%s", token[:4], token[len(token)-4:])
	} else {
		log.Printf("🔑 使用令牌: [已遮掩]")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 记录响应状态码
	log.Printf("🔄 API响应状态码: %d", resp.StatusCode)

	// 如果状态码不是200，记录详细错误
	if resp.StatusCode != 200 {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		log.Printf("❌ GitLab API错误: %s", string(bodyBytes))
		return nil, fmt.Errorf("GitLab API 返回非200状态码: %d", resp.StatusCode)
	}

	var result struct {
		Changes []GitLabDiff `json:"changes"`
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// 打印响应预览以便调试
	if len(bodyBytes) > 300 {
		log.Printf("📄 响应预览: %s...", string(bodyBytes[:300]))
	} else {
		log.Printf("📄 响应内容: %s", string(bodyBytes))
	}

	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, err
	}

	log.Printf("🔎 解析到 %d 个变更文件", len(result.Changes))

	return result.Changes, nil
}

func reviewFileChange(change GitLabDiff) ([]Comment, error) {
	log.Printf("🔍 正在评审文件: %s", change.NewPath)
	log.Printf("📄 代码片段预览:\n%s", getPreviewDiff(change.Diff))

	// 获取文件语言类型
	language := getLanguageFromPath(change.NewPath)

	// 更新提示词，强调数组越界问题
	prompt := fmt.Sprintf(`# 专业代码评审

## 语言: %s

## 优先检查问题:
1. 数组越界访问 - 必须特别关注任何数组/切片/列表访问操作
2. 未处理的异常情况
3. SQL注入风险
4. XSS安全隐患
5. 资源泄漏(未关闭文件/连接)
6. 逻辑错误
7. 变量命名不当(误导性命名)

## 数组越界检查规则:
- 检查所有数组索引操作，如 array[index]
- 如果索引未经检查即使用，标记为"数组越界访问"问题
- 安全代码应先检查索引是否越界，如 if(isset(array[index]))
- PHP中特别注意 $array[0], $array[1] 等直接访问
- JavaScript中注意 arr[i] 是否确保了 i < arr.length

## 格式要求:
- 使用 "行号:问题类型|解决方案" 格式
- 数组问题必须使用"数组越界访问"作为问题类型
- 对没有问题的文件，返回 "NOISSUES"

## 代码文件: %s
## 代码:
%s`, language, change.NewPath, change.Diff)

	aiResp, err := callAI(prompt)
	if err != nil {
		return nil, err
	}

	log.Printf("🤖 AI响应: %s", aiResp)

	// 检查"无问题"的特殊标记
	if strings.Contains(aiResp, "NOISSUES") {
		log.Printf("✅ 未发现重要问题")
		return []Comment{}, nil
	}

	// 解析AI响应获取评论
	comments := parseAIResponse(aiResp, change.Diff)
	log.Printf("📋 原始解析评论: %d 条", len(comments))

	// 过滤掉无意义的评论
	comments = filterMeaninglessComments(comments)
	log.Printf("🧹 过滤后评论: %d 条", len(comments))

	// 检查解析的评论是否合理(行号检查等)
	validComments := filterValidComments(comments, change.Diff)
	log.Printf("🎯 最终有效评论: %d 条", len(validComments))

	// 格式化每个评论
	for i := range validComments {
		parts := strings.Split(validComments[i].Content, "|")

		if len(parts) >= 2 {
			problem := strings.TrimSpace(parts[0])
			optimization := strings.TrimSpace(parts[1])
			validComments[i].Content = fmt.Sprintf("**问题**: %s\n\n**优化**: %s", problem, optimization)
		} else {
			validComments[i].Content = fmt.Sprintf("**问题**: %s", validComments[i].Content)
		}
	}

	return validComments, nil
}

func createNote(event MergeRequestEvent, change GitLabDiff, comment Comment) error {
	// 获取MR的版本信息
	versionsURL := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/versions",
		config.GitlabHost,
		event.Project.ID,
		event.ObjectAttributes.IID)

	req, err := http.NewRequest("GET", versionsURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", config.GitlabToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var versions []struct {
		HeadCommitSHA  string `json:"head_commit_sha"`
		BaseCommitSHA  string `json:"base_commit_sha"`
		StartCommitSHA string `json:"start_commit_sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&versions); err != nil {
		return err
	}

	if len(versions) == 0 {
		return fmt.Errorf("no versions found for merge request")
	}

	latestVersion := versions[0]

	// 需要一个更可靠的line_code生成方式
	// 根据GitLab源码，line_code通常是文件哈希和行号的组合
	fileHash := fmt.Sprintf("%x", md5.Sum([]byte(change.NewPath)))
	lineCode := fmt.Sprintf("%s_0_%d", fileHash, comment.Line)

	// 输出调试信息
	log.Printf("File: %s, Line: %d, Generated line_code: %s",
		change.NewPath, comment.Line, lineCode)

	position := map[string]interface{}{
		"base_sha":      latestVersion.BaseCommitSHA,
		"start_sha":     latestVersion.StartCommitSHA,
		"head_sha":      latestVersion.HeadCommitSHA,
		"position_type": "text",
		"old_path":      change.OldPath,
		"new_path":      change.NewPath,
		"old_line":      nil,
		"new_line":      comment.Line,
	}

	// 使用discussions API端点，而不是notes API
	url := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/discussions",
		config.GitlabHost,
		event.Project.ID,
		event.ObjectAttributes.IID)

	payload := map[string]interface{}{
		"body":     comment.Content,
		"position": position,
	}

	log.Printf("Creating discussion with payload: %+v", payload)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err = http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}

	req.Header.Set("PRIVATE-TOKEN", config.GitlabToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	responseBody, _ := ioutil.ReadAll(resp.Body)
	log.Printf("Response status: %d, body: %s", resp.StatusCode, string(responseBody))

	if resp.StatusCode >= 400 {
		return fmt.Errorf("failed to create discussion: status=%d, response=%s",
			resp.StatusCode, string(responseBody))
	}

	return nil
}

// 调用AI服务获取代码评审结果
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
	req, err := http.NewRequest("POST", config.BaseURL+"/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.APIKey))

	// 发送请求
	client := &http.Client{
		Timeout: 60 * time.Second, // 增加超时时间到60秒
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

	// 添加响应内容日志，便于调试
	respContent := string(body)
	if len(respContent) > 200 {
		log.Printf("API响应前200字符: %s...", respContent[:200])
	} else {
		log.Printf("API响应: %s", respContent)
	}

	// 尝试处理可能存在的前导或尾随内容
	cleanedJSON := regexp.MustCompile(`[\s\n]*(.+?)[\s\n]*$`).FindStringSubmatch(respContent)
	if len(cleanedJSON) > 1 {
		respContent = cleanedJSON[1]
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(respContent), &result); err != nil {
		// 如果标准解析失败，尝试使用兼容模式
		log.Printf("标准JSON解析失败: %v，尝试兼容模式", err)
		return extractContentFromResponse(respContent)
	}

	// 检查错误
	if errObj, exists := result["error"]; exists {
		errMap, ok := errObj.(map[string]interface{})
		if ok {
			if message, has := errMap["message"]; has {
				return "", fmt.Errorf("API error: %v", message)
			}
		}
		return "", fmt.Errorf("API error: %v", errObj)
	}

	// 提取AI响应
	choices, exists := result["choices"].([]interface{})
	if !exists || len(choices) == 0 {
		return "", fmt.Errorf("unexpected API response format")
	}

	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("unexpected choice format")
	}

	message, ok := choice["message"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("unexpected message format")
	}

	content, ok := message["content"].(string)
	if !ok {
		return "", fmt.Errorf("unexpected content format")
	}

	return content, nil
}

// 备用解析函数，处理非标准JSON响应
func extractContentFromResponse(responseText string) (string, error) {
	// 尝试直接提取内容部分
	contentPattern := regexp.MustCompile(`"content"\s*:\s*"([^"]+)"`)
	matches := contentPattern.FindStringSubmatch(responseText)
	if len(matches) > 1 {
		// 反转义JSON字符串
		content := strings.ReplaceAll(matches[1], "\\\"", "\"")
		content = strings.ReplaceAll(content, "\\n", "\n")
		content = strings.ReplaceAll(content, "\\t", "\t")
		content = strings.ReplaceAll(content, "\\\\", "\\")
		return content, nil
	}

	// 尝试从非标准API返回结构中提取内容
	directResponsePattern := regexp.MustCompile(`(?:NOISSUES|\d+\s*:.*\|.*)`)
	if directResponsePattern.MatchString(responseText) {
		// 如果响应文本直接包含评论格式，返回整个文本
		return responseText, nil
	}

	// 如果无法识别格式，返回整个响应文本但报告警告
	log.Printf("⚠️ 无法解析API响应，返回原始文本")
	return responseText, nil
}

func parseAIResponse(aiResponse, diff string) []Comment {
	var comments []Comment

	// 先处理常见格式的行号:问题描述
	patterns := []string{
		`(?m)^(\d+):(.+)$`, // 基本格式：行号:问题描述
		`(?m)行号[：:]\s*(\d+)\n潜在问题[：:]\s*(.+)`, // 格式：行号: 数字 换行 潜在问题: 描述
		`(?m)行[：:]?\s*(\d+)[：:]?\s*(.+)`,      // 格式：行 数字: 描述
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindAllStringSubmatch(aiResponse, -1)

		for _, match := range matches {
			if len(match) >= 3 {
				lineNum, err := strconv.Atoi(match[1])
				if err != nil {
					log.Printf("Error parsing line number: %v", err)
					continue
				}

				content := strings.TrimSpace(match[2])

				// 过滤掉可能是示例代码的内容
				if isLikelyCode(content) {
					log.Printf("忽略可能是示例代码的评论: 行%d: %s", lineNum, content)
					continue
				}

				if len(content) > 500 {
					content = content[:497] + "..."
				}

				comments = append(comments, Comment{
					Line:    lineNum,
					Content: content,
				})
			}
		}

		// 如果已经找到了评论，就不用尝试其他正则表达式了
		if len(comments) > 0 {
			break
		}
	}

	log.Printf("📝 从AI响应解析出 %d 条评论", len(comments))
	for i, comment := range comments {
		log.Printf("  🔹 评论 #%d - 行 %d: %s", i+1, comment.Line, comment.Content)
	}

	return comments
}

// 过滤有效的评论
func filterValidComments(comments []Comment, diff string) []Comment {
	var validComments []Comment

	// 跟踪已经评论过的行号，避免重复
	commentedLines := make(map[int]bool)

	for _, comment := range comments {
		// 先检查行号是否有效
		if comment.Line <= 0 {
			log.Printf("❌ 忽略无效行号 %d: %s", comment.Line, truncateString(comment.Content, 50))
			continue
		}

		// 检查行号是否在diff范围内
		if !isLineInDiff(diff, comment.Line) {
			log.Printf("❌ 忽略超出范围的评论 - 行 %d: %s", comment.Line, truncateString(comment.Content, 50))
			continue
		}

		// 检查是否已经评论过这一行
		if commentedLines[comment.Line] {
			log.Printf("⚠️ 忽略重复行的评论 - 行 %d: %s", comment.Line, truncateString(comment.Content, 50))
			continue
		}

		// 标记该行已评论
		commentedLines[comment.Line] = true

		log.Printf("✅ 有效评论 - 行 %d: %s", comment.Line, truncateString(comment.Content, 50))
		validComments = append(validComments, comment)
	}

	return validComments
}

// 新增函数：根据评论内容找到相关的代码行
func findRelevantLineNumber(diff string, content string) int {
	// 检查是否是数组相关问题
	if strings.Contains(strings.ToLower(content), "数组") ||
		strings.Contains(strings.ToLower(content), "array") {
		return findArrayRelatedLine(diff)
	}

	// 检查是否是变量命名问题
	if strings.Contains(strings.ToLower(content), "命名") ||
		strings.Contains(strings.ToLower(content), "name") {
		return findVariableDeclarationLine(diff)
	}

	// 检查是否是安全相关问题
	if strings.Contains(strings.ToLower(content), "安全") ||
		strings.Contains(strings.ToLower(content), "注入") ||
		strings.Contains(strings.ToLower(content), "xss") ||
		strings.Contains(strings.ToLower(content), "security") {
		return findSecurityRelatedLine(diff)
	}

	return -1
}

// 新增函数：查找数组相关代码行
func findArrayRelatedLine(diff string) int {
	lines := strings.Split(diff, "\n")

	// 先查找数组定义行
	for i, line := range lines {
		if strings.HasPrefix(line, "+") {
			cleanLine := strings.TrimPrefix(line, "+")
			// 匹配数组定义或使用模式
			if regexp.MustCompile(`\[\s*\d+\s*\]|array\s*\(|=\s*\[`).MatchString(cleanLine) {
				lineNum := findRealLineNumber(diff, i)
				if lineNum > 0 {
					return lineNum
				}
			}
		}
	}

	return -1
}

// 新增函数：查找变量声明行
func findVariableDeclarationLine(diff string) int {
	lines := strings.Split(diff, "\n")

	for i, line := range lines {
		if strings.HasPrefix(line, "+") {
			cleanLine := strings.TrimPrefix(line, "+")
			// 匹配变量声明模式
			if regexp.MustCompile(`\$\w+\s*=|\bvar\b|\blet\b|\bconst\b|function\s+\w+|class\s+\w+`).MatchString(cleanLine) {
				lineNum := findRealLineNumber(diff, i)
				if lineNum > 0 {
					return lineNum
				}
			}
		}
	}

	return -1
}

// 新增函数：查找安全相关代码行
func findSecurityRelatedLine(diff string) int {
	lines := strings.Split(diff, "\n")

	for i, line := range lines {
		if strings.HasPrefix(line, "+") {
			cleanLine := strings.TrimPrefix(line, "+")
			// 匹配可能有安全问题的代码模式
			if regexp.MustCompile(`\$_GET|\$_POST|\$_REQUEST|exec\(|eval\(|shell_exec|passthru`).MatchString(cleanLine) {
				lineNum := findRealLineNumber(diff, i)
				if lineNum > 0 {
					return lineNum
				}
			}
		}
	}

	return -1
}

// 新增辅助函数：找到diff中索引对应的真实行号
func findRealLineNumber(diff string, index int) int {
	lines := strings.Split(diff, "\n")
	if index < 0 || index >= len(lines) {
		return -1
	}

	currentLine := 0
	inHeader := true

	for i, line := range lines {
		if i > index {
			break
		}

		if inHeader && strings.HasPrefix(line, "@@") {
			// 解析diff头部
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
				if i == index {
					return currentLine
				}
			} else if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "\\") {
				if strings.HasPrefix(line, " ") {
					currentLine++
				}
			}
		}
	}

	return -1
}

// 新增函数：在找不到具体行号时，返回第一个有意义的代码行
func findFirstRelevantLine(diff string, content string) int {
	lines := strings.Split(diff, "\n")
	currentLine := 0
	inHeader := true

	for _, line := range lines {
		if inHeader && strings.HasPrefix(line, "@@") {
			// 解析diff头部
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
				cleanLine := strings.TrimPrefix(line, "+")
				if len(strings.TrimSpace(cleanLine)) > 0 && !strings.HasPrefix(cleanLine, "//") {
					// 找到第一个非空非注释行
					return currentLine
				}
			} else if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "\\") {
				if strings.HasPrefix(line, " ") {
					currentLine++
				}
			}
		}
	}

	return -1
}

type LineRange struct {
	Start int
	End   int
}

func parseLineRanges(diffLines []string) []LineRange {
	var ranges []LineRange
	var currentRange *LineRange
	currentLine := 0

	for _, line := range diffLines {
		if strings.HasPrefix(line, "@@") {
			// 解析diff头，格式如：@@ -1,7 +1,9 @@
			re := regexp.MustCompile(`@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)
			matches := re.FindStringSubmatch(line)
			if len(matches) >= 2 {
				start, _ := strconv.Atoi(matches[1])
				length := 1
				if len(matches) >= 3 && matches[2] != "" {
					length, _ = strconv.Atoi(matches[2])
				}
				currentRange = &LineRange{
					Start: start,
					End:   start + length - 1,
				}
				ranges = append(ranges, *currentRange)
			}
			currentLine = currentRange.Start
			continue
		}

		if strings.HasPrefix(line, "+") {
			currentLine++
		}
	}

	return ranges
}

func isLineInRanges(line int, ranges []LineRange) bool {
	for _, r := range ranges {
		if line >= r.Start && line <= r.End {
			return true
		}
	}
	return false
}

type GitLabDiff struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Diff    string `json:"diff"`
}

func extractLineContent(diff string, targetLine int) string {
	lines := strings.Split(diff, "\n")
	currentLine := 0

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			// 解析diff头部获取起始行号
			re := regexp.MustCompile(`@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
			matches := re.FindStringSubmatch(line)
			if len(matches) >= 2 {
				start, _ := strconv.Atoi(matches[1])
				currentLine = start - 1 // 减1是因为下面会先增加
			}
			continue
		}

		if strings.HasPrefix(line, "+") {
			currentLine++
			if currentLine == targetLine {
				// 返回去掉前缀的代码行内容
				return strings.TrimPrefix(line, "+")
			}
		} else if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "\\") {
			// 上下文行也算作新文件的行
			if !strings.HasPrefix(line, " ") {
				// 不是标准的diff格式，跳过
				continue
			}
			currentLine++
			if currentLine == targetLine {
				return strings.TrimPrefix(line, " ")
			}
		}
	}

	return "[找不到该行代码]"
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

// 辅助函数：查找特定行在diff中的行号
func findLineNumber(diff string, targetLine string) int {
	lines := strings.Split(diff, "\n")
	currentLine := 0

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			// 解析diff头部获取起始行号
			re := regexp.MustCompile(`@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
			matches := re.FindStringSubmatch(line)
			if len(matches) >= 2 {
				start, _ := strconv.Atoi(matches[1])
				currentLine = start - 1 // 减1是因为下面会先增加
			}
			continue
		}

		if strings.HasPrefix(line, "+") {
			currentLine++
			if line == targetLine {
				return currentLine
			}
		} else if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "\\") {
			// 上下文行也算作新文件的行
			if !strings.HasPrefix(line, " ") {
				// 不是标准的diff格式，跳过
				continue
			}
			currentLine++
		}
	}

	return -1
}

// 过滤无意义的评论
func filterMeaninglessComments(comments []Comment) []Comment {
	var filteredComments []Comment

	// 无意义评论的关键词
	meaninglessPatterns := []string{
		"(?i)缩进",
		"(?i)空[格行]",
		"(?i)格式",
		"(?i)注释",
		"(?i)文档",
		"(?i)indent",
		"(?i)whitespace",
		"(?i)spacing",
		"(?i)format",
		"(?i)comment",
		"(?i)document",
		"(?i)PSR",
		"(?i)coding standard",
	}

	for _, comment := range comments {
		// 默认认为评论有意义
		isMeaningless := false

		// 检查是否包含无意义关键词
		for _, pattern := range meaninglessPatterns {
			if regexp.MustCompile(pattern).MatchString(comment.Content) {
				isMeaningless = true
				log.Printf("🚫 过滤无意义评论: 行 %d - %s", comment.Line, truncateString(comment.Content, 50))
				break
			}
		}

		// 如果不是无意义的，加入过滤后的结果
		if !isMeaningless {
			filteredComments = append(filteredComments, comment)
		}
	}

	return filteredComments
}

// 辅助函数：截断字符串到指定长度
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// 检查给定行号是否在diff范围内
func isLineInDiff(diff string, lineNum int) bool {
	lines := strings.Split(diff, "\n")
	currentLine := 0
	inHeader := true

	for _, line := range lines {
		if inHeader && strings.HasPrefix(line, "@@") {
			// 解析diff头部获取起始行号和范围
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
					return true
				}
			} else if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "\\") {
				// 上下文行也算作新文件的行
				if strings.HasPrefix(line, " ") {
					currentLine++
					if currentLine == lineNum {
						return true
					}
				}
			}
		}
	}

	return false
}
