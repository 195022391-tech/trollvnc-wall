import Foundation
import RootEncoder
import AVFoundation

/// RootEncoder 相机 → RTSP TCP（优先 H.265）→ MediaMTX
final class RootStreamSession: NSObject, ConnectChecker {
    private var stream: RtspStream?
    private var running = false
    var onLog: ((String) -> Void)?

    func onConnectionSuccess() { onLog?("rtsp: connected") }
    func onConnectionFailed(reason: String) { onLog?("rtsp: failed \(reason)") }
    func onNewBitrate(bitrate: UInt64) {}
    func onDisconnect() { onLog?("rtsp: disconnected") }
    func onAuthError() { onLog?("rtsp: auth error") }
    func onAuthSuccess() { onLog?("rtsp: auth ok") }

    func start(host: String, deviceId: String, token: String, name: String) {
        stop()
        _ = name
        requestAV { [weak self] cam, mic in
            guard let self else { return }
            let st = AVCaptureDevice.authorizationStatus(for: .video)
            self.onLog?("相机权限 status=\(Self.authName(st)) granted=\(cam)")
            guard cam else {
                self.onLog?("无相机权限：设置 → 屏幕墙推流 → 相机")
                return
            }
            let back = AVCaptureDevice.default(.builtInWideAngleCamera, for: .video, position: .back)
            let front = AVCaptureDevice.default(.builtInWideAngleCamera, for: .video, position: .front)
            self.onLog?("相机设备 back=\(back != nil) front=\(front != nil)")
            if back == nil && front == nil {
                self.onLog?("系统找不到相机硬件，重装 tipa 或重启手机后再试")
                return
            }
            if !mic { self.onLog?("无麦克风：将尝试纯视频") }
            DispatchQueue.main.async {
                self.begin(host: host, deviceId: deviceId, token: token, wantAudio: mic)
            }
        }
    }

    private func begin(host: String, deviceId: String, token: String, wantAudio: Bool) {
        // WallCameraSource 避开 RootEncoder CameraManager 按 height 排序后误比 width 的检查
        let trials: [(Int, Int, Int)] = [
            (640, 480, 800_000),
            (1280, 720, 1_200_000),
            (352, 288, 400_000),
        ]
        let codecs: [VideoCodec] = [.H265, .H264]
        let rotation = CameraHelper.getCameraOrientation()
        onLog?("rotation=\(rotation)")

        for codec in codecs {
            let s = RtspStream(connectChecker: self)
            s.changeVideoSource(source: WallCameraSource())
            s.setVideoCodec(codec: codec)
            var vOK = false
            var used = (0, 0, 0)
            for t in trials {
                if s.prepareVideo(width: t.0, height: t.1, bitrate: t.2, fps: 30, iFrameInterval: 2, rotation: rotation) {
                    vOK = true
                    used = t
                    break
                }
                let stage = s.videoSource.created() ? "编码器" : "相机"
                onLog?("prepareVideo fail \(t.0)x\(t.1) \(codec) @\(stage)")
            }
            guard vOK else {
                s.release()
                continue
            }

            var onlyVideo = !wantAudio
            if wantAudio {
                let aOK = s.prepareAudio(sampleRate: 32_000, isStereo: true, bitrate: 64_000)
                if !aOK {
                    onLog?("prepareAudio fail → 纯视频")
                    onlyVideo = true
                }
            }
            let client = s.getStreamClient()
            client.setOnlyVideo(onlyVideo: onlyVideo)
            client.setRetries(reTries: 5)
            client.setLogs(enabled: true)

            let url = "rtsp://\(host):8554/\(deviceId)?token=\(token)"
            onLog?("start \(used.0)x\(used.1) \(codec) \(url)")
            s.startStream(endpoint: url)
            stream = s
            running = true
            return
        }
        onLog?("prepareVideo failed：相机或编码器都起不来")
    }

    func stop() {
        running = false
        if let s = stream {
            if s.isStreaming() { s.stopStream() }
            s.release()
        }
        stream = nil
    }

    private func requestAV(_ done: @escaping (Bool, Bool) -> Void) {
        let group = DispatchGroup()
        var cam = false
        var mic = false
        group.enter()
        AVCaptureDevice.requestAccess(for: .video) { ok in cam = ok; group.leave() }
        group.enter()
        AVCaptureDevice.requestAccess(for: .audio) { ok in mic = ok; group.leave() }
        group.notify(queue: .main) { done(cam, mic) }
    }

    private static func authName(_ s: AVAuthorizationStatus) -> String {
        switch s {
        case .authorized: return "authorized"
        case .denied: return "denied"
        case .restricted: return "restricted"
        case .notDetermined: return "notDetermined"
        @unknown default: return "unknown"
        }
    }
}
