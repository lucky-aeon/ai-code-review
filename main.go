package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// 配置结构
type Config struct {
	GitLabToken    string `json:"gitlab_token"`
	WebhookPort    string `json:"webhook_port"`
	GitLabHost     string `json:"gitlab_host"`
	ApiKey         string `json:"api_key"`
	Model          string `json:"model"`
	BaseURL        string `json:"base_url"`
	ReviewerName   string `json:"reviewer_name"`
	ReviewerAvatar string `json:"reviewer_avatar"`
}

// MR Webhook 事件结构
type MergeRequestEvent struct {
	ObjectKind string `json:"object_kind"`
	Project    struct {
		ID                int    `json:"id"`
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	ObjectAttributes struct {
		ID          int    `json:"iid"`
		State       string `json:"state"`
		Description string `json:"description"`
	} `json:"object_attributes"`
}

// 修改 GitLab API 响应结构
type DiffChange struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Diff    string `json:"diff"`
}

// 添加 MR 详情结构
type MergeRequestDetail struct {
	Changes  []DiffChange `json:"changes"`
	DiffRefs struct {
		BaseSHA  string `json:"base_sha"`
		StartSHA string `json:"start_sha"`
		HeadSHA  string `json:"head_sha"`
	} `json:"diff_refs"`
}

// 添加新的结构用于代码行评论
type LinePosition struct {
	BaseSHA  string `json:"base_sha"`
	StartSHA string `json:"start_sha"`
	HeadSHA  string `json:"head_sha"`
	OldPath  string `json:"old_path"`
	NewPath  string `json:"new_path"`
	OldLine  *int   `json:"old_line"`
	NewLine  int    `json:"new_line"`
	LineCode string `json:"line_code"`
	Type     string `json:"type"`
}

// 修改评论请求结构
type CreateNoteRequest struct {
	Body     string           `json:"body"`
	Position *PositionRequest `json:"position,omitempty"`
}

// 修改位置请求结构
type PositionRequest struct {
	BaseSHA      string `json:"base_sha"`
	StartSHA     string `json:"start_sha"`
	HeadSHA      string `json:"head_sha"`
	OldPath      string `json:"old_path"`
	NewPath      string `json:"new_path"`
	PositionType string `json:"position_type"`
	OldLine      *int   `json:"old_line"`
	NewLine      int    `json:"new_line"`
}

type LineRange struct {
	Start LinePosition `json:"start"`
	End   LinePosition `json:"end"`
}

// 添加新的结构体来存储代码块信息
type CodeBlock struct {
	StartLine int
	EndLine   int
	Content   string
	Context   string // 包含上下文的完整代码
}

func loadConfig() Config {
	file, err := ioutil.ReadFile("config.json")
	if err != nil {
		log.Fatal("Error reading config file:", err)
	}

	var config Config
	err = json.Unmarshal(file, &config)
	if err != nil {
		log.Fatal("Error parsing config:", err)
	}
	return config
}

func reviewCode(diff string, config Config) (string, error) {

	if strings.TrimSpace(diff) == "" {
		return "", nil
	}
	// 构建请求体
	requestBody := map[string]interface{}{
		"model": config.Model,
		"messages": []map[string]string{
			{
				"role": "user",
				"content": fmt.Sprintf(`作为专业的代码审查者，请对以下代码变更进行详细的审查。请从以下几个方面进行分析：

1. 代码质量与最佳实践
   - 代码可读性和清晰度
   - 命名规范
   - 代码结构和组织
   - 潜在的性能问题

2. 功能性
   - 代码是否完成预期功能
   - 是否存在逻辑错误
   - 边界条件处理

3. 安全性
   - 潜在的安全漏洞
   - 数据验证和清理
   - 敏感信息处理

4. 可维护性
   - 代码复杂度
   - 注释完整性
   - 重复代码
   - 测试覆盖

请提供具体的改进建议，并说明原因。对于发现的问题，请给出改进的示例代码。

以下是需要审查的代码变更：

%s

请以清晰的条目形式列出审查意见，并标注优先级（高/中/低）。`, diff),
			},
		},
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("error marshaling request: %v", err)
	}

	// 构建 OpenAI API 请求
	url := config.BaseURL + "/chat/completions"
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+config.ApiKey)

	// 发送请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error making request: %v", err)
	}
	defer resp.Body.Close()

	// 解析响应
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("error decoding response: %v", err)
	}

	// 检查是否有错误
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error: %s", result.Error.Message)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no response from API")
	}

	return result.Choices[0].Message.Content, nil
}

// 修改 getMRChanges 函数
func getMRChanges(config Config, projectPath string, mrID int) (*MergeRequestDetail, error) {
	encodedPath := url.PathEscape(projectPath)
	url := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/changes",
		config.GitLabHost, encodedPath, mrID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("PRIVATE-TOKEN", config.GitLabToken)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var mrResp MergeRequestDetail
	if err := json.NewDecoder(resp.Body).Decode(&mrResp); err != nil {
		return nil, err
	}

	return &mrResp, nil
}

// 修改添加评论的函数
func addLineComment(config Config, projectPath string, mrID int, comment string, position LinePosition) error {
	encodedPath := url.PathEscape(projectPath)
	url := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/discussions",
		config.GitLabHost, encodedPath, mrID)

	// 构建请求体
	reqBody := CreateNoteRequest{
		Body: comment,
		Position: &PositionRequest{
			BaseSHA:      position.BaseSHA,
			StartSHA:     position.StartSHA,
			HeadSHA:      position.HeadSHA,
			OldPath:      position.OldPath,
			NewPath:      position.NewPath,
			PositionType: "text",
			OldLine:      nil,
			NewLine:      position.NewLine,
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("error marshaling request body: %v", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return err
	}

	req.Header.Set("PRIVATE-TOKEN", config.GitLabToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("failed to add comment: status %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

// 修改 parseDiff 函数，使用更智能的代码块划分逻辑
func parseDiff(diff string) []CodeBlock {
	lines := strings.Split(diff, "\n")
	var blocks []CodeBlock
	var currentBlock *CodeBlock
	var allContent strings.Builder

	currentLine := 0

	// 第一遍扫描，收集所有内容作为上下文
	for _, line := range lines {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			content := strings.TrimPrefix(line, "+")
			allContent.WriteString(content + "\n")
		}
	}

	fullContext := allContent.String()
	currentLine = 0

	// 第二遍扫描，根据语法结构划分代码块
	for _, line := range lines {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			currentLine++
			content := strings.TrimPrefix(line, "+")

			// 检测 PHP 代码块的开始
			if strings.Contains(content, "<?php") {
				if currentBlock != nil {
					currentBlock.EndLine = currentLine - 1
					blocks = append(blocks, *currentBlock)
				}
				currentBlock = &CodeBlock{
					StartLine: currentLine,
					Content:   content + "\n",
					Context:   fullContext,
				}
				continue
			}

			// 检测函数定义
			if strings.Contains(content, "function") {
				if currentBlock != nil {
					currentBlock.EndLine = currentLine - 1
					blocks = append(blocks, *currentBlock)
				}
				currentBlock = &CodeBlock{
					StartLine: currentLine,
					Content:   content + "\n",
					Context:   fullContext,
				}
				continue
			}

			// 检测变量定义或赋值语句
			if strings.Contains(content, "=") || strings.Contains(content, "var ") ||
				strings.Contains(content, "let ") || strings.Contains(content, "const ") {
				if currentBlock != nil &&
					!strings.Contains(currentBlock.Content, "function") {
					currentBlock.EndLine = currentLine - 1
					blocks = append(blocks, *currentBlock)
					currentBlock = nil
				}
				if currentBlock == nil {
					currentBlock = &CodeBlock{
						StartLine: currentLine,
						Content:   content + "\n",
						Context:   fullContext,
					}
				} else {
					currentBlock.Content += content + "\n"
				}
				continue
			}

			// 检测控制结构
			if strings.Contains(content, "if ") || strings.Contains(content, "for ") ||
				strings.Contains(content, "while ") || strings.Contains(content, "foreach ") {
				if currentBlock != nil {
					currentBlock.EndLine = currentLine - 1
					blocks = append(blocks, *currentBlock)
				}
				currentBlock = &CodeBlock{
					StartLine: currentLine,
					Content:   content + "\n",
					Context:   fullContext,
				}
				continue
			}

			// 将相关代码添加到当前块
			if currentBlock != nil {
				currentBlock.Content += content + "\n"
			} else {
				currentBlock = &CodeBlock{
					StartLine: currentLine,
					Content:   content + "\n",
					Context:   fullContext,
				}
			}
		}
	}

	// 处理最后一个代码块
	if currentBlock != nil {
		currentBlock.EndLine = currentLine
		blocks = append(blocks, *currentBlock)
	}

	// 合并过小的代码块
	var mergedBlocks []CodeBlock
	var lastBlock *CodeBlock

	for i := 0; i < len(blocks); i++ {
		if lastBlock == nil {
			lastBlock = &blocks[i]
			continue
		}

		// 如果当前块很小（比如只有1-2行）并且与上一个块相邻，则合并
		if blocks[i].StartLine-lastBlock.EndLine <= 2 &&
			blocks[i].EndLine-blocks[i].StartLine <= 2 {
			lastBlock.EndLine = blocks[i].EndLine
			lastBlock.Content += blocks[i].Content
		} else {
			mergedBlocks = append(mergedBlocks, *lastBlock)
			lastBlock = &blocks[i]
		}
	}

	if lastBlock != nil {
		mergedBlocks = append(mergedBlocks, *lastBlock)
	}

	return mergedBlocks
}

func handleWebhook(w http.ResponseWriter, r *http.Request, config Config) {
	fmt.Println("接收到请求了")

	// 打印请求体以便调试
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Printf("Error reading request body: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 重要：重新设置请求体，因为已经被读取
	r.Body = ioutil.NopCloser(bytes.NewBuffer(body))

	fmt.Printf("Received webhook payload: %s\n", string(body))

	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var event MergeRequestEvent
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 只处理 MR 打开或更新的事件
	if event.ObjectKind != "merge_request" ||
		(event.ObjectAttributes.State != "opened" && event.ObjectAttributes.State != "updated") {
		return
	}

	// 获取 MR 的改动
	mrDetail, err := getMRChanges(config, event.Project.PathWithNamespace, event.ObjectAttributes.ID)
	if err != nil {
		log.Printf("Error getting MR changes: %v", err)
		return
	}

	// 检查是否获取到了必要的 SHA
	if mrDetail.DiffRefs.HeadSHA == "" {
		log.Printf("Error: Missing HeadSHA in MR response")
		return
	}

	for _, change := range mrDetail.Changes {
		if strings.TrimSpace(change.Diff) == "" {
			continue
		}

		// 获取所有代码块
		codeBlocks := parseDiff(change.Diff)
		if len(codeBlocks) == 0 {
			continue
		}

		// 合并所有代码块的内容，作为一个完整的文件内容进行审查
		var fileContent strings.Builder
		fileContent.WriteString(fmt.Sprintf("文件: %s\n\n", change.NewPath))
		for _, block := range codeBlocks {
			fileContent.WriteString(block.Content)
		}

		// 对整个文件进行审查
		review, err := reviewCode(fileContent.String(), config)
		if err != nil || review == "" {
			continue
		}

		// 创建文件级别的评论
		position := LinePosition{
			BaseSHA:  mrDetail.DiffRefs.BaseSHA,
			StartSHA: mrDetail.DiffRefs.StartSHA,
			HeadSHA:  mrDetail.DiffRefs.HeadSHA,
			OldPath:  change.OldPath,
			NewPath:  change.NewPath,
			NewLine:  1, // 将评论放在文件开头
		}

		// 构建评论内容
		commentText := fmt.Sprintf("### 文件 `%s` 的代码审查意见：\n\n%s",
			change.NewPath, review)

		err = addLineComment(config, event.Project.PathWithNamespace,
			event.ObjectAttributes.ID, commentText, position)
		if err != nil {
			log.Printf("Error adding file comment: %v", err)
			continue
		}
	}
}

func main() {
	config := loadConfig()

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		handleWebhook(w, r, config)
	})

	log.Printf("Starting server on port %s", config.WebhookPort)
	log.Fatal(http.ListenAndServe(":"+config.WebhookPort, nil))
}
