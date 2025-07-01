package tts

import (
	"fmt"
	"os"
	"path/filepath"
	"xiaozhi-server-go/src/core/providers"
)

// Config TTS配置结构
type GoogleTTSConfig struct {
	CredentialsFile string `yaml:"credentials_file"`
}

type Config struct {
	Type string
	Data map[string]interface{}
}

//type GoogleTTS struct {
//	*tts.BaseProvider // Kế thừa các thuộc tính và phương thức chung
//	credsFile         string
//	OutputDir         string  `yaml:"output_dir"`
//	Voice             string  `yaml:"voice,omitempty"`
//	Format            string  `yaml:"format,omitempty"`
//	SampleRate        int     `yaml:"sample_rate,omitempty"`
//	AppID             string  `yaml:"appid"`
//	Token             string  `yaml:"token"`
//	Cluster           string  `yaml:"cluster"`
//	LanguageCode      string  `yaml:"language_code,omitempty"`
//	SpeakingRate      float64 `yaml:"speaking_rate,omitempty"`
//	Pitch             float64 `yaml:"pitch,omitempty"`
//}

//type Config struct {
//	TTS struct {
//		GoogleTTS GoogleTTSConfig `yaml:"GoogleTTS"`
//	} `yaml:"TTS"`

//}

//type Config struct {
//	Type            string  `yaml:"type"`
//	OutputDir       string  `yaml:"output_dir"`
//	Voice           string  `yaml:"voice,omitempty"`
//	Format          string  `yaml:"format,omitempty"`
//	SampleRate      int     `yaml:"sample_rate,omitempty"`
//	AppID           string  `yaml:"appid"`
//	Token           string  `yaml:"token"`
//	Cluster         string  `yaml:"cluster"`
//	LanguageCode    string  `yaml:"language_code,omitempty"`
//	SpeakingRate    float64 `yaml:"speaking_rate,omitempty"`
//	Pitch           float64 `yaml:"pitch,omitempty"`
//	CredentialsFile string  `yaml:"credentials_file"`
//}

// Provider TTS提供者接口
type Provider interface {
	providers.TTSProvider
}

// BaseProvider TTS基础实现
type BaseProvider struct {
	config     *Config
	deleteFile bool
}

// Config 获取配置
func (p *BaseProvider) Config() *Config {
	return p.config
}

// DeleteFile 获取是否删除文件标志
func (p *BaseProvider) DeleteFile() bool {
	return p.deleteFile
}

// NewBaseProvider 创建TTS基础提供者
func NewBaseProvider(config *Config, deleteFile bool) *BaseProvider {
	return &BaseProvider{
		config:     config,
		deleteFile: deleteFile,
	}
}

// Initialize 初始化提供者
func (p *BaseProvider) Initialize() error {
	// Kiểm tra xem config và Data có nil không để tránh panic
	if p.config == nil || p.config.Data == nil {
		return fmt.Errorf("cấu hình (config) hoặc config.Data chưa được khởi tạo")
	}

	// Lấy giá trị OutputDir một cách an toàn
	outputDir, ok := p.config.Data["output_dir"].(string)
	if !ok {
		// 'ok' sẽ là false nếu key không tồn tại hoặc giá trị không phải là string
		return fmt.Errorf("key 'OutputDir' không tồn tại trong config.Data hoặc không phải là chuỗi (string)")
	}

	if outputDir == "" {
		return fmt.Errorf("giá trị của 'OutputDir' không được để trống")
	}

	// Bây giờ mới sử dụng giá trị một cách an toàn
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("tạo thư mục output '%s' thất bại: %v", outputDir, err)
	}
	return nil
}

// Cleanup 清理资源
func (p *BaseProvider) Cleanup() error {
	if p.deleteFile {
		// 清理输出目录中的临时文件
		pattern := filepath.Join(p.config.Data["output_dir"].(string), "*.{wav,mp3,opus}")
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("查找临时文件失败: %v", err)
		}
		for _, file := range matches {
			if err := os.Remove(file); err != nil {
				return fmt.Errorf("删除临时文件失败: %v", err)
			}
		}
	}
	return nil
}

// Factory TTS工厂函数类型
type Factory func(config *Config, deleteFile bool) (Provider, error)

var (
	factories = make(map[string]Factory)
)

// Register 注册TTS提供者工厂
func Register(name string, factory Factory) {
	factories[name] = factory
}

// Create 创建TTS提供者实例
func Create(name string, config *Config, deleteFile bool) (Provider, error) {
	factory, ok := factories[name]
	if !ok {
		return nil, fmt.Errorf("未知的TTS提供者: %s", name)
	}

	provider, err := factory(config, deleteFile)
	if err != nil {
		return nil, fmt.Errorf("创建TTS提供者失败: %v", err)
	}

	if err := provider.Initialize(); err != nil {
		return nil, fmt.Errorf("初始化TTS提供者失败: %v", err)
	}

	return provider, nil
}
