import asyncio
import json
import websockets
import sounddevice as sd
import numpy as np
import queue

WEBSOCKET_URI = "ws://localhost:8000/conversation"
MIC_SAMPLE_RATE = 16000
MIC_CHANNELS = 1
MIC_DTYPE = 'int16'
MIC_BLOCKSIZE_MS = 100
SPEAKER_SAMPLE_RATE = 24000
SPEAKER_CHANNELS = 1
SPEAKER_DTYPE = 'int16'
BOT_END_OF_SPEECH_DELAY = 0.8
MIC_BLOCKSIZE_SAMPLES = int(MIC_SAMPLE_RATE * (MIC_BLOCKSIZE_MS / 1000))
audio_queue = queue.Queue()


class AppState:
    """Một lớp đơn giản để quản lý trạng thái chung của ứng dụng."""
    def __init__(self, loop):
        self.loop = loop
        # Event này hoạt động như một "công tắc"
        # Nếu được set() -> Bot đang nói, micro im lặng
        # Nếu được clear() -> Bot im lặng, micro có thể gửi
        self.is_bot_speaking_event = asyncio.Event()
        self.end_of_speech_timer = None
        self.is_bot_speaking_event.clear()

    def bot_is_speaking(self):
        """Được gọi khi bot bắt đầu phát audio."""
        # Nếu có một bộ đếm thời gian đang chạy, hãy hủy nó
        if self.end_of_speech_timer:
            self.end_of_speech_timer.cancel()

        # Đặt trạng thái -> Vô hiệu hóa micro
        self.is_bot_speaking_event.set()

        # Lên lịch để tự động chuyển về trạng thái im lặng sau một khoảng trễ
        self.end_of_speech_timer = self.loop.call_later(
            BOT_END_OF_SPEECH_DELAY,
            self.bot_has_stopped_speaking
        )

    def bot_has_stopped_speaking(self):
        # Xóa trạng thái -> Kích hoạt lại micro
        self.is_bot_speaking_event.clear()
        print("\n✅ Bot đã nói xong. Bạn có thể nói tiếp.")


def audio_callback(indata, frames, time, status):
    if status:
        print(f"Lỗi thu âm: {status}")
    audio_queue.put(indata.copy())


async def mic_stream_sender(websocket, app_state: AppState):
    loop = asyncio.get_running_loop()

    stream = sd.InputStream(
        samplerate=MIC_SAMPLE_RATE,
        blocksize=MIC_BLOCKSIZE_SAMPLES,
        channels=MIC_CHANNELS,
        dtype=MIC_DTYPE,
        callback=audio_callback
    )
    stream.start()
    print(f"✅ Micro đã bắt đầu thu âm ở {MIC_SAMPLE_RATE}Hz. Hãy bắt đầu nói...")

    while True:
        try:
            data = await loop.run_in_executor(None, audio_queue.get)

            # Chỉ gửi audio nếu bot không đang nói
            if not app_state.is_bot_speaking_event.is_set():
                await websocket.send(data.tobytes())
            # Nếu bot đang nói, dữ liệu từ queue sẽ được lấy ra và bỏ đi
            # để tránh hàng đợi bị đầy.

        except websockets.exceptions.ConnectionClosed:
            print("Kết nối đã đóng, dừng gửi audio.")
            break
        except Exception as e:
            print(f"Lỗi khi gửi audio: {e}")
            break

    stream.stop()
    stream.close()
    print("--- Đã dừng luồng thu âm. ---")


async def audio_player(websocket, app_state: AppState):
    player_stream = sd.OutputStream(
        samplerate=SPEAKER_SAMPLE_RATE,
        channels=SPEAKER_CHANNELS,
        dtype=SPEAKER_DTYPE
    )
    player_stream.start()
    print("✅ Loa đã sẵn sàng. Đang chờ audio từ server...")

    try:
        async for message in websocket:
            if isinstance(message, bytes):
                # Khi nhận được chunk audio, báo cho AppState biết là bot đang nói
                app_state.bot_is_speaking()

                audio_chunk = np.frombuffer(message, dtype=SPEAKER_DTYPE)
                player_stream.write(audio_chunk)
            else:
                # Xử lý tin nhắn text (JSON)
                try:
                    data = json.loads(message)
                    msg_type = data.get("type", "")
                    text = data.get("text", "")

                    if msg_type == "transcript":
                        is_final = data.get("is_final", False)
                        if is_final:
                            print(f"\rYou: {text}    ", end="\n")
                        else:
                            print(f"\r(đang nghe)... {text}", end="")

                    elif msg_type == "bot_response":
                        print(f"Bot: {text}")

                    elif msg_type == "error":
                        print(f"Lỗi từ Server: {text}")

                except json.JSONDecodeError:
                    print(f"SERVER (RAW TEXT): {message}")

    except websockets.exceptions.ConnectionClosed:
        print("Kết nối đã đóng, dừng nhận kết quả.")
    finally:
        player_stream.stop()
        player_stream.close()
        print("\n--- Đã dừng luồng phát audio. ---")


async def main():
    try:
        headers = { "Authorization": "Bearer minhngo" }
        async with websockets.connect(WEBSOCKET_URI, additional_headers=headers) as websocket:
            print(f"✅ Đã kết nối thành công tới {WEBSOCKET_URI}")

            loop = asyncio.get_running_loop()
            app_state = AppState(loop)

            sender_task = asyncio.create_task(mic_stream_sender(websocket, app_state))
            player_task = asyncio.create_task(audio_player(websocket, app_state))

            done, pending = await asyncio.wait(
                [sender_task, player_task],
                return_when=asyncio.FIRST_COMPLETED,
            )

            for task in pending:
                task.cancel()

    except Exception as e:
        print(f"Lỗi không mong muốn: {e}")


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        print("\nĐã nhận lệnh thoát (Ctrl+C).")