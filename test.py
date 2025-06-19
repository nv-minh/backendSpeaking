import asyncio
import websockets
import pathlib

# --- CẤU HÌNH ---
# URL của WebSocket server, đảm bảo có endpoint /conversation và tham số test=asr
WEBSOCKET_URI = "ws://localhost:8000/conversation"
# Tên file audio bạn muốn gửi
AUDIO_FILE_PATH = "brooklyn_bridge_mono.wav"
# Kích thước mỗi chunk audio gửi đi (tính bằng byte), giả lập streaming
CHUNK_SIZE = 8192
# -----------------

async def send_audio(websocket, path):
    """
    Hàm này đọc file audio và gửi đi từng chunk qua WebSocket.
    """
    print(f"Đang đọc file audio từ: {path}")
    try:
        with open(path, "rb") as audio_file:
            while True:
                # Đọc một chunk dữ liệu từ file
                data = audio_file.read(CHUNK_SIZE)
                if not data:
                    break  # Hết file

                # Gửi chunk dữ liệu dưới dạng binary message
                await websocket.send(data)
                print(f"Đã gửi chunk audio ({len(data)} bytes)...")

                # Chờ một chút để giả lập streaming thời gian thực
                # await asyncio.sleep(0.01)

        print("--- Gửi xong toàn bộ file audio. ---")

    except FileNotFoundError:
        print(f"LỖI: Không tìm thấy file audio tại '{path}'.")
        print("Vui lòng đặt file 'test_audio.wav' cùng thư mục với script.")
    except Exception as e:
        print(f"Lỗi khi gửi audio: {e}")

async def receive_text(websocket):
    """
    Hàm này lắng nghe liên tục các tin nhắn text trả về từ server.
    """
    print("--- Bắt đầu lắng nghe kết quả phiên âm từ server... ---")
    try:
        async for message in websocket:
            # In ra kết quả nhận được
            print(f">>> KẾT QUẢ NHẬN ĐƯỢC: {message}")
    except websockets.exceptions.ConnectionClosed as e:
        print(f"Kết nối đã đóng: {e}")
    except Exception as e:
        print(f"Lỗi khi nhận tin nhắn: {e}")

async def main():
    """
    Hàm chính, kết nối và chạy đồng thời việc gửi audio và nhận text.
    """
    # Sử dụng 'async with' để đảm bảo kết nối được đóng đúng cách
    async with websockets.connect(WEBSOCKET_URI) as websocket:
        print(f"Đã kết nối thành công tới {WEBSOCKET_URI}")

        # Chạy đồng thời 2 tác vụ: gửi audio và nhận text
        # receive_task sẽ chạy mãi cho đến khi kết nối đóng
        # send_task sẽ chạy cho đến khi gửi xong file
        receive_task = asyncio.create_task(receive_text(websocket))
        send_task = asyncio.create_task(send_audio(websocket, AUDIO_FILE_PATH))

        # Đợi cho cả hai tác vụ hoàn thành
        await asyncio.gather(send_task, receive_task)

if __name__ == "__main__":
    # Chạy vòng lặp sự kiện bất đồng bộ
    asyncio.run(main())