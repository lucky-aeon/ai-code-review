package handlers

import (
	"encoding/json"
	"io/ioutil"
	"net/http"

	"bot-code-review/models"
)

// HandleWebhook 处理GitHub的webhook请求
func HandleWebhook(w http.ResponseWriter, r *http.Request) {
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
	var prEvent models.PullRequestEvent
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
	go HandleCodeReview(prEvent)

	w.WriteHeader(http.StatusOK)
}
