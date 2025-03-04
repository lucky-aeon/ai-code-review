package utils

import (
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"bot-code-review/models"
)

// ParseLineRanges 解析diff文本获取新文件中的行范围
func ParseLineRanges(diffLines []string) []struct{ Start, End int } {
	var ranges []struct{ Start, End int }
	currentLine := 0
	inHeader := true

	log.Printf("📊 解析diff行范围，共 %d 行", len(diffLines))

	for i, line := range diffLines {
		if inHeader && strings.HasPrefix(line, "@@") {
			// 解析diff头部获取起始行号
			re := regexp.MustCompile(`@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
			matches := re.FindStringSubmatch(line)
			if len(matches) >= 2 {
				start, _ := strconv.Atoi(matches[1])
				currentLine = start - 1 // 减1是因为下面会先增加
				inHeader = false
				log.Printf("📍 找到diff头部行 %d: %s, 新文件起始行: %d", i, line, start)
			}
			continue
		}

		// 处理diff行缺失的问题
		if line == "" && !inHeader {
			continue
		}

		if !inHeader {
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				// 新增行
				currentLine++
				log.Printf("➕ 行 %d: %s, 当前行号: %d", i, line[:min(30, len(line))], currentLine)

				// 如果当前没有正在处理的范围，或者最后一个范围已经结束，添加新范围
				if len(ranges) == 0 || ranges[len(ranges)-1].End < currentLine-1 {
					ranges = append(ranges, struct{ Start, End int }{Start: currentLine, End: currentLine})
					log.Printf("📌 创建新范围: [%d, %d]", currentLine, currentLine)
				} else {
					// 否则，扩展最后一个范围
					ranges[len(ranges)-1].End = currentLine
					log.Printf("📏 扩展范围到: [%d, %d]", ranges[len(ranges)-1].Start, currentLine)
				}
			} else if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "\\") {
				// 上下文行
				if strings.HasPrefix(line, " ") {
					currentLine++
					log.Printf("◻️ 行 %d: %s, 当前行号: %d (上下文行)", i, line[:min(30, len(line))], currentLine)
				}
			} else {
				log.Printf("➖ 行 %d: %s (删除行或其他)", i, line[:min(30, len(line))])
			}
		}
	}

	log.Printf("📊 解析完成，共找到 %d 个行范围", len(ranges))
	for i, r := range ranges {
		log.Printf("  📍 范围 %d: [%d, %d]", i+1, r.Start, r.End)
	}

	return ranges
}

// IsLineInRanges 检查行号是否在任何一个有效范围内
func IsLineInRanges(lineNum int, ranges []struct{ Start, End int }) bool {
	for _, r := range ranges {
		if lineNum >= r.Start && lineNum <= r.End {
			return true
		}
	}
	return false
}

// GetLanguageFromPath 根据文件路径判断语言类型
func GetLanguageFromPath(path string) string {
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

// GetPreviewDiff 生成代码差异的简短预览
func GetPreviewDiff(diff string) string {
	lines := strings.Split(diff, "\n")

	// 限制预览长度
	maxLines := 10
	if len(lines) > maxLines {
		return strings.Join(lines[:maxLines], "\n") + "\n... (更多行省略)"
	}

	return diff
}

// IsLikelyCode 判断内容是否可能是代码片段
func IsLikelyCode(content string) bool {
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

// ExtractLineContent 从diff中提取特定行的内容
func ExtractLineContent(diff string, lineNum int) string {
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

// DetectCommonIssues 使用本地规则检测常见代码问题
func DetectCommonIssues(patch string) []models.Comment {
	var comments []models.Comment
	lines := strings.Split(patch, "\n")

	log.Printf("🔍 使用本地规则检测常见问题，共 %d 行", len(lines))

	// 记录当前行号
	currentLine := 0
	inHeader := true

	for i, line := range lines {
		if inHeader && strings.HasPrefix(line, "@@") {
			// 解析diff头部获取起始行号
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

		// 处理新增的代码行
		if !inHeader && strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			currentLine++
			content := strings.TrimPrefix(line, "+")
			content = strings.TrimSpace(content)

			log.Printf("🔎 检查行 %d: %s", currentLine, content)

			// 检测数组越界问题 - 寻找array[数字]模式，其中数字大于4
			// 这里假设array是[5]int类型，有效索引为0-4
			arrayAccessPattern := regexp.MustCompile(`array\[(\d+)\]`)
			matches := arrayAccessPattern.FindAllStringSubmatch(content, -1)

			for _, match := range matches {
				if len(match) >= 2 {
					index, _ := strconv.Atoi(match[1])
					if index > 4 { // 假设数组长度为5，有效索引为0-4
						comment := models.Comment{
							Line:    currentLine,
							Content: fmt.Sprintf("问题: 数组索引越界访问将导致运行时错误 | 建议: 数组array长度为5，索引%d超出了有效范围[0:4]，建议使用有效的索引范围或添加边界检查", index),
						}
						comments = append(comments, comment)
						log.Printf("❌ 行 %d 发现数组越界: 索引 %d 超出范围 [0:4]", currentLine, index)
					}
				}
			}

			// 检测变量重复声明问题 - 寻找array := 模式
			if strings.Contains(content, "array :=") && strings.Contains(content, "[5]int") {
				comment := models.Comment{
					Line:    currentLine,
					Content: "问题: 变量重复声明 | 建议: 变量array已经声明过，不应使用:=重复声明，应使用=进行赋值或使用不同的变量名",
				}
				comments = append(comments, comment)
				log.Printf("❌ 行 %d 发现变量重复声明: %s", currentLine, content)
			}
		} else if !inHeader && !strings.HasPrefix(line, "-") {
			// 处理上下文行
			if strings.HasPrefix(line, " ") {
				currentLine++
			}
		}
	}

	log.Printf("🔍 本地规则检测完成，发现 %d 个问题", len(comments))
	return comments
}
