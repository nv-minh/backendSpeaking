import asyncio
import websockets
import sounddevice as sd
import numpy as np
import time
import queue

# --- CẤU HÌNH ---
WEBSOCKET_URI = "ws://localhost:8000/conversation"
SAMPLE_RATE = 16000 # Phải khớp với cấu hình trên server Go
CHANNELS = 1
DTYPE = 'int16'
BLOCKSIZE_MS = 100 # Gửi audio mỗi 100ms
BLOCKSIZE_SAMPLES = int(SAMPLE_RATE * (BLOCKSIZE_MS / 1000))
# -----------------

audio_queue = queue.Queue()

def audio_callback(indata, frames, time, status):
    """Callback được gọi bởi sounddevice để nhận audio từ micro."""
    if status:
        print(f"Lỗi thu âm: {status}")
    audio_queue.put(indata.copy())

async def mic_stream_sender(websocket):
    """Gửi audio từ micro đi liên tục."""
    loop = asyncio.get_running_loop()

    # Bắt đầu luồng thu âm từ micro
    stream = sd.InputStream(
        samplerate=SAMPLE_RATE,
        blocksize=BLOCKSIZE_SAMPLES,
        channels=CHANNELS,
        dtype=DTYPE,
        callback=audio_callback
    )
    stream.start()
    print(f"✅ Micro đã bắt đầu thu âm ở {SAMPLE_RATE}Hz. Hãy bắt đầu nói...")

    while True:
        try:
            # Dùng run_in_executor để lấy dữ liệu từ queue một cách an toàn
            # mà không block event loop của asyncio
            data = await loop.run_in_executor(None, audio_queue.get)
            await websocket.send(data.tobytes())
        except websockets.exceptions.ConnectionClosed:
            print("Kết nối đã đóng, dừng gửi audio.")
            break
        except Exception as e:
            print(f"Lỗi khi gửi audio: {e}")
            break

    stream.stop()
    stream.close()
    print("--- Đã dừng luồng thu âm. ---")


async def result_receiver(websocket):
    """Nhận và in kết quả phiên âm từ server."""
    print("--- Đang chờ kết quả phiên âm thời gian thực... ---")
    async for message in websocket:
        print(f"[{time.strftime('%H:%M:%S')}] SERVER: {message}")


async def main():
    """Hàm chính kết nối và quản lý các tác vụ."""
    try:
        async with websockets.connect(WEBSOCKET_URI) as websocket:
            print(f"✅ Đã kết nối thành công tới {WEBSOCKET_URI}")

            # Chạy song song tác vụ gửi và nhận
            sender_task = asyncio.create_task(mic_stream_sender(websocket))
            receiver_task = asyncio.create_task(result_receiver(websocket))

            done, pending = await asyncio.wait(
                [sender_task, receiver_task],
                return_when=asyncio.FIRST_COMPLETED,
            )

            for task in pending:
                task.cancel()

    except ConnectionRefusedError:
        print(f"LỖI: Kết nối tới {WEBSOCKET_URI} bị từ chối. Server Go có đang chạy không?")
    except Exception as e:
        print(f"Lỗi không mong muốn: {e}")

if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        print("\nĐã nhận lệnh thoát (Ctrl+C).")