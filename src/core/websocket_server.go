package core

import (
	speech "cloud.google.com/go/speech/apiv1"
	"cloud.google.com/go/speech/apiv1/speechpb"
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"google.golang.org/api/option"
	"io"
	_ "io"
	"net/http"
	"sync"
	"time"
	"xiaozhi-server-go/src/configs"
	_ "xiaozhi-server-go/src/connections"
	"xiaozhi-server-go/src/core/pool"
	_ "xiaozhi-server-go/src/core/providers/llm"
	"xiaozhi-server-go/src/core/providers/tts/googletts"
	_ "xiaozhi-server-go/src/core/providers/tts/googletts"
	"xiaozhi-server-go/src/core/utils"
	"xiaozhi-server-go/src/task"
)

// WebSocketServer WebSocket服务器结构
type WebSocketServer struct {
	config            *configs.Config
	server            *http.Server
	upgrader          Upgrader
	logger            *utils.Logger
	pm                *pool.PoolManager
	taskMgr           *task.TaskManager
	poolManager       *pool.PoolManager // 替换providers
	activeConnections sync.Map          // 存储 clientID -> *ConnectionContext
}

// Upgrader WebSocket升级器接口
type Upgrader interface {
	Upgrade(w http.ResponseWriter, r *http.Request) (Connection, error)
}

// NewWebSocketServer 创建新的WebSocket服务器
func NewWebSocketServer(config *configs.Config, logger *utils.Logger, pm *pool.PoolManager) (*WebSocketServer, error) {
	ws := &WebSocketServer{
		config:   config,
		logger:   logger,
		pm:       pm,
		upgrader: NewDefaultUpgrader(),
		taskMgr: func() *task.TaskManager {
			tm := task.NewTaskManager(task.ResourceConfig{
				MaxWorkers:        12,
				MaxTasksPerClient: 20,
			})
			tm.Start()
			return tm
		}(),
	}
	// 初始化资源池管理器
	poolManager, err := pool.NewPoolManager(config, logger)
	if err != nil {
		logger.Error(fmt.Sprintf("初始化资源池管理器失败: %v", err))
		return nil, fmt.Errorf("初始化资源池管理器失败: %v", err)
	}
	ws.poolManager = poolManager
	return ws, nil
}

// Start 启动WebSocket服务器
func (ws *WebSocketServer) Start(ctx context.Context) error {
	// 检查资源池是否正常
	if ws.poolManager == nil {
		ws.logger.Error("资源池管理器未初始化")
		return fmt.Errorf("资源池管理器未初始化")
	}

	addr := fmt.Sprintf("%s:%d", ws.config.Server.IP, ws.config.Server.Port)

	mux := http.NewServeMux()
	mux.HandleFunc("/", ws.handleWebSocket)
	mux.HandleFunc("/conversation", ws.handleConversation)

	ws.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	ws.logger.Info(fmt.Sprintf("启动WebSocket服务器 ws://%s...", addr))

	// 启动服务器
	if err := ws.server.ListenAndServe(); err != nil {
		if err == http.ErrServerClosed {
			ws.logger.Info("服务器已正常关闭")
			return nil
		}
		ws.logger.Error(fmt.Sprintf("服务器启动失败: %v", err))
		return fmt.Errorf("服务器启动失败: %v", err)
	}

	return nil
}

// defaultUpgrader 默认的WebSocket升级器实现
type defaultUpgrader struct {
	wsUpgrader *websocket.Upgrader
}

// NewDefaultUpgrader 创建默认的WebSocket升级器
func NewDefaultUpgrader() *defaultUpgrader {
	return &defaultUpgrader{
		wsUpgrader: &websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // 允许所有来源的连接
			},
		},
	}
}

// Upgrade 实现Upgrader接口
func (u *defaultUpgrader) Upgrade(w http.ResponseWriter, r *http.Request) (Connection, error) {
	conn, err := u.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	wsConn := &websocketConn{
		conn:       conn,
		closed:     0,
		lastActive: now,
	}

	return wsConn, nil
}

// Stop 停止WebSocket服务器
func (ws *WebSocketServer) Stop() error {
	if ws.server != nil {
		ws.logger.Info("正在关闭WebSocket服务器...")

		// 关闭所有活动连接并归还资源
		ws.activeConnections.Range(func(key, value interface{}) bool {
			if ctx, ok := value.(*ConnectionContext); ok {
				if err := ctx.Close(); err != nil {
					ws.logger.Error(fmt.Sprintf("关闭连接上下文失败: %v", err))
				}
			} else if conn, ok := value.(Connection); ok {
				// 向后兼容：直接关闭连接（如果存储的是旧格式）
				conn.Close()
			}
			ws.activeConnections.Delete(key)
			return true
		})

		// 关闭资源池
		if ws.poolManager != nil {
			ws.poolManager.Close()
		}

		// 关闭服务器
		if err := ws.server.Close(); err != nil {
			return fmt.Errorf("服务器关闭失败: %v", err)
		}
	}
	return nil
}

// handleWebSocket 处理WebSocket连接
func (ws *WebSocketServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := ws.upgrader.Upgrade(w, r)
	if err != nil {
		ws.logger.Error(fmt.Sprintf("WebSocket升级失败: %v", err))
		return
	}

	clientID := fmt.Sprintf("%p", conn)

	// 从资源池获取提供者集合
	providerSet, err := ws.poolManager.GetProviderSet()
	if err != nil {
		ws.logger.Error(fmt.Sprintf("获取提供者集合失败: %v", err))
		conn.Close()
		return
	}

	connCtx, connCancel := context.WithCancel(context.Background())
	// 创建新的连接处理器
	handler := NewConnectionHandler(ws.config, providerSet, ws.logger, r, connCtx)

	connContext := NewConnectionContext(handler, providerSet, ws.poolManager, clientID, ws.logger, conn, connCtx, connCancel)

	// 设置TaskManager的回调（使用安全回调）
	handler.taskMgr = ws.taskMgr
	handler.SetTaskCallback(connContext.CreateSafeCallback())

	// 存储连接上下文
	ws.activeConnections.Store(clientID, connContext)

	ws.logger.Info(fmt.Sprintf("客户端 %s 连接已建立，资源已分配", clientID))
	ws.logger.Info(fmt.Sprintf("data request %s", r))

	// 启动连接处理，并在结束时清理资源
	go func() {
		defer func() {
			// 连接结束时清理
			ws.activeConnections.Delete(clientID)
			if err := connContext.Close(); err != nil {
				ws.logger.Error(fmt.Sprintf("清理连接上下文失败: %v", err))
			}
		}()

		handler.Handle(conn)
	}()
}

// GetPoolStats 获取资源池统计信息（用于监控）
func (ws *WebSocketServer) GetPoolStats() map[string]map[string]int {
	if ws.poolManager == nil {
		return nil
	}
	return ws.poolManager.GetDetailedStats()
}

// GetActiveConnectionsCount 获取活跃连接数
func (ws *WebSocketServer) GetActiveConnectionsCount() int {
	count := 0
	ws.activeConnections.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}

type WsMessage struct {
	Type    string `json:"type"`     // "transcript", "bot_response", "error"
	IsFinal bool   `json:"is_final"` // Dành cho transcript
	Text    string `json:"text"`
}

const (
	// Thời gian chờ để ghi một tin nhắn đến client.
	writeWait = 10 * time.Second

	// Thời gian chờ để đọc tin nhắn pong tiếp theo từ client. Phải lớn hơn pingPeriod.
	pongWait = 60 * time.Second

	// Tần suất gửi ping đến client. Phải nhỏ hơn pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Kích thước tối đa cho phép của tin nhắn từ client.
	maxMessageSize = 16384 // Tăng kích thước để chứa các chunk audio lớn hơn một chút
)

func (ws *WebSocketServer) handleConversation(w http.ResponseWriter, r *http.Request) {
	ws.logger.Info("Nhận được yêu cầu kết nối WebSocket mới đến /conversation (LIVE)")

	conn, err := ws.upgrader.Upgrade(w, r)
	if err != nil {
		ws.logger.Error("Lỗi nâng cấp kết nối WebSocket: %v", err)
		return
	}
	defer conn.Close()

	// --- Thiết lập cơ chế Heartbeat (Ping/Pong) ---
	conn.SetReadLimit(maxMessageSize)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		ws.logger.Info("PONG received, resetting read deadline.")
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	sessionID := uuid.New().String()
	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ws.logger.Info("[%s] --- Sending PING ---", sessionID)
				conn.SetWriteDeadline(time.Now().Add(writeWait))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					ws.logger.Info("[%s] Lỗi gửi ping, kết thúc goroutine ping: %v", sessionID, err)
					return
				}
			case <-r.Context().Done():
				ws.logger.Info("[%s] Context của request bị hủy, dừng goroutine ping.", sessionID)
				return
			}
		}
	}()

	sendJSON := func(msgType string, text string, isFinal bool) {
		message, _ := json.Marshal(WsMessage{Type: msgType, Text: text, IsFinal: isFinal})
		conn.SetWriteDeadline(time.Now().Add(writeWait))
		if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
			ws.logger.Warn("[%s] Lỗi gửi tin nhắn JSON đến client: %v", sessionID, err)
		}
	}

	// === BƯỚC 1: LẤY BỘ PROVIDER VÀ CONFIG ===
	providerSet, err := ws.pm.GetProviderSet()
	if err != nil {
		ws.logger.Error("Không thể lấy ProviderSet từ PoolManager: %v", err)
		sendJSON("error", "Lỗi hệ thống: Server đang quá tải.", true)
		return
	}
	defer ws.pm.ReturnProviderSet(providerSet)

	llmProvider := providerSet.LLM
	ttsProvider := providerSet.TTS
	credsFile := ws.config.ASR["GoogleSTT"]["credentials_file"].(string)
	languageCode := ws.config.ASR["GoogleSTT"]["language_code"].(string)
	ws.logger.Info("Bắt đầu phiên hội thoại với ID: %s", sessionID)

	// <--- Biến để lưu trữ keywords cho lượt nói tiếp theo
	var nextKeywords []string

	// ===================================================================
	// === VÒNG LẶP HỘI THOẠI CHÍNH (Tích hợp Speech Adaptation Động) ===
	// ===================================================================
	for {
		ws.logger.Info("[%s] Lượt mới: Đang chờ audio đầu tiên từ client...", sessionID)

		var firstChunk []byte
		var msgType int

		msgType, firstChunk, err = conn.ReadMessage()
		if err != nil {
			ws.logger.Info("[%s] Client đóng kết nối hoặc lỗi đọc khi chờ audio đầu tiên: %v", sessionID, err)
			return
		}

		if msgType != websocket.BinaryMessage {
			continue
		}

		ctx, cancelConversationTurn := context.WithCancel(r.Context())

		// Tạo SpeechAdaptation DỰA TRÊN KEYWORDS TỪ LƯỢT TRƯỚC
		var adaptation *speechpb.SpeechAdaptation
		if len(nextKeywords) > 0 {
			ws.logger.Info("[%s] Áp dụng speech adaptation với keywords: %v", sessionID, nextKeywords)
			phrases := make([]*speechpb.PhraseSet_Phrase, len(nextKeywords))
			for i, kw := range nextKeywords {
				phrases[i] = &speechpb.PhraseSet_Phrase{Value: kw, Boost: 15}
			}
			adaptation = &speechpb.SpeechAdaptation{
				PhraseSets: []*speechpb.PhraseSet{{Phrases: phrases}},
			}
		}

		speechClient, err := speech.NewClient(ctx, option.WithCredentialsFile(credsFile))
		if err != nil {
			ws.logger.Error("[%s] Lỗi tạo speech client: %v", sessionID, err)
			sendJSON("error", "Lỗi khởi tạo STT.", true)
			cancelConversationTurn()
			continue
		}

		sttStream, err := speechClient.StreamingRecognize(ctx)
		if err != nil {
			ws.logger.Error("[%s] Lỗi tạo streaming recognize: %v", sessionID, err)
			sendJSON("error", "Lỗi khởi tạo STT stream.", true)
			speechClient.Close()
			cancelConversationTurn()
			continue
		}

		if err := sttStream.Send(&speechpb.StreamingRecognizeRequest{
			StreamingRequest: &speechpb.StreamingRecognizeRequest_StreamingConfig{
				StreamingConfig: &speechpb.StreamingRecognitionConfig{
					Config: &speechpb.RecognitionConfig{
						Encoding:                   speechpb.RecognitionConfig_LINEAR16,
						SampleRateHertz:            16000,
						LanguageCode:               languageCode,
						EnableAutomaticPunctuation: true,
						Model:                      "latest_short",
						Adaptation:                 adaptation, // <--- THÊM VÀO ĐÂY
					},
					InterimResults:  true,
					SingleUtterance: true,
				},
			},
		}); err != nil {
			ws.logger.Error("[%s] Lỗi gửi config đến Google STT: %v", sessionID, err)
			sendJSON("error", "Lỗi cấu hình STT.", true)
			sttStream.CloseSend()
			speechClient.Close()
			cancelConversationTurn()
			continue
		}

		ws.logger.Info("[%s] Đã nhận audio đầu tiên. Bắt đầu stream tới Google STT...", sessionID)

		if err := sttStream.Send(&speechpb.StreamingRecognizeRequest{
			StreamingRequest: &speechpb.StreamingRecognizeRequest_AudioContent{AudioContent: firstChunk},
		}); err != nil {
			ws.logger.Error("[%s] Lỗi gửi audio chunk đầu tiên đến Google STT: %v", sessionID, err)
			sttStream.CloseSend()
			speechClient.Close()
			cancelConversationTurn()
			continue
		}

		finalTranscriptChan := make(chan string, 1)
		sttErrChan := make(chan error, 1)
		var sttWg sync.WaitGroup
		sttWg.Add(1)

		go func() {
			defer sttWg.Done()
			for {
				resp, err := sttStream.Recv()
				if err != nil {
					if err != io.EOF {
						sttErrChan <- fmt.Errorf("lỗi nhận từ Google STT: %w", err)
					}
					return
				}
				if len(resp.Results) > 0 && len(resp.Results[0].Alternatives) > 0 {
					result := resp.Results[0]
					transcript := result.Alternatives[0].Transcript
					if result.IsFinal {
						ws.logger.Info("[%s] FINAL Transcript: \"%s\"", sessionID, transcript)
						sendJSON("transcript", transcript, true)
						finalTranscriptChan <- transcript
						return
					} else {
						ws.logger.Info("[%s] INTERIM Transcript: \"%s\"", sessionID, transcript)
						sendJSON("transcript", transcript, false)
					}
				}
			}
		}()

		var finalTranscript string
		var receivedFinalTranscript bool

	listeningLoop:
		for {
			select {
			case transcript := <-finalTranscriptChan:
				finalTranscript = transcript
				receivedFinalTranscript = true
				break listeningLoop
			case err := <-sttErrChan:
				ws.logger.Error("[%s] Lỗi trong goroutine của STT: %v", sessionID, err)
				receivedFinalTranscript = false
				break listeningLoop
			default:
				msgType, pcmChunk, err := conn.ReadMessage()
				if err != nil {
					ws.logger.Info("[%s] Client đóng kết nối hoặc lỗi đọc: %v", sessionID, err)
					cancelConversationTurn()
					sttStream.CloseSend()
					speechClient.Close()
					return
				}
				if msgType == websocket.BinaryMessage {
					if err := sttStream.Send(&speechpb.StreamingRecognizeRequest{
						StreamingRequest: &speechpb.StreamingRecognizeRequest_AudioContent{AudioContent: pcmChunk},
					}); err != nil {
						ws.logger.Error("[%s] Lỗi gửi audio chunk tiếp theo đến Google STT: %v", sessionID, err)
						break listeningLoop
					}
				}
			}
		}

		sttStream.CloseSend()
		sttWg.Wait()
		speechClient.Close()

		if receivedFinalTranscript && finalTranscript != "" {
			ws.logger.Info("[%s] Đã nhận transcript. Đang gọi LLM...", sessionID)
			llmResp, err := llmProvider.Chat(ctx, sessionID, finalTranscript)
			if err != nil {
				ws.logger.Error("[%s] Lỗi gọi Gemini LLM: %v", sessionID, err)
				sendJSON("error", "Bot: Xin lỗi, tôi đang gặp sự cố khi suy nghĩ.", true)
				nextKeywords = nil
			} else {
				botReply := llmResp.Reply
				ws.logger.Info("[%s] Gemini trả lời: \"%s\"", sessionID, botReply)
				sendJSON("bot_response", botReply, true)

				// LƯU KEYWORDS CHO LƯỢT TIẾP THEO
				nextKeywords = llmResp.Keywords

				ws.logger.Info("[%s] Đang gọi TTS và streaming audio...", sessionID)
				audioStream, err := ttsProvider.(*googletts.GoogleTTSProvider).StreamSynthesis(ctx, llmResp.Reply)
				if err != nil {
					ws.logger.Error("[%s] Lỗi TTS: %v", sessionID, err)
					sendJSON("error", "Bot: Tôi không thể nói câu trả lời.", true)
				} else {
					for chunk := range audioStream {
						conn.SetWriteDeadline(time.Now().Add(writeWait))
						if err := conn.WriteMessage(websocket.BinaryMessage, chunk); err != nil {
							ws.logger.Error("[%s] Lỗi gửi chunk âm thanh TTS (client có thể đã đóng): %v", sessionID, err)
							cancelConversationTurn()
							return
						}
					}
					ws.logger.Info("[%s] Streaming TTS hoàn tất.", sessionID)
				}
			}
		} else {
			ws.logger.Info("[%s] Không nhận được transcript cuối cùng, bắt đầu lượt mới.", sessionID)
			nextKeywords = nil
		}

		cancelConversationTurn()
	}
}
