package github

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

	"bot-code-review/config"
	"bot-code-review/models"
)

// GetMRChanges 获取合并请求的变更
func GetMRChanges(event models.PullRequestEvent) ([]models.GitHubDiff, error) {
	// 构建GitHub API URL，获取Pull Request的文件变更
	url := fmt.Sprintf("%s/repos/%s/pulls/%d/files",
		config.GlobalConfig.GithubHost,
		event.Repository.FullName,
		event.PullRequest.Number)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	// 设置GitHub API认证头
	req.Header.Set("Authorization", fmt.Sprintf("token %s", config.GlobalConfig.GithubToken))
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
	var files []models.GitHubDiff
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, err
	}

	return files, nil
}

// GetMRCommitInfo 获取PR的提交信息
func GetMRCommitInfo(event models.PullRequestEvent) (string, string, error) {
	// 使用Pull Request的head.sha作为head提交SHA
	headSHA := event.PullRequest.Head.SHA
	baseSHA := event.PullRequest.Base.SHA

	// 如果SHA为空，则通过API获取
	if headSHA == "" || baseSHA == "" {
		url := fmt.Sprintf("%s/repos/%s/pulls/%d",
			config.GlobalConfig.GithubHost,
			event.Repository.FullName,
			event.PullRequest.Number)

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return "", "", err
		}

		req.Header.Set("Authorization", fmt.Sprintf("token %s", config.GlobalConfig.GithubToken))
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

// CreateNote 在GitHub PR上创建评论
func CreateNote(event models.PullRequestEvent, change models.GitHubDiff, comment models.Comment) error {
	// 格式化评论内容为Markdown格式
	formattedNote := ""
	if strings.Contains(comment.Content, "|") {
		parts := strings.Split(comment.Content, "|")
		if len(parts) >= 2 {
			problem := strings.TrimSpace(parts[0])
			solution := strings.TrimSpace(parts[1])
			formattedNote = fmt.Sprintf("**%s**\n\n%s", problem, solution)
		} else {
			formattedNote = comment.Content
		}
	} else {
		formattedNote = comment.Content
	}

	// 确保评论格式正确
	formattedNote = strings.ReplaceAll(formattedNote, "问题:", "**问题:**")
	formattedNote = strings.ReplaceAll(formattedNote, "建议:", "**建议:**")

	// 获取PR的提交SHA
	_, headSHA, err := GetMRCommitInfo(event)
	if err != nil {
		return fmt.Errorf("无法获取PR提交信息: %v", err)
	}

	// 准备请求体 - 创建单个评论
	url := fmt.Sprintf("%s/repos/%s/pulls/%d/comments",
		config.GlobalConfig.GithubHost,
		event.Repository.FullName,
		event.PullRequest.Number)

	// 构建评论请求
	reviewPayload := map[string]interface{}{
		"commit_id": headSHA,
		"path":      change.Filename,
		"body":      formattedNote,
		"position":  GetDiffPosition(change.Patch, comment.Line),
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
	req.Header.Set("Authorization", fmt.Sprintf("token %s", config.GlobalConfig.GithubToken))
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

// GetDiffPosition 计算GitHub差异中的位置
func GetDiffPosition(patch string, newLine int) int {
	// GitHub的position是在差异中的行号（从1开始），而不是文件中的行号
	// 需要计算从patch开始的第几行
	lines := strings.Split(patch, "\n")
	position := 0
	currentLine := 0
	inHeader := true

	log.Printf("📊 计算行 %d 在diff中的位置，diff共有 %d 行", newLine, len(lines))

	for i, line := range lines {
		if inHeader && strings.HasPrefix(line, "@@") {
			// 解析diff头部获取起始行号
			re := regexp.MustCompile(`@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
			matches := re.FindStringSubmatch(line)
			if len(matches) >= 2 {
				start, _ := strconv.Atoi(matches[1])
				currentLine = start - 1 // GitHub从1开始计数
				inHeader = false
				log.Printf("📍 找到diff头部行 %d: %s, 新文件起始行: %d", i, line, start)
			}
			continue
		}

		if !inHeader {
			position++

			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				// 这是新增行
				currentLine++
				log.Printf("➕ 行 %d (position %d): %s, 当前行号: %d", i, position, line[:min(30, len(line))], currentLine)

				if currentLine == newLine {
					log.Printf("✅ 找到目标行 %d, 对应position: %d", newLine, position)
					return position
				}
			} else if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				// 上下文行，只有空格前缀
				if strings.HasPrefix(line, " ") {
					currentLine++
					log.Printf("◻️ 行 %d (position %d): %s, 当前行号: %d", i, position, line[:min(30, len(line))], currentLine)
				}
			}
		}
	}

	log.Printf("⚠️ 未找到行 %d 在diff中的对应位置，返回默认值1", newLine)
	return 1 // 默认返回1，避免为0
}

// 辅助函数：获取两个整数中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
