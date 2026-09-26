// Command mcp exposes the DeepSider2API gateway to MCP clients over stdio.
//
// It talks to a running gateway over HTTP. Configure with:
//
//	DEEPSIDER_BASE_URL   default http://127.0.0.1:7863
//	DEEPSIDER_API_KEY    default: api_key from config.json next to this binary
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

var (
	baseURL string
	apiKey  string
	httpc   = &http.Client{Timeout: 300 * time.Second}
)

func main() {
	baseURL, apiKey = loadConfig()

	s := server.NewMCPServer("deepsider2api", "1.0.0", server.WithToolCapabilities(false))

	s.AddTool(mcp.NewTool("list_accounts",
		mcp.WithDescription("列出 DeepSider 账号池里的账号：邮箱、剩余积分、套餐、启用状态、失败次数。"),
	), handleListAccounts)

	s.AddTool(mcp.NewTool("total_credits",
		mcp.WithDescription("返回账号池所有启用账号的剩余积分总和。"),
	), handleTotalCredits)

	s.AddTool(mcp.NewTool("list_models",
		mcp.WithDescription("列出可用的 DeepSider 模型（botId / 名称 / 类型 / 积分）。"),
		mcp.WithString("type", mcp.Description("chat | image | video | all（默认 all）")),
	), handleListModels)

	s.AddTool(mcp.NewTool("get_invitation",
		mcp.WithDescription("查询某个账号的专属邀请码、邀请链接、已邀请人数与奖励积分。"),
		mcp.WithString("account", mcp.Required(), mcp.Description("账号邮箱或 id")),
	), handleGetInvitation)

	s.AddTool(mcp.NewTool("generate_image",
		mcp.WithDescription("用 DeepSider 生成图片，可指定账号与模型，返回图片 URL 与下载链接。"),
		mcp.WithString("prompt", mcp.Required(), mcp.Description("图片描述")),
		mcp.WithString("model", mcp.Description("图片模型 botId，如 pro/gemini-3.1-flash-lite-image；留空用默认")),
		mcp.WithString("account", mcp.Description("指定账号邮箱或 id；留空自动选账号")),
		mcp.WithString("size", mcp.Description("OpenAI size，如 1024x1024")),
		mcp.WithString("resolution", mcp.Description("1k / 2k / 4k")),
		mcp.WithString("ratio", mcp.Description("1:1 / 16:9 / 9:16 ...")),
	), handleGenerateImage)

	s.AddTool(mcp.NewTool("chat",
		mcp.WithDescription("和 DeepSider 文本模型对话（非流式），返回回复文本。"),
		mcp.WithString("prompt", mcp.Required(), mcp.Description("用户消息")),
		mcp.WithString("model", mcp.Description("模型 botId 或 auto；留空 auto")),
	), handleChat)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "mcp server error: %v\n", err)
		os.Exit(1)
	}
}

func loadConfig() (string, string) {
	base := os.Getenv("DEEPSIDER_BASE_URL")
	key := os.Getenv("DEEPSIDER_API_KEY")

	if exe, err := os.Executable(); err == nil {
		cfgPath := filepath.Join(filepath.Dir(exe), "config.json")
		if data, err := os.ReadFile(cfgPath); err == nil {
			var cfg struct {
				Listen string `json:"listen"`
				APIKey string `json:"api_key"`
			}
			if json.Unmarshal(data, &cfg) == nil {
				if key == "" {
					key = cfg.APIKey
				}
				if base == "" && cfg.Listen != "" {
					if _, port, err := net.SplitHostPort(cfg.Listen); err == nil {
						base = "http://127.0.0.1:" + port
					}
				}
			}
		}
	}
	if base == "" {
		base = "http://127.0.0.1:7863"
	}
	if key == "" {
		key = "change_me"
	}
	return strings.TrimRight(base, "/"), key
}

func doJSON(ctx context.Context, method, path string, body interface{}, out interface{}) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gateway %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
	}
	return nil
}

type accountDTO struct {
	Name            string  `json:"name"`
	Email           string  `json:"email"`
	CreditRemaining float64 `json:"credit_remaining"`
	PlanName        string  `json:"plan_name"`
	Enabled         bool    `json:"enabled"`
	FailCount       int     `json:"fail_count"`
	LastError       string  `json:"last_error"`
}

func fetchAccounts(ctx context.Context) ([]accountDTO, error) {
	var resp struct {
		Accounts []accountDTO `json:"accounts"`
	}
	if err := doJSON(ctx, http.MethodGet, "/api/panel/accounts", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Accounts, nil
}

func handleListAccounts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	accounts, err := fetchAccounts(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var b strings.Builder
	var total float64
	for _, a := range accounts {
		status := "启用"
		if !a.Enabled {
			status = "停用"
		}
		fmt.Fprintf(&b, "- %s | 积分 %.0f | 套餐 %s | %s", a.Email, a.CreditRemaining, a.PlanName, status)
		if a.FailCount > 0 {
			fmt.Fprintf(&b, " | 失败 %d", a.FailCount)
		}
		b.WriteString("\n")
		if a.Enabled {
			total += a.CreditRemaining
		}
	}
	fmt.Fprintf(&b, "\n合计（启用账号）: %.0f 积分，共 %d 个账号", total, len(accounts))
	return mcp.NewToolResultText(b.String()), nil
}

func handleTotalCredits(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	accounts, err := fetchAccounts(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var total float64
	var enabled int
	for _, a := range accounts {
		if a.Enabled {
			total += a.CreditRemaining
			enabled++
		}
	}
	return mcp.NewToolResultText(fmt.Sprintf("总剩余积分: %.0f（%d 个启用账号 / 共 %d 个）", total, enabled, len(accounts))), nil
}

func handleListModels(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	typ := strings.ToLower(strings.TrimSpace(req.GetString("type", "all")))
	var resp struct {
		Models []map[string]interface{} `json:"models"`
	}
	if err := doJSON(ctx, http.MethodGet, "/api/panel/models", nil, &resp); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var b strings.Builder
	for _, m := range resp.Models {
		botID, _ := m["botId"].(string)
		if botID == "" {
			continue
		}
		if disabled, _ := m["disabled"].(bool); disabled {
			continue
		}
		title, _ := m["title"].(string)
		class, _ := m["classification"].(string)
		isDrawing, _ := m["isDrawing"].(bool)
		asyncVideo, _ := m["asyncGenerateVideo"].(bool)
		kind := "chat"
		if isDrawing || class == "image" {
			kind = "image"
		} else if asyncVideo || class == "video" {
			kind = "video"
		}
		if typ != "all" && typ != kind {
			continue
		}
		fmt.Fprintf(&b, "- [%s] %s | %s | %v 分\n", kind, title, botID, m["credits"])
	}
	if b.Len() == 0 {
		return mcp.NewToolResultText("没有匹配的模型"), nil
	}
	return mcp.NewToolResultText(b.String()), nil
}

func handleGetInvitation(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	account, err := req.RequireString("account")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var resp struct {
		Email         string `json:"email"`
		InvitationID  string `json:"invitation_id"`
		Desc          string `json:"desc"`
		InvitedCount  int    `json:"invited_count"`
		RewardCredits int    `json:"reward_credits"`
	}
	path := "/api/panel/invitation?account=" + url.QueryEscape(account)
	if err := doJSON(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	link := "https://web.deepsider.online/?c=" + resp.InvitationID
	return mcp.NewToolResultText(fmt.Sprintf(
		"账号: %s\n邀请码: %s\n邀请链接: %s\n已邀请: %d 人\n奖励积分: %d\n%s",
		resp.Email, resp.InvitationID, link, resp.InvitedCount, resp.RewardCredits, resp.Desc)), nil
}

func handleGenerateImage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	prompt, err := req.RequireString("prompt")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	body := map[string]interface{}{
		"prompt":     prompt,
		"model":      req.GetString("model", ""),
		"account":    req.GetString("account", ""),
		"size":       req.GetString("size", ""),
		"resolution": req.GetString("resolution", ""),
		"ratio":      req.GetString("ratio", ""),
	}
	var resp struct {
		Model string `json:"model"`
		Data  []struct {
			URL         string `json:"url"`
			DownloadURL string `json:"download_url"`
		} `json:"data"`
	}
	if err := doJSON(ctx, http.MethodPost, "/api/panel/generate", body, &resp); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "模型: %s\n", resp.Model)
	for i, d := range resp.Data {
		fmt.Fprintf(&b, "图片 %d: %s\n下载: %s\n", i+1, d.URL, d.DownloadURL)
	}
	return mcp.NewToolResultText(b.String()), nil
}

func handleChat(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	prompt, err := req.RequireString("prompt")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	body := map[string]interface{}{
		"model":    req.GetString("model", "auto"),
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := doJSON(ctx, http.MethodPost, "/v1/chat/completions", body, &resp); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if len(resp.Choices) == 0 {
		return mcp.NewToolResultError("empty response"), nil
	}
	return mcp.NewToolResultText(resp.Choices[0].Message.Content), nil
}
