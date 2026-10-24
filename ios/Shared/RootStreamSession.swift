import Foundation
import RootEncoder
import AVFoundation

/// RootEncoder 摄像头 → RTSP TCP（H.265）→ MediaMTX
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
        requestCamera { [weak self] ok in
            guard let self else { return }
            guard ok else {
                self.onLog?("需要相机权限（设置里打开）")
                return
            }
            DispatchQueue.main.async {
                self.begin(host: host, deviceId: deviceId, token: token)
            }
        }
    }

    private func begin(host: String, deviceId: String, token: String) {
        let s = RtspStream(connectChecker: self)
        s.setVideoCodec(codec: .H265)
        // 预览：720p / 15fps / ~800kbps H.265；仅视频（少要麦克风）
        let vOK = s.prepareVideo(width: 1280, height: 720, bitrate: 800_000, fps: 15, iFrameInterval: 2, rotation: 0)
        guard vOK else {
            onLog?("prepareVideo failed")
            return
        }
        let client = s.getStreamClient()
        client.setOnlyVideo(onlyVideo: true)
        client.setRetries(reTries: 5)
        client.setLogs(enabled: true)

        let url = "rtsp://\(host):8554/\(deviceId)?token=\(token)"
        onLog?("start \(url) H.265 TCP")
        s.startStream(endpoint: url)
        stream = s
        running = true
    }

    func stop() {
        running = false
        if let s = stream {
            if s.isStreaming() { s.stopStream() }
            s.release()
        }
        stream = nil
    }

    private func requestCamera(_ done: @escaping (Bool) -> Void) {
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .authorized:
            done(true)
        case .notDetermined:
            AVCaptureDevice.requestAccess(for: .video, completionHandler: done)
        default:
            done(false)
        }
    }
}
