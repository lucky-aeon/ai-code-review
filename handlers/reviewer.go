package handlers

import (
	"fmt"
	"log"
	"strings"

	"bot-code-review/github"
	"bot-code-review/models"
	"bot-code-review/utils"
)

// ReviewFileChange 评审单个文件的变更
func ReviewFileChange(change models.GitHubDiff) ([]models.Comment, error) {
	log.Printf("🔍 正在评审文件: %s", change.Filename)
	log.Printf("📄 代码片段预览:\n%s", utils.GetPreviewDiff(change.Patch))

	// 获取文件语言类型
	language := utils.GetLanguageFromPath(change.Filename)

	// 修改提示词，使用简单分隔符格式
	prompt := fmt.Sprintf(`# 代码审查专家

## 审查要求

- 请对以下代码文件进行详细且全面的审查，识别并分析潜在的编码问题。
- 审查过程中，您需要特别关注以下方面，并给予详细反馈：
  - **数组越界访问**：优先检查代码中是否有任何数组或切片索引超出有效范围的情况，这些会导致运行时错误或程序崩溃。例如在Go语言中，array[10]访问5个元素的数组。
  - **逻辑错误**：检查代码中可能存在的逻辑漏洞或错误的实现，如空指针引用等，确保代码行为符合预期。
  - **变量重复声明**：检查是否存在变量重复声明的情况，例如使用:=对已存在的变量重新赋值。
  - **编程规范和最佳实践**：查找违反行业标准的部分，如不合适的命名、不清晰的函数设计或不合理的代码结构。
  - **可维护性**：分析代码是否易于后续的维护、扩展与修改，是否有冗余代码、重复逻辑等。
  - **可读性**：确保代码结构清晰、命名规范，易于理解和调试。注释是否足够清晰、完整。
  - **性能**：检查是否有明显的性能瓶颈或不必要的复杂度，例如低效的算法、重复的计算等。
  - **安全性**：检查是否存在可能的安全漏洞，例如 SQL 注入、跨站脚本攻击（XSS）、敏感数据泄露等问题。

- 非常重要：如果发现任何数组索引超出范围的问题，必须优先报告这些问题，不能忽略。
- 对每个发现的问题，必须标明具体的行号并提供明确的问题描述和解决方案建议。
- 请确保评审语言为中文，且清晰表达审查结果。
- 对于行号，请确保使用代码差异中的新文件行号（"+"号后面的代码行）。
- 输出时请严格按照以下格式，每行一个问题：

  ISSUE|行号|问题描述|解决方案

  例如：
  ISSUE|927|数组索引越界访问将导致运行时错误|数组array长度为5，索引10超出了有效范围[0:4]，建议使用有效的索引范围或添加边界检查。
  
  如果没有发现问题，请输出：NOISSUES

## 语言: %s
## 文件: %s
## 代码:
%s`, language, change.Filename, change.Patch)

	aiResp, err := utils.CallAI(prompt)
	if err != nil {
		return nil, err
	}

	log.Printf("🤖 AI响应: %s", aiResp)

	// 解析AI响应获取评论
	comments := utils.ParseAIResponseJSON(aiResp, change.Patch)

	// 如果AI未发现问题，尝试使用本地规则检测常见问题
	if len(comments) == 0 {
		log.Printf("🔍 AI未发现问题，尝试使用本地规则检测...")
		comments = utils.DetectCommonIssues(change.Patch)
	}

	log.Printf("📋 解析得到评论: %+v", comments)

	// 仅校验行号的有效性
	var validComments []models.Comment
	diffLines := strings.Split(change.Patch, "\n")
	lineRanges := utils.ParseLineRanges(diffLines)

	for _, comment := range comments {
		if utils.IsLineInRanges(comment.Line, lineRanges) {
			validComments = append(validComments, comment)
			log.Printf("✅ 有效评论 - 行 %d: %s", comment.Line, comment.Content)
		} else {
			log.Printf("❌ 行号无效 - 行 %d: %s", comment.Line, comment.Content)
		}
	}

	log.Printf("🎯 最终有效评论数: %d", len(validComments))
	return validComments, nil
}

// HandleCodeReview 处理代码评审流程
func HandleCodeReview(event models.PullRequestEvent) {
	log.Printf("🚀 开始评审 MR !%d 在项目 %s",
		event.PullRequest.Number, event.Repository.FullName)

	// 获取MR的变更
	changes, err := github.GetMRChanges(event)
	if err != nil {
		log.Printf("❌ 获取MR变更失败: %v", err)
		return
	}

	log.Printf("📝 发现 %d 个文件需要评审", len(changes))

	// 评审每个文件
	reviewedFiles := 0
	commentCount := 0

	for _, change := range changes {
		comments, err := ReviewFileChange(change)
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
			if err := github.CreateNote(event, change, comment); err != nil {
				log.Printf("❌ 创建评论失败 %s 行 %d: %v", change.Filename, comment.Line, err)
			} else {
				log.Printf("💬 已在 %s 行 %d 创建评论", change.Filename, comment.Line)
			}
		}
	}

	log.Printf("🏁 评审完成: %d 个文件有问题, 共发布 %d 条评论",
		reviewedFiles, commentCount)
}

// TestArrayBoundsCheck 用于测试数组索引越界检测功能
func TestArrayBoundsCheck() {
	// 创建一个模拟的PR修改，包含数组越界访问问题
	mockPatch := `@@ -924,7 +924,15 @@ func test() {
	array := [5]int{1, 2, 3, 4, 5}

	// 正确的数组访问
+	fmt.Println("访问有效索引:", array[10])
+	fmt.Println("访问有效索引:", array[12])
+
+	// 重复声明问题
+	array := [5]int{1, 2, 3, 4, 5}
+
+	// 再次访问
+	fmt.Println("访问有效索引:", array[4])
 }`

	log.Println("🧪 开始测试数组越界检测功能")

	// 创建模拟的GitHubDiff对象
	mockDiff := models.GitHubDiff{
		Filename: "test.go",
		Status:   "modified",
		Patch:    mockPatch,
	}

	// 调用reviewFileChange函数进行代码审查
	comments, err := ReviewFileChange(mockDiff)
	if err != nil {
		log.Printf("❌ 测试失败: %v", err)
		return
	}

	// 检查是否发现了问题
	if len(comments) > 0 {
		log.Printf("✅ 测试通过: 发现了 %d 个问题", len(comments))
		for i, comment := range comments {
			log.Printf("  问题 %d: 行 %d - %s", i+1, comment.Line, comment.Content)
		}
	} else {
		log.Printf("❌ 测试失败: 未能检测到数组越界问题")
	}
}
