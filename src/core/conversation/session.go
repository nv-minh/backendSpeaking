package conversation

import (
	"log"
	"net/http"
	"sync"
	"xiaozhi-server-go/src/connections"
	"xiaozhi-server-go/src/core/providers/asr"
	"xiaozhi-server-go/src/core/providers/llm"
	"xiaozhi-server-go/src/core/providers/tts"
)

type Session struct {
	conn        connections.Conn
	testMode    string
	asrProvider asr.Provider
	llmProvider llm.Provider
	ttsProvider tts.Provider
	isClosed    bool
	mutex       sync.Mutex
}

func NewSession(conn connections.Conn, r *http.Request, asrProv asr.Provider, llmProv llm.Provider, ttsProv tts.Provider) *Session {
	testMode := r.URL.Query().Get("test")
	log.Printf("Tạo session mới với testMode = '%s'\n", testMode)
	return &Session{
		conn:        conn,
		testMode:    testMode,
		asrProvider: asrProv,
		llmProvider: llmProv,
		ttsProvider: ttsProv,
	}
}

func (s *Session) Start() {
	log.Println("Bắt đầu phiên hội thoại streaming...")
	defer s.Close()
	audioFromClient := make(chan []byte, 100)
	go s.readLoop(audioFromClient)

	if s.testMode == "asr" {
		log.Println("Chạy ở chế độ TEST ASR.")
		textFromASR, err := s.asrProvider.Stream(audioFromClient)
		if err != nil {
			log.Printf("Lỗi khởi tạo ASR stream: %v", err)
			return
		}
		s.writeTextLoop(textFromASR)
	} else {
		log.Println("Chạy ở chế độ hội thoại đầy đủ (chưa triển khai).")
	}
}

func (s *Session) readLoop(audioChan chan<- []byte) {
	defer close(audioChan)
	const websocketBinaryMessage = 2

	var fullAudioData []byte // Buffer để gom audio

	for {
		messageType, p, err := s.conn.ReadMessage()
		if err != nil {
			// Sau khi vòng lặp kết thúc, in ra thông tin tổng hợp
			if len(fullAudioData) > 0 {
				log.Printf("--- GO (SESSION): Kết nối đóng. Tổng cộng đã nhận: %d bytes.", len(fullAudioData))
				log.Printf("--- GO (SESSION): 16 bytes đầu tiên đã nhận (hex): %x", fullAudioData[:16])
			} else {
				log.Println("--- GO (SESSION): Kết nối đóng. Không nhận được byte nào.")
			}
			break
		}

		if messageType == websocketBinaryMessage {
			if len(fullAudioData) == 0 { // Chỉ log chunk đầu tiên
				log.Printf("Session ReadLoop: Nhận được chunk đầu tiên, size: %d bytes", len(p))
			}
			fullAudioData = append(fullAudioData, p...) // Gom dữ liệu
			audioChan <- p
		}
	}
}
func (s *Session) writeTextLoop(textChan <-chan string) {
	const websocketTextMessage = 1
	for text := range textChan {
		s.mutex.Lock()
		if s.isClosed {
			s.mutex.Unlock()
			return
		}
		err := s.conn.WriteMessage(websocketTextMessage, []byte(text))
		s.mutex.Unlock()
		if err != nil {
			return
		}
	}
}

func (s *Session) Close() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if !s.isClosed {
		log.Println("Đóng phiên hội thoại.")
		s.isClosed = true
		s.conn.Close()
	}
}
