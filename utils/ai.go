package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bot-code-review/config"
	"bot-code-review/models"
)

// CallAI 调用AI服务进行代码评审
func CallAI(prompt string) (string, error) {
	// 准备请求体，不再添加 JSON 格式指导
	requestBody := map[string]interface{}{
		"model":    config.GlobalConfig.Model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}

	// 创建HTTP请求
	req, err := http.NewRequest("POST", config.GlobalConfig.BaseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.GlobalConfig.APIKey))

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

// ParseAIResponseJSON 解析AI的响应并提取评论
func ParseAIResponseJSON(resp string, diff string) []models.Comment {
	var comments []models.Comment
	lines := strings.Split(resp, "\n")

	log.Printf("🔍 解析AI响应，共 %d 行", len(lines))

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		log.Printf("📝 处理行: %s", line)

		if line == "NOISSUES" {
			log.Printf("✅ AI未发现问题")
			return comments
		}

		if strings.HasPrefix(line, "ISSUE|") {
			parts := strings.Split(line, "|")
			if len(parts) >= 4 {
				lineNumStr := parts[1]
				problem := parts[2]
				solution := parts[3]

				log.Printf("🔹 解析行号: %s, 问题: %s, 解决方案: %s", lineNumStr, problem, solution)

				lineNum, err := strconv.Atoi(lineNumStr)
				if err != nil {
					log.Printf("❌ 无效的行号: %s", lineNumStr)
					continue
				}

				// 确认这是一个被修改的行，才添加评论
				if IsAddedLine(diff, lineNum) {
					comment := models.Comment{
						Line:    lineNum,
						Content: fmt.Sprintf("问题: %s | 建议: %s", problem, solution),
					}
					comments = append(comments, comment)
					log.Printf("✅ 添加评论到行 %d", lineNum)
				} else {
					log.Printf("❌ 行 %d 不是修改行，跳过", lineNum)
				}
			} else {
				log.Printf("❌ 无效的ISSUE格式: %s, 部分数: %d", line, len(parts))
			}
		} else if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "```") {
			log.Printf("⚠️ 忽略未知格式行: %s", line)
		}
	}

	return comments
}

// ParseAIResponseFallback 备用解析函数
func ParseAIResponseFallback(aiResp string, diff string) []models.Comment {
	var comments []models.Comment

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

				comment := models.Comment{
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

			comment := models.Comment{
				Line:    lineNum,
				Content: fmt.Sprintf("%s|%s", problem, solution),
			}

			comments = append(comments, comment)
			log.Printf("  🔹 从备用解析评论 - 行 %d: %s", lineNum, problem)
		}
	}

	return comments
}

// IsAddedLine 检查指定行是否是新增行
func IsAddedLine(diff string, lineNum int) bool {
	lines := strings.Split(diff, "\n")
	currentLine := 0
	inHeader := true

	log.Printf("📊 检查行 %d 是否是新增行，diff共有 %d 行", lineNum, len(lines))

	for i, line := range lines {
		if inHeader && strings.HasPrefix(line, "@@") {
			re := regexp.MustCompile(`@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
			matches := re.FindStringSubmatch(line)
			if len(matches) >= 2 {
				start, _ := strconv.Atoi(matches[1])
				currentLine = start - 1
				inHeader = false
				log.Printf("📍 找到diff头部行 %d: %s, 新文件起始行: %d", i, line, start)
			}
			continue
		}

		if !inHeader {
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				currentLine++
				log.Printf("➕ 行 %d (diff行 %d): %s, 当前行号: %d", i, currentLine, line[:min(30, len(line))], currentLine)
				if currentLine == lineNum {
					log.Printf("✅ 行 %d 是新增行", lineNum)
					return true
				}
			} else if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				// 上下文行，只有空格前缀
				if strings.HasPrefix(line, " ") {
					currentLine++
					log.Printf("◻️ 行 %d (diff行 %d): %s, 当前行号: %d", i, currentLine, line[:min(30, len(line))], currentLine)
				}
			} else {
				// 删除行或其他
				log.Printf("➖ 行 %d: %s", i, line[:min(30, len(line))])
			}
		}
	}

	log.Printf("❌ 行 %d 不是新增行", lineNum)
	return false
}

// 辅助函数：获取两个整数中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
