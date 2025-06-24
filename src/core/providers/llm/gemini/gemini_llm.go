package gemini_llm

import (
	"context"
	"fmt"
	"github.com/sashabaranov/go-openai"
	"log"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
	"xiaozhi-server-go/src/core/providers/llm"
	"xiaozhi-server-go/src/core/types"
)

func init() {
	llm.Register("gemini", NewProvider)
}

var _ types.LLMProvider = (*GeminiLLMProvider)(nil)

type GeminiLLMProvider struct {
	*llm.BaseProvider
	client *genai.Client
}

func NewProvider(config *llm.Config) (llm.Provider, error) {
	apiKey := config.APIKey
	if apiKey == "" {
		return nil, fmt.Errorf("không tìm thấy api_key trong cấu hình GeminiLLM")
	}
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("không thể tạo gemini client: %w", err)
	}
	return &GeminiLLMProvider{
		BaseProvider: llm.NewBaseProvider(config),
		client:       client,
	}, nil
}

// Response triển khai phương thức được yêu cầu bởi interface
func (p *GeminiLLMProvider) Response(ctx context.Context, sessionID string, messages []types.Message) (<-chan string, error) {
	responseChan := make(chan string)

	go func() {
		defer close(responseChan)

		model := p.client.GenerativeModel(p.Config().ModelName)

		// Chuyển đổi từ types.Message sang genai.Part
		var partsToSend []genai.Part
		for _, msg := range messages {
			partsToSend = append(partsToSend, genai.Text(msg.Content))
		}

		log.Printf("LLM <-- Gửi %d phần đến Gemini", len(partsToSend))

		resp, err := model.GenerateContent(ctx, partsToSend...)
		if err != nil {
			log.Printf("Lỗi GenerateContent từ Gemini: %v", err)
			return
		}

		if len(resp.Candidates) > 0 && len(resp.Candidates[0].Content.Parts) > 0 {
			if textPart, ok := resp.Candidates[0].Content.Parts[0].(genai.Text); ok {
				aiResponse := string(textPart)
				log.Printf("LLM --> Nhận từ Gemini: '%s'", aiResponse)
				responseChan <- aiResponse
			}
		}
	}()

	return responseChan, nil
}

// ResponseWithFunctions --- Các phương thức placeholder khác để tuân thủ interface ---
func (p *GeminiLLMProvider) ResponseWithFunctions(ctx context.Context, sessionID string, messages []types.Message, tools []openai.Tool) (<-chan types.Response, error) {
	return nil, fmt.Errorf("ResponseWithFunctions chưa được hỗ trợ cho Gemini")
}
func (p *GeminiLLMProvider) StartChatSession(systemPrompt string) types.ChatSession {
	return nil // Không dùng đến
}
