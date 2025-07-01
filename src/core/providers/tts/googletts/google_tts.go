package googletts

import (
	"context"
	"fmt"
	"os"
	"time"

	texttospeech "cloud.google.com/go/texttospeech/apiv1"
	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
	"google.golang.org/api/option"
	"xiaozhi-server-go/src/core/providers/tts"
)

// GoogleTTSProvider triển khai interface tts.Provider.
type GoogleTTSProvider struct {
	*tts.BaseProvider
	client *texttospeech.Client
}

// NewProvider là factory function an toàn.
// Nó sẽ kiểm tra tất cả các key cần thiết trong config trước khi tạo provider.
func NewProvider(config *tts.Config, deleteFile bool) (tts.Provider, error) {
	// --- KIỂM TRA CONFIG MỘT CÁCH AN TOÀN ---
	if config == nil || config.Data == nil {
		return nil, fmt.Errorf("GoogleTTS: config.Data chưa được khởi tạo")
	}

	credsFile, ok := config.Data["credentials_file"].(string)
	if !ok || credsFile == "" {
		return nil, fmt.Errorf("GoogleTTS: cấu hình 'credentials_file' bị thiếu hoặc không phải là chuỗi")
	}

	// Kiểm tra các trường khác cần cho TTS
	if _, ok := config.Data["language_code"].(string); !ok {
		return nil, fmt.Errorf("GoogleTTS: cấu hình 'LanguageCode' bị thiếu hoặc không phải là chuỗi")
	}
	if _, ok := config.Data["voice"].(string); !ok {
		return nil, fmt.Errorf("GoogleTTS: cấu hình 'Voice' bị thiếu hoặc không phải là chuỗi")
	}
	if _, ok := config.Data["speaking_rate"].(float64); !ok {
		return nil, fmt.Errorf("GoogleTTS: cấu hình 'SpeakingRate' bị thiếu hoặc không phải là số thực")
	}
	if _, ok := config.Data["pitch"].(float64); !ok {
		return nil, fmt.Errorf("GoogleTTS: cấu hình 'Pitch' bị thiếu hoặc không phải là số thực")
	}

	// --- KHỞI TẠO CLIENT ---
	ctx := context.Background()
	client, err := texttospeech.NewClient(ctx, option.WithCredentialsFile(credsFile))
	if err != nil {
		return nil, fmt.Errorf("GoogleTTS: không thể tạo client: %v", err)
	}

	base := tts.NewBaseProvider(config, deleteFile)

	return &GoogleTTSProvider{
		BaseProvider: base,
		client:       client,
	}, nil
}

// Synthesize thực hiện chuyển văn bản thành giọng nói
func (p *GoogleTTSProvider) Synthesize(ctx context.Context, text, outputPath string) error {
	cfgData := p.Config().Data

	// Lấy các giá trị config một cách an toàn
	languageCode, ok := cfgData["language_code"].(string)
	if !ok {
		return fmt.Errorf("lỗi cấu hình TTS: thiếu LanguageCode")
	}

	voice, ok := cfgData["voice"].(string)
	if !ok {
		return fmt.Errorf("lỗi cấu hình TTS: thiếu Voice")
	}

	speakingRate, ok := cfgData["speaking_rate"].(float64)
	if !ok {
		return fmt.Errorf("lỗi cấu hình TTS: thiếu SpeakingRate")
	}

	pitch, ok := cfgData["pitch"].(float64)
	if !ok {
		return fmt.Errorf("lỗi cấu hình TTS: thiếu Pitch")
	}

	// Xây dựng yêu cầu gửi đến Google TTS API
	req := &texttospeechpb.SynthesizeSpeechRequest{
		Input: &texttospeechpb.SynthesisInput{
			InputSource: &texttospeechpb.SynthesisInput_Text{Text: text},
		},
		Voice: &texttospeechpb.VoiceSelectionParams{
			LanguageCode: languageCode,
			Name:         voice,
		},
		AudioConfig: &texttospeechpb.AudioConfig{
			AudioEncoding: texttospeechpb.AudioEncoding_LINEAR16,
			SpeakingRate:  speakingRate,
			Pitch:         pitch,
		},
	}

	resp, err := p.client.SynthesizeSpeech(ctx, req)
	if err != nil {
		return fmt.Errorf("lỗi khi gọi API SynthesizeSpeech: %v", err)
	}

	err = os.WriteFile(outputPath, resp.AudioContent, 0644)
	if err != nil {
		return fmt.Errorf("lỗi khi ghi file audio: %v", err)
	}

	fmt.Printf("Đã tổng hợp giọng nói thành công, lưu tại: %s\n", outputPath)
	return nil
}

// ToTTS là một hàm tiện ích an toàn.
func (p *GoogleTTSProvider) ToTTS(text string) (string, error) {
	outputDir, ok := p.Config().Data["output_dir"].(string)
	if !ok {
		return "", fmt.Errorf("lỗi cấu hình TTS: thiếu OutputDir")
	}

	outputPath := fmt.Sprintf("%s/output_%d.mp3", outputDir, time.Now().UnixNano())
	err := p.Synthesize(context.Background(), text, outputPath)
	if err != nil {
		return "", err
	}
	return outputPath, nil
}

// StreamSynthesis là phiên bản an toàn, không gây panic.
func (p *GoogleTTSProvider) StreamSynthesis(ctx context.Context, content string) (<-chan []byte, error) {
	cfgData := p.Config().Data

	// Lấy các giá trị config một cách an toàn
	languageCode, ok := cfgData["language_code"].(string)
	if !ok {
		return nil, fmt.Errorf("lỗi cấu hình TTS: thiếu LanguageCode")
	}

	voice, ok := cfgData["voice"].(string)
	if !ok {
		return nil, fmt.Errorf("lỗi cấu hình TTS: thiếu Voice")
	}

	speakingRate, ok := cfgData["speaking_rate"].(float64)
	if !ok {
		return nil, fmt.Errorf("lỗi cấu hình TTS: thiếu SpeakingRate")
	}

	pitch, ok := cfgData["pitch"].(float64)
	if !ok {
		return nil, fmt.Errorf("lỗi cấu hình TTS: thiếu Pitch")
	}

	// Xây dựng yêu cầu
	req := &texttospeechpb.SynthesizeSpeechRequest{
		Input: &texttospeechpb.SynthesisInput{
			InputSource: &texttospeechpb.SynthesisInput_Text{Text: content},
		},
		Voice: &texttospeechpb.VoiceSelectionParams{
			LanguageCode: languageCode,
			Name:         voice,
		},
		AudioConfig: &texttospeechpb.AudioConfig{
			AudioEncoding: texttospeechpb.AudioEncoding_LINEAR16,
			SpeakingRate:  speakingRate,
			Pitch:         pitch,
		},
	}

	resp, err := p.client.SynthesizeSpeech(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("lỗi gọi SynthesizeSpeech: %v", err)
	}

	// Bắt đầu goroutine để stream audio
	ch := make(chan []byte)
	go func() {
		defer close(ch)
		const chunkSize = 4096 // Tăng chunk size một chút
		audio := resp.AudioContent
		for i := 0; i < len(audio); i += chunkSize {
			end := i + chunkSize
			if end > len(audio) {
				end = len(audio)
			}
			ch <- audio[i:end]
		}
	}()

	return ch, nil
}

// Cleanup đóng kết nối client.
func (p *GoogleTTSProvider) Cleanup() error {
	if p.client != nil {
		return p.client.Close()
	}
	return nil
}

// Stream không được hỗ trợ cho TTS
func (p *GoogleTTSProvider) Stream(audioIn <-chan []byte) (<-chan string, error) {
	return nil, fmt.Errorf("GoogleTTS không hỗ trợ stream audio input thành text")
}

func init() {
	tts.Register("GoogleTTS", NewProvider)
}
