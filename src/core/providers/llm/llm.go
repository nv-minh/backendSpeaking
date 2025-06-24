package llm

import (
	"context"
	"fmt"

	"xiaozhi-server-go/src/core/types"
)

// Config LLM配置结构
type Config struct {
	Type         string                 `yaml:"type"`
	ModelName    string                 `yaml:"model_name"`
	BaseURL      string                 `yaml:"base_url,omitempty"`
	APIKey       string                 `yaml:"api_key,omitempty"`
	SystemPrompt string                 `yaml:"prompt,omitempty"`
	Temperature  float64                `yaml:"temperature,omitempty"`
	MaxTokens    int                    `yaml:"max_tokens,omitempty"`
	TopP         float64                `yaml:"top_p,omitempty"`
	Extra        map[string]interface{} `yaml:",inline"`
}

// Message định nghĩa cấu trúc một tin nhắn trong cuộc trò chuyện.
type Message struct {
	Role    string `json:"role"` // "user", "assistant", "system"
	Content string `json:"content"`
}

// LLMProvider là interface mà tất cả các nhà cung cấp LLM phải implement.
type LLMProvider interface {
	Initialize() error
	Cleanup() error
	Chat(ctx context.Context, sessionID string, prompt string) (*Message, error)
}

// Provider LLM提供者接口
type Provider interface {
	types.LLMProvider
}

// BaseProvider LLM基础实现
type BaseProvider struct {
	config *Config
}

// Config 获取配置
func (p *BaseProvider) Config() *Config {
	return p.config
}

// NewBaseProvider 创建LLM基础提供者
func NewBaseProvider(config *Config) *BaseProvider {
	return &BaseProvider{
		config: config,
	}
}

// Initialize 初始化提供者
func (p *BaseProvider) Initialize() error {
	return nil
}

// Cleanup 清理资源
func (p *BaseProvider) Cleanup() error {
	return nil
}

// Factory LLM工厂函数类型
type Factory func(config *Config) (Provider, error)

var (
	factories = make(map[string]Factory)
)

// Register 注册LLM提供者工厂
func Register(name string, factory Factory) {
	factories[name] = factory
}

// Create 创建LLM提供者实例
func Create(name string, config *Config) (Provider, error) {
	factory, ok := factories[name]
	if !ok {
		return nil, fmt.Errorf("未知的LLM提供者: %s", name)
	}

	provider, err := factory(config)
	if err != nil {
		return nil, fmt.Errorf("创建LLM提供者失败: %v", err)
	}

	if err := provider.Initialize(); err != nil {
		return nil, fmt.Errorf("初始化LLM提供者失败: %v", err)
	}

	return provider, nil
}
