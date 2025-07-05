import asyncio
import json
import websockets
import sounddevice as sd
import numpy as np

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



class AppState:
    def __init__(self, loop):
        self.loop = loop
        self.is_bot_speaking_event = asyncio.Event()
        self.end_of_speech_timer = None
        self.is_bot_speaking_event.clear()

    def bot_is_speaking(self):
        if self.end_of_speech_timer:
            self.end_of_speech_timer.cancel()
        self.is_bot_speaking_event.set()
        self.end_of_speech_timer = self.loop.call_later(
            BOT_END_OF_SPEECH_DELAY,
            self.bot_has_stopped_speaking
        )

    def bot_has_stopped_speaking(self):
        self.is_bot_speaking_event.clear()
        print("\n✅ Bot đã nói xong. Bạn có thể nói tiếp.")


# truy cập vào event loop và asyncio.Queue
def create_audio_callback(loop, audio_queue):
    def audio_callback(indata, frames, time, status):
        if status:
            print(f"Lỗi thu âm: {status}")
        # Sử dụng call_soon_threadsafe để đưa dữ liệu vào asyncio.Queue từ một thread khác
        # một cách an toàn và không bị block.
        loop.call_soon_threadsafe(audio_queue.put_nowait, indata.copy())
    return audio_callback


async def mic_stream_sender(websocket, app_state: AppState, audio_queue: asyncio.Queue):
    stream = sd.InputStream(
        samplerate=MIC_SAMPLE_RATE,
        blocksize=MIC_BLOCKSIZE_SAMPLES,
        channels=MIC_CHANNELS,
        dtype=MIC_DTYPE,
        callback=create_audio_callback(app_state.loop, audio_queue) # Truyền loop và queue vào callback
    )
    stream.start()
    print(f"✅ Micro đã bắt đầu thu âm ở {MIC_SAMPLE_RATE}Hz. Hãy bắt đầu nói...")

    while True:
        try:
            # Lấy dữ liệu từ asyncio.Queue một cách trực tiếp và hiệu quả
            data = await audio_queue.get()

            if not app_state.is_bot_speaking_event.is_set():
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
                app_state.bot_is_speaking()
                audio_chunk = np.frombuffer(message, dtype=SPEAKER_DTYPE)
                player_stream.write(audio_chunk)
            else:
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

            # <--- THAY ĐỔI: Tạo asyncio.Queue ở đây
            audio_queue = asyncio.Queue()

            sender_task = asyncio.create_task(mic_stream_sender(websocket, app_state, audio_queue))
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