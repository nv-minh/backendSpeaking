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

func (ws *WebSocketServer) handleConversation(w http.ResponseWriter, r *http.Request) {
	ws.logger.Info("Nhận được yêu cầu kết nối WebSocket mới đến /conversation (LIVE)")

	conn, err := ws.upgrader.Upgrade(w, r)
	if err != nil {
		ws.logger.Error("Lỗi nâng cấp kết nối WebSocket: %v", err)
		return
	}
	defer conn.Close()

	// Helper function để gửi tin nhắn JSON
	sendJSON := func(msgType string, text string, isFinal bool) {
		message, _ := json.Marshal(WsMessage{Type: msgType, Text: text, IsFinal: isFinal})
		conn.WriteMessage(websocket.TextMessage, message)
	}

	// === BƯỚC 1: LẤY BỘ PROVIDER VÀ TẠO SESSION ID (Không đổi) ===
	providerSet, err := ws.pm.GetProviderSet()
	if err != nil {
		ws.logger.Error("Không thể lấy ProviderSet từ PoolManager: %v", err)
		sendJSON("error", "Lỗi hệ thống: Server đang quá tải.", true)
		return
	}
	defer ws.pm.ReturnProviderSet(providerSet)

	// (Phần lấy llmProvider, ttsProvider và sessionID)
	llmProvider := providerSet.LLM
	if llmProvider == nil {
		ws.logger.Error("LLM Provider không có sẵn")
		sendJSON("error", "Bot: Xin lỗi, dịch vụ AI hiện không khả dụng.", true)
		return
	}
	ttsProvider := providerSet.TTS
	if ttsProvider == nil {
		ws.logger.Error("TTS Provider không có sẵn")
		sendJSON("error", "Bot: Không thể nói được, thiếu dịch vụ TTS.", true)
		return
	}
	sessionID := uuid.New().String()
	ws.logger.Info("Phiên LLM được tạo với ID: %s", sessionID)

	// === BƯỚC 2: KHỞI TẠO GOOGLE STT (VỚI INTERIM RESULTS) ===
	credsFile := ws.config.ASR["GoogleSTT"]["credentials_file"].(string)
	languageCode := ws.config.ASR["GoogleSTT"]["language_code"].(string)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	speechClient, err := speech.NewClient(ctx, option.WithCredentialsFile(credsFile))
	if err != nil {
		ws.logger.Error("Lỗi tạo speech client: %v", err)
		return
	}
	defer speechClient.Close()

	stream, err := speechClient.StreamingRecognize(ctx)
	if err != nil {
		ws.logger.Error("Lỗi tạo streaming recognize: %v", err)
		return
	}

	if err := stream.Send(&speechpb.StreamingRecognizeRequest{
		StreamingRequest: &speechpb.StreamingRecognizeRequest_StreamingConfig{
			StreamingConfig: &speechpb.StreamingRecognitionConfig{
				Config: &speechpb.RecognitionConfig{
					Encoding:                   speechpb.RecognitionConfig_LINEAR16,
					SampleRateHertz:            16000,
					LanguageCode:               languageCode,
					AudioChannelCount:          1,
					AlternativeLanguageCodes:   []string{"vi-VN"},
					Model:                      "command_and_search",
					EnableAutomaticPunctuation: true,
				},
				InterimResults:  true,
				SingleUtterance: true,
			},
		},
	}); err != nil {
		ws.logger.Error("Lỗi gửi config đến Google STT: %v", err)
		return
	}
	ws.logger.Info("Google STT: Đã sẵn sàng nhận âm thanh...")

	var wg sync.WaitGroup
	wg.Add(1)

	// === BƯỚC 3: GOROUTINE NHẬN KẾT QUẢ STT (LOGIC MỚI) ===
	go func() {
		defer wg.Done()
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				// Nếu client đóng kết nối, context sẽ bị cancel, gây ra lỗi ở đây.
				// Chúng ta chỉ cần log và thoát khỏi goroutine.
				ws.logger.Info("Lỗi nhận từ Google STT (có thể do client đã đóng kết nối): %v", err)
				return
			}

			if len(resp.Results) > 0 && len(resp.Results[0].Alternatives) > 0 {
				result := resp.Results[0]
				transcript := result.Alternatives[0].Transcript

				if result.IsFinal {
					ws.logger.Info("FINAL Transcript: \"%s\"", transcript)
					sendJSON("transcript", transcript, true) // Gửi kết quả cuối cùng

					// === BƯỚC 4: GỌI LLM (CHỈ KHI CÓ KẾT QUẢ CUỐI CÙNG) ===
					llmResp, err := llmProvider.Chat(ctx, sessionID, transcript)
					if err != nil {
						ws.logger.Error("Lỗi gọi Gemini LLM: %v", err)
						sendJSON("error", "Bot: Xin lỗi, tôi đang gặp sự cố khi suy nghĩ.", true)
						continue
					}
					ws.logger.Info("Gemini trả lời: \"%s\"", llmResp.Content)
					sendJSON("bot_response", llmResp.Content, true) // Gửi text của bot

					// === BƯỚC 5: TTS - STREAM ÂM THANH NGƯỢC VỀ CLIENT ===
					audioStream, err := ttsProvider.(*googletts.GoogleTTSProvider).StreamSynthesis(ctx, llmResp.Content)
					if err != nil {
						ws.logger.Error("Lỗi TTS: %v", err)
						sendJSON("error", "Bot: Tôi không thể nói câu trả lời.", true)
						continue
					}
					for chunk := range audioStream {
						if err := conn.WriteMessage(websocket.BinaryMessage, chunk); err != nil {
							ws.logger.Error("Lỗi gửi chunk âm thanh: %v", err)
							break
						}
					}
				} else {
					ws.logger.Info("INTERIM Transcript: \"%s\"", transcript)
					sendJSON("transcript", transcript, false)
				}
			}
		}
	}()

	// === BƯỚC 6: NHẬN AUDIO TỪ CLIENT
	for {
		msgType, pcmChunk, err := conn.ReadMessage()
		if err != nil {
			ws.logger.Info("Client đóng kết nối: %v", err)
			break
		}
		if msgType == websocket.BinaryMessage {
			if err := stream.Send(&speechpb.StreamingRecognizeRequest{
				StreamingRequest: &speechpb.StreamingRecognizeRequest_AudioContent{AudioContent: pcmChunk},
			}); err != nil {
				ws.logger.Error("Lỗi gửi audio chunk đến Google STT: %v", err)
				break
			}
		}
	}

	stream.CloseSend()
	wg.Wait()
	ws.logger.Info("Kết thúc phiên /conversation với sessionID: %s", sessionID)
}
