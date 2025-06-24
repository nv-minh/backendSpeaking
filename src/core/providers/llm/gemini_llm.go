// File: src/core/llm/gemini.go
package llm

import (
	"context"
	"fmt"
	"sync"
	"xiaozhi-server-go/src/configs"
	"xiaozhi-server-go/src/core/types"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/sashabaranov/go-openai" // Cần thiết cho ResponseWithFunctions
	// Import các package cần thiết
	"xiaozhi-server-go/src/core/providers"
)

// GeminiProvider implement đầy đủ interface providers.LLMProvider
type GeminiProvider struct {
	*BaseProvider
	client   *genai.Client
	sessions map[string]*genai.ChatSession
	mu       sync.Mutex
}

// NewGeminiProvider là factory function được gọi bởi hàm llm.Create.
func NewGeminiProvider(config *Config) (Provider, error) {
	base := NewBaseProvider(config)
	return &GeminiProvider{
		BaseProvider: base,
		sessions:     make(map[string]*genai.ChatSession),
	}, nil
}

// Initialize khởi tạo client kết nối tới Gemini API.
func (p *GeminiProvider) Initialize() error {
	cfg := p.Config()
	if cfg.APIKey == "" {
		return fmt.Errorf("gemini provider yêu cầu 'api_key'")
	}
	client, err := genai.NewClient(context.Background(), option.WithAPIKey(cfg.APIKey))
	if err != nil {
		return fmt.Errorf("không thể tạo genai client: %v", err)
	}
	p.client = client
	return nil
}

// Cleanup đóng kết nối client.
func (p *GeminiProvider) Cleanup() error {
	if p.client != nil {
		if err := p.client.Close(); err != nil {
			return err
		}
		p.client = nil
	}
	return nil
}

// Chat implement phần `Chat` của interface LLMProvider.
func (p *GeminiProvider) Chat(ctx context.Context, sessionID string, prompt string) (*providers.Message, error) {
	p.mu.Lock()

	chatSession, exists := p.sessions[sessionID]
	if !exists {
		modelName := p.Config().ModelName
		if modelName == "" {
			modelName = "gemini-1.5-flash-latest"
		}
		model := p.client.GenerativeModel(modelName)
		chatSession = model.StartChat()
		config, _, _ := configs.LoadConfig()
		systemPrompt := config.DefaultPrompt
		if systemPrompt != "" {
			// Thiết lập lịch sử ban đầu với vai trò của AI
			// Việc này giúp "khóa" vai trò của AI ngay từ đầu.
			// Chúng ta coi system prompt là lời 'chỉ thị' của người dùng,
			// và AI trả lời 'OK' để xác nhận đã nhận chỉ thị.
			chatSession.History = []*genai.Content{
				{
					Parts: []genai.Part{genai.Text(systemPrompt)},
					Role:  "user",
				},
				{
					Parts: []genai.Part{genai.Text("Got it. I'll be Lida from now on. lol, let's see what you've got.")},
					Role:  "model", // 'model' là vai của AI trong thư viện Gemini
				},
			}
		}
		p.sessions[sessionID] = chatSession
	}

	p.mu.Unlock()

	resp, err := chatSession.SendMessage(ctx, genai.Text(prompt))
	if err != nil {
		return nil, fmt.Errorf("lỗi khi gửi tin nhắn tới Gemini: %v", err)
	}

	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		var content string
		for _, part := range resp.Candidates[0].Content.Parts {
			if txt, ok := part.(genai.Text); ok {
				content += string(txt)
			}
		}
		if content != "" {
			return &providers.Message{
				Role:    "assistant",
				Content: content,
			}, nil
		}
	}

	return nil, fmt.Errorf("không nhận được nội dung hợp lệ từ Gemini")
}

// Response implement phần streaming `Response` của interface LLMProvider.
func (p *GeminiProvider) Response(ctx context.Context, sessionID string, messages []providers.Message) (<-chan string, error) {
	outChan := make(chan string)

	if len(messages) == 0 {
		return nil, fmt.Errorf("danh sách tin nhắn không được để trống")
	}
	lastMessage := messages[len(messages)-1]
	if lastMessage.Role != "user" {
		return nil, fmt.Errorf("tin nhắn cuối cùng trong lịch sử phải từ 'user'")
	}
	prompt := lastMessage.Content

	go func() {
		defer close(outChan)

		p.mu.Lock()
		chatSession, exists := p.sessions[sessionID]
		if !exists {
			modelName := p.Config().ModelName
			if modelName == "" {
				modelName = "gemini-1.5-flash-latest"
			}
			model := p.client.GenerativeModel(modelName)
			chatSession = model.StartChat()
			// Chuyển đổi lịch sử tin nhắn
			for _, msg := range messages[:len(messages)-1] {
				chatSession.History = append(chatSession.History, &genai.Content{
					Parts: []genai.Part{genai.Text(msg.Content)},
					Role:  msg.Role,
				})
			}
			p.sessions[sessionID] = chatSession
		}
		p.mu.Unlock()

		stream := chatSession.SendMessageStream(ctx, genai.Text(prompt))
		for {
			resp, err := stream.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				fmt.Printf("Lỗi khi nhận phản hồi stream: %v\n", err)
				break
			}

			if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
				for _, part := range resp.Candidates[0].Content.Parts {
					if text, ok := part.(genai.Text); ok {
						outChan <- string(text)
					}
				}
			}
		}
	}()

	return outChan, nil
}

// ResponseWithFunctions implement phần `ResponseWithFunctions` của interface LLMProvider.
// API Gemini hiện tại không hỗ trợ trực tiếp Function Calling kiểu OpenAI, vì vậy chúng ta trả về lỗi.
func (p *GeminiProvider) ResponseWithFunctions(ctx context.Context, sessionID string, messages []providers.Message, tools []openai.Tool) (<-chan types.Response, error) {
	return nil, fmt.Errorf("gemini provider hiện tại không hỗ trợ Function Calling theo kiểu OpenAI")
}

func init() {
	Register("gemini", NewGeminiProvider)
}
