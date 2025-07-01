package googlestt

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"xiaozhi-server-go/src/core/providers"

	speech "cloud.google.com/go/speech/apiv1"
	"cloud.google.com/go/speech/apiv1/speechpb"
	"google.golang.org/api/option"
	"xiaozhi-server-go/src/core/providers/asr"
	"xiaozhi-server-go/src/core/utils"
)

func init() {
	asr.Register("GoogleSTT", NewProvider)
}

// GoogleSTT struct bây giờ sẽ kế thừa BaseProvider
type GoogleSTT struct {
	*asr.BaseProvider
	credsFile    string
	languageCode string
	sampleRate   int32
	encoding     speechpb.RecognitionConfig_AudioEncoding
}

// NewProvider là hàm tạo có chữ ký khớp với yêu cầu của asr.Factory.
func NewProvider(config *asr.Config, deleteFile bool, logger *utils.Logger) (asr.Provider, error) {
	base := asr.NewBaseProvider(config, deleteFile)
	credsFile, _ := config.Data["credentials_file"].(string)
	if credsFile == "" {
		return nil, fmt.Errorf("google STT provider yêu cầu 'credentials_file'")
	}
	languageCode, _ := config.Data["language_code"].(string)
	if languageCode == "" {
		languageCode = "en-US"
	}
	sampleRate, _ := config.Data["sample_rate"].(float64)
	if sampleRate == 0 {
		sampleRate = 16000
	}
	encodingStr, _ := config.Data["encoding"].(string)
	encodingVal, ok := speechpb.RecognitionConfig_AudioEncoding_value[encodingStr]
	if !ok {
		encodingVal = int32(speechpb.RecognitionConfig_LINEAR16)
	}
	return &GoogleSTT{
		BaseProvider: base,
		credsFile:    credsFile,
		languageCode: languageCode,
		sampleRate:   int32(sampleRate),
		encoding:     speechpb.RecognitionConfig_AudioEncoding(encodingVal),
	}, nil
}

func (s *GoogleSTT) Stream(audioIn <-chan []byte) (<-chan string, error) {
	textOut := make(chan string)

	// Khởi chạy một goroutine duy nhất để quản lý toàn bộ vòng đời của stream
	go func() {
		// Đảm bảo channel textOut luôn được đóng khi goroutine kết thúc
		defer close(textOut)

		ctx := context.Background()
		client, err := speech.NewClient(ctx, option.WithCredentialsFile(s.credsFile))
		if err != nil {
			log.Printf("Lỗi goroutine: không tạo được speech client: %v", err)
			return
		}
		defer client.Close()

		stream, err := client.StreamingRecognize(ctx)
		if err != nil {
			log.Printf("Lỗi goroutine: không tạo được streaming recognize: %v", err)
			return
		}

		// Gói tin cấu hình ban đầu
		if err := stream.Send(&speechpb.StreamingRecognizeRequest{
			StreamingRequest: &speechpb.StreamingRecognizeRequest_StreamingConfig{
				StreamingConfig: &speechpb.StreamingRecognitionConfig{
					Config: &speechpb.RecognitionConfig{
						Encoding:        s.encoding,
						SampleRateHertz: s.sampleRate,
						LanguageCode:    s.languageCode,
					},
					SingleUtterance: true,
				},
			},
		}); err != nil {
			log.Printf("Lỗi goroutine: không gửi được config: %v", err)
			return
		}
		log.Println("Google STT: Đã gửi cấu hình ban đầu thành công.")

		var wg sync.WaitGroup
		wg.Add(1)

		// Goroutine con để nhận kết quả
		go func() {
			defer wg.Done()
			for {
				resp, err := stream.Recv()
				if err == io.EOF {
					return
				}
				if err != nil {
					log.Printf("Google STT: Lỗi nhận phản hồi: %v", err)
					return
				}
				if len(resp.Results) > 0 && resp.Results[0].IsFinal {
					if len(resp.Results[0].Alternatives) > 0 {
						transcript := resp.Results[0].Alternatives[0].Transcript
						log.Printf("Google STT --> Server: Nhận được transcript: '%s'", transcript)
						textOut <- transcript
					}
				}
			}
		}()

		// Vòng lặp chính để gửi audio
		for chunk := range audioIn {
			if err := stream.Send(&speechpb.StreamingRecognizeRequest{
				StreamingRequest: &speechpb.StreamingRecognizeRequest_AudioContent{
					AudioContent: chunk,
				},
			}); err != nil {
				log.Printf("Google STT: Lỗi gửi audio chunk: %v", err)
			}
		}

		// Sau khi gửi hết audio, đóng luồng gửi
		if err := stream.CloseSend(); err != nil {
			log.Printf("Google STT: Lỗi khi đóng luồng gửi: %v", err)
		}

		// Đợi goroutine nhận xử lý xong
		wg.Wait()
		log.Println("Google STT: Hoàn tất phiên streaming.")
	}()

	return textOut, nil
}
func (s *GoogleSTT) AddAudio(audio []byte) error {
	return fmt.Errorf("GoogleSTT provider không hỗ trợ phương thức AddAudio, vui lòng sử dụng Stream")
}

func (s *GoogleSTT) Reset() error {
	log.Println("GoogleSTT provider: Reset() được gọi. Vì provider này hoạt động ở chế độ streaming, hàm Reset không cần thực hiện hành động gì.")
	return nil
}

// Transcribe HÃY CHẮC CHẮN PHƯƠNG THỨC NÀY TỒN TẠI VÀ ĐÚNG CHỮ KÝ
func (s *GoogleSTT) Transcribe(ctx context.Context, audioData []byte) (string, error) {
	log.Println("GoogleSTT provider: Transcribe() được gọi.")
	return "", fmt.Errorf("GoogleSTT provider không hỗ trợ phương thức Transcribe, vui lòng sử dụng Stream")
}

func (s *GoogleSTT) SetListener(listener providers.AsrEventListener) {
	s.BaseProvider.SetListener(listener)
}

func init() {
	asr.Register("googlestt", NewProvider)

	//asr.Register("GoogleSTT", NewProvider)
}
