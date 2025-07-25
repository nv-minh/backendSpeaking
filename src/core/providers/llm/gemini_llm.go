package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"xiaozhi-server-go/src/configs"
	"xiaozhi-server-go/src/core/types"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/sashabaranov/go-openai"
	"xiaozhi-server-go/src/core/providers"
)

// GeminiProvider implement đầy đủ interface providers.LLMProvider
type GeminiProvider struct {
	*BaseProvider
	client   *genai.Client
	sessions map[string]*genai.ChatSession
	mu       sync.Mutex
}

func (p *GeminiProvider) StreamChat(ctx context.Context, sessionID, message string) (<-chan string, error) {
	outputChan := make(chan string)
	cs := p.client.GenerativeModel("gemini-1.5-flash-latest").StartChat()

	go func() {
		defer close(outputChan)

		iter := cs.SendMessageStream(ctx, genai.Text(message))

		for {
			resp, err := iter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				break
			}
			// Lấy nội dung text từ các response parts
			if len(resp.Candidates) > 0 && len(resp.Candidates[0].Content.Parts) > 0 {
				if txt, ok := resp.Candidates[0].Content.Parts[0].(genai.Text); ok {
					outputChan <- string(txt)
				}
			}
		}
	}()

	return outputChan, nil
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
func LoadJSONAsMap(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result map[string]interface{}
	err = json.Unmarshal(data, &result)
	return result, err
}

// Chat implement phần `Chat` của interface LLMProvider.
func (p *GeminiProvider) Chat(ctx context.Context, sessionID string, prompt string) (*types.LLMResponse, error) { // <--- THAY ĐỔI 1: Chữ ký hàm
	p.mu.Lock()
	defer p.mu.Unlock()
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
			chatSession.History = []*genai.Content{
				{
					Parts: []genai.Part{genai.Text(systemPrompt)},
					Role:  "user",
				},
				{
					Parts: []genai.Part{genai.Text("{\"reply\": \"OK, I understand my role. I will be an English teacher and always respond in the required JSON format.\", \"keywords\": []}")},
					Role:  "model",
				},
			}
		}
		p.sessions[sessionID] = chatSession
	}

	resp, err := chatSession.SendMessage(ctx, genai.Text(prompt))
	if err != nil {
		return nil, fmt.Errorf("lỗi khi gửi tin nhắn tới Gemini: %v", err)
	}

	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		var jsonContent string
		for _, part := range resp.Candidates[0].Content.Parts {
			if txt, ok := part.(genai.Text); ok {
				jsonContent += string(txt)
			}
		}

		if jsonContent != "" {
			// Tạo một biến để chứa kết quả sau khi unmarshal
			var llmResponse types.LLMResponse

			// Unmarshal chuỗi JSON vào struct
			if err := json.Unmarshal([]byte(jsonContent), &llmResponse); err != nil {
				log.Printf("Lỗi unmarshal JSON từ Gemini: %v. Raw response: %s", err, jsonContent)

				// Fallback: Nếu không phải JSON, coi toàn bộ là câu trả lời
				// và không có keywords
				return &types.LLMResponse{
					Reply:    jsonContent,
					Keywords: []string{},
				}, nil
			}

			// Trả về con trỏ tới struct mới đã được điền dữ liệu
			return &llmResponse, nil
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

func (p *GeminiProvider) ResponseWithFunctions(ctx context.Context, sessionID string, messages []providers.Message, tools []openai.Tool) (<-chan types.Response, error) {
	return nil, fmt.Errorf("gemini provider hiện tại không hỗ trợ Function Calling theo kiểu OpenAI")
}

func init() {
	Register("gemini", NewGeminiProvider)
}
