package core

import (
	speech "cloud.google.com/go/speech/apiv1"
	"cloud.google.com/go/speech/apiv1/speechpb"
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"google.golang.org/api/option"
	"io"
	_ "io"
	"log"
	"net/http"
	"sync"
	"time"
	"xiaozhi-server-go/src/configs"
	"xiaozhi-server-go/src/connections"
	_ "xiaozhi-server-go/src/connections" // <-- DÒNG IMPORT QUAN TRỌNG ĐÃ ĐƯỢC THÊM
	"xiaozhi-server-go/src/core/pool"
	_ "xiaozhi-server-go/src/core/providers/llm"
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

func (ws *WebSocketServer) handleConversation(w http.ResponseWriter, r *http.Request) {
	ws.logger.Info("Nhận được yêu cầu kết nối WebSocket mới đến /conversation (LIVE)")

	conn, err := ws.upgrader.Upgrade(w, r)
	if err != nil {
		ws.logger.Error("Lỗi nâng cấp kết nối WebSocket: %v", err)
		return
	}
	defer conn.Close()

	// === BƯỚC 1: TÍCH HỢP LLM - LẤY PROVIDER VÀ TẠO SESSION ID ===
	ws.logger.Info("Bắt đầu tích hợp LLM cho phiên làm việc...")

	// HÃY DÙNG: Lấy cả bộ provider từ "ông chủ" PoolManager (ws.pm)
	providerSet, err := ws.pm.GetProviderSet()
	if err != nil {
		ws.logger.Error("Không thể lấy ProviderSet từ PoolManager: %v", err)
		err := conn.WriteMessage(websocket.TextMessage, []byte("Lỗi hệ thống: Server đang quá tải."))
		if err != nil {
			return
		}
		return
	}
	// Và trả lại cả bộ khi hàm kết thúc
	defer func(pm *pool.PoolManager, set *pool.ProviderSet) {
		err := pm.ReturnProviderSet(set)
		if err != nil {

		}
	}(ws.pm, providerSet)

	// Bây giờ bạn có thể lấy llmProvider từ bộ công cụ đó
	llmProvider := providerSet.LLM
	if llmProvider == nil {
		ws.logger.Error("LLM Provider không có sẵn trong ProviderSet")
		err := conn.WriteMessage(websocket.TextMessage, []byte("Bot: Xin lỗi, dịch vụ AI hiện không khả dụng."))
		if err != nil {
			return
		}
		return
	}

	// Tạo một ID phiên duy nhất cho cuộc trò chuyện này để duy trì ngữ cảnh với Gemini
	sessionID := uuid.New().String()
	ws.logger.Info("Phiên LLM được tạo với ID: %s", sessionID)
	// =================================================================

	// Lấy cấu hình ASR (STT)
	credsFile := ws.config.ASR["GoogleSTT"]["credentials_file"].(string)
	languageCode := ws.config.ASR["GoogleSTT"]["language_code"].(string)

	ctx := context.Background()
	client, err := speech.NewClient(ctx, option.WithCredentialsFile(credsFile))
	if err != nil {
		ws.logger.Error("Lỗi tạo speech client: %v", err)
		return
	}
	defer client.Close()

	stream, err := client.StreamingRecognize(ctx)
	if err != nil {
		ws.logger.Error("Lỗi tạo streaming recognize: %v", err)
		return
	}

	// Gửi cấu hình STT, yêu cầu trả về kết quả cuối cùng (khi người dùng ngắt lời)
	if err := stream.Send(&speechpb.StreamingRecognizeRequest{
		StreamingRequest: &speechpb.StreamingRecognizeRequest_StreamingConfig{
			StreamingConfig: &speechpb.StreamingRecognitionConfig{
				Config: &speechpb.RecognitionConfig{
					Encoding:          speechpb.RecognitionConfig_LINEAR16,
					SampleRateHertz:   16000,
					LanguageCode:      languageCode,
					AudioChannelCount: 1,
					// Bạn có thể giữ lại các cấu hình tối ưu của mình
					Model:                      "telephony",
					EnableAutomaticPunctuation: true,
				},
				InterimResults:  false, // Rất quan trọng: Chỉ nhận kết quả khi người dùng nói xong
				SingleUtterance: false,
			},
		},
	}); err != nil {
		ws.logger.Error("Lỗi gửi config đến Google STT: %v", err)
		return
	}
	ws.logger.Info("Google STT: Đã gửi cấu hình. Sẵn sàng nhận audio...")

	var wg sync.WaitGroup
	wg.Add(1)

	// Goroutine nhận kết quả từ Google
	go func() {
		defer wg.Done()
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				ws.logger.Error("Lỗi nhận kết quả từ Google STT: %v", err)
				// Có thể thông báo lỗi cho client ở đây nếu cần
				// conn.WriteMessage(1, []byte("Lỗi: Mất kết nối tới dịch vụ giọng nói."))
				return
			}

			if len(resp.Results) > 0 && resp.Results[0].Alternatives != nil && len(resp.Results[0].Alternatives) > 0 {
				// Lấy bản ghi giọng nói từ STT
				transcript := resp.Results[0].Alternatives[0].Transcript
				ws.logger.Info("STT Transcript nhận được: \"%s\"", transcript)

				// Gửi lại transcript cho client để họ biết hệ thống đã nghe thấy gì (Cải thiện UX)
				userMessage := fmt.Sprintf("You: %s", transcript)
				if err := conn.WriteMessage(1, []byte(userMessage)); err != nil {
					ws.logger.Error("Lỗi gửi transcript về client: %v", err)
				}

				// === BƯỚC 2: GỌI HÀM CHAT CỦA GEMINI PROVIDER ===
				ws.logger.Info("Đang gửi transcript tới Gemini với sessionID: %s...", sessionID)

				llmResponse, err := llmProvider.Chat(ctx, sessionID, transcript)
				if err != nil {
					ws.logger.Error("Lỗi khi gọi Gemini LLM: %v", err)
					// Gửi thông báo lỗi về client
					err := conn.WriteMessage(1, []byte("Bot: Xin lỗi, tôi đang gặp sự cố khi suy nghĩ."))
					if err != nil {
						return
					}
					continue // Tiếp tục vòng lặp để nhận yêu cầu tiếp theo
				}

				ws.logger.Info("Gemini trả lời: \"%s\"", llmResponse.Content)

				// Gửi câu trả lời của LLM về client
				botMessage := fmt.Sprintf("Bot: %s", llmResponse.Content)
				if err := conn.WriteMessage(1, []byte(botMessage)); err != nil {
					ws.logger.Error("Lỗi gửi câu trả lời của LLM về client: %v", err)
				}
				// ===============================================
			}
		}
	}()

	// Vòng lặp đọc audio từ client và gửi đến Google STT
	for {
		msgType, pcmChunk, err := conn.ReadMessage()
		if err != nil {
			ws.logger.Info("Kết nối từ client đã đóng: %v", err)
			break
		}
		if msgType == 2 { // Binary Message
			if err := stream.Send(&speechpb.StreamingRecognizeRequest{
				StreamingRequest: &speechpb.StreamingRecognizeRequest_AudioContent{
					AudioContent: pcmChunk,
				},
			}); err != nil {
				ws.logger.Error("Lỗi gửi audio chunk đến Google: %v", err)
			}
		}
	}

	stream.CloseSend()
	wg.Wait()
	ws.logger.Info("Hoàn tất phiên /conversation với sessionID: %s.", sessionID)
}

// === THÊM PHƯƠNG THỨC HELPER NÀY VÀO TRONG *WebSocketServer ===

// processUtterance nhận một khối audio hoàn chỉnh và gửi đến Google để xử lý
func (ws *WebSocketServer) processUtterance(conn connections.Conn, audioData []byte) {
	log.Printf("processUtterance: Bắt đầu xử lý %d bytes.", len(audioData))

	// Lấy cấu hình và kết nối đến Google
	credsFile := ws.config.ASR["GoogleSTT"]["credentials_file"].(string)
	languageCode := ws.config.ASR["GoogleSTT"]["language_code"].(string)
	sampleRate := int32(ws.config.ASR["GoogleSTT"]["sample_rate"].(int))

	ctx := context.Background()
	client, err := speech.NewClient(ctx, option.WithCredentialsFile(credsFile))
	if err != nil {
		log.Printf("Lỗi tạo speech client: %v", err)
		return
	}
	defer client.Close()

	// Sử dụng API Recognize non-streaming vì chúng ta đã có toàn bộ audio của lượt nói
	resp, err := client.Recognize(ctx, &speechpb.RecognizeRequest{
		Config: &speechpb.RecognitionConfig{
			Encoding:                 speechpb.RecognitionConfig_LINEAR16,
			SampleRateHertz:          sampleRate,
			LanguageCode:             languageCode,
			AlternativeLanguageCodes: []string{"vi-VN"},
			Model:                    "telephony",
			AudioChannelCount:        1,
		},
		Audio: &speechpb.RecognitionAudio{
			AudioSource: &speechpb.RecognitionAudio_Content{Content: audioData},
		},
	})

	if err != nil {
		log.Printf("Lỗi Recognize từ Google: %v", err)
		return
	}

	// Xử lý kết quả và gửi về client
	if len(resp.Results) > 0 && len(resp.Results[0].Alternatives) > 0 {
		transcript := resp.Results[0].Alternatives[0].Transcript
		log.Printf("ĐÃ NHẬN KẾT QUẢ: '%s'", transcript)
		// TODO: Ở bước tiếp theo, chúng ta sẽ gửi `transcript` này đến LLM
		// Hiện tại, chúng ta gửi thẳng về client để test
		conn.WriteMessage(1, []byte(transcript))
	} else {
		log.Println("Google không trả về kết quả phiên âm nào.")
	}
}
