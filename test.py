import asyncio
import websockets
import wave

WEBSOCKET_URI = "ws://localhost:8000/conversation"
AUDIO_FILE_PATH = "recording-2025-06-20T13-59-19.wav"

SAMPLE_RATE = 44100  # Tốc độ lấy mẫu 44.1kHz
BIT_DEPTH = 16       # Độ sâu bit (16 bit = 2 bytes)
CHANNELS = 1         # Số kênh (mono)


CHUNK_DURATION_MS = 100
BYTES_PER_SAMPLE = BIT_DEPTH // 8
CHUNK_SIZE = int(SAMPLE_RATE * (CHUNK_DURATION_MS / 1000) * BYTES_PER_SAMPLE * CHANNELS)
REAL_TIME_DELAY = CHUNK_DURATION_MS / 1000.0 # Độ trễ giữa các chunk (tính bằng giây)

# -----------------

async def send_audio(websocket, path):
    """
    Hàm đọc file WAV, bỏ qua header, và gửi các chunk audio
    với độ trễ để mô phỏng luồng thời gian thực.
    """
    print(f"Đang xử lý file audio từ: {path}")
    try:
        with wave.open(path, "rb") as audio_file:
            # Kiểm tra thông tin file audio có khớp với cấu hình không
            if (audio_file.getframerate() != SAMPLE_RATE or
                    audio_file.getsampwidth() != BYTES_PER_SAMPLE or
                    audio_file.getnchannels() != CHANNELS):
                print("LỖI: Thông số file audio không khớp với cấu hình!")
                print(f"  - File: {audio_file.getframerate()}Hz, {audio_file.getsampwidth()*8}bit, {audio_file.getnchannels()} kênh")
                print(f"  - Cấu hình: {SAMPLE_RATE}Hz, {BIT_DEPTH}bit, {CHANNELS} kênh")
                return

            print(f"Thông tin file audio hợp lệ. Bắt đầu gửi...")
            print(f"Kích thước mỗi chunk: {CHUNK_SIZE} bytes. Độ trễ: {REAL_TIME_DELAY:.3f} giây.")

            while True:
                data = audio_file.readframes(CHUNK_SIZE // BYTES_PER_SAMPLE)
                if not data:
                    break

                await websocket.send(data)

                await asyncio.sleep(REAL_TIME_DELAY)

        print("--- Gửi xong toàn bộ file audio. ---")

    except FileNotFoundError:
        print(f"LỖI: Không tìm thấy file audio tại '{path}'.")
    except wave.Error as e:
        print(f"LỖI: File '{path}' không phải là file WAV hợp lệ hoặc bị hỏng. Lỗi: {e}")
    except Exception as e:
        print(f"Lỗi khi gửi audio: {e}")

async def receive_text(websocket):
    """
    Hàm này lắng nghe liên tục các tin nhắn text trả về từ server.
    """
    print("--- Bắt đầu lắng nghe kết quả phiên âm từ server... ---")
    try:
        async for message in websocket:
            print(f">>> KẾT QUẢ NHẬN ĐƯỢC: {message}")
    except websockets.exceptions.ConnectionClosed as e:
        print(f"Kết nối đã đóng một cách sạch sẽ: {e.reason} (code={e.code})")
    except Exception as e:
        print(f"Lỗi khi nhận tin nhắn: {e}")

async def main():
    try:
        async with websockets.connect(WEBSOCKET_URI) as websocket:
            print(f"Đã kết nối thành công tới {WEBSOCKET_URI}")

            # Chạy song song tác vụ gửi audio và nhận kết quả
            receive_task = asyncio.create_task(receive_text(websocket))
            send_task = asyncio.create_task(send_audio(websocket, AUDIO_FILE_PATH))

            # Chờ cả hai tác vụ hoàn thành
            await asyncio.gather(send_task, receive_task)

    except ConnectionRefusedError:
        print(f"LỖI: Kết nối tới {WEBSOCKET_URI} bị từ chối. Server có đang chạy không?")
    except Exception as e:
        print(f"Lỗi không mong muốn trong hàm main: {e}")

if __name__ == "__main__":
    asyncio.run(main())