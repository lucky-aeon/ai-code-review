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

## 通用代码审查指令

### 审查原则
1. 分层检查优先级：
   [致命错误] > [逻辑缺陷] > [安全漏洞] > [规范问题] > [优化建议]
2. 结合代码变更的上下文语义分析
3. 建议需符合软件工程通用准则

### 审查维度
#### 1. 运行时安全（最高优先级）
- **容器越界**：数组/列表/集合的索引越界访问
- **空值处理**：可能的空对象引用
- **资源管理**：未关闭的句柄、连接泄漏
- **并发问题**：竞态条件、死锁、线程安全

#### 2. 逻辑正确性
- **边界条件**：循环终止条件、极值处理
- **状态完整性**：状态机转换缺失
- **异常处理**：未捕获可能异常
- **计算逻辑**：错误的条件判断

#### 3. 代码规范
- **命名规范**：有意义的命名，符合语言惯用风格
- **函数设计**：单一职责原则，合理参数设计
- **结构组织**：模块化程度，文件合理拆分
- **格式规范**：统一缩进、括号风格

#### 4. 可维护性
- **重复代码**：相同逻辑出现≥3次需抽象
- **复杂度**：过长的函数/类（建议函数≤50行）
- **依赖管理**：模块间耦合度
- **文档覆盖**：关键算法需有注释说明

#### 5. 性能优化
- **算法效率**：避免不必要的高复杂度操作
- **内存使用**：减少不必要的对象创建
- **资源复用**：连接池、缓存机制
- **批量操作**：减少频繁的IO操作

#### 6. 安全审计
- **注入防御**：SQL/命令/模板注入风险
- **数据校验**：未过滤的用户输入
- **敏感数据**：硬编码凭证、日志泄露
- **加密存储**：弱哈希算法、不安全的随机数

### 输出规范
ISSUE|行号|问题描述|解决方案

示例输出：
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
