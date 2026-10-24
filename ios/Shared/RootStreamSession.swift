import Foundation
import RootEncoder
import ReplayKit
import UIKit

/// RootEncoder 屏幕（ReplayKit）→ RTSP TCP（优先 H.265）→ MediaMTX
/// 不依赖相机硬件。
final class RootStreamSession: NSObject, ConnectChecker {
    private var stream: RtspDisplay?
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
        guard RPScreenRecorder.shared().isAvailable else {
            onLog?("ReplayKit 不可用，无法采屏")
            return
        }
        onLog?("采屏模式（无相机）先要录屏权限")
        preflightScreen { [weak self] ok in
            guard let self else { return }
            guard ok else {
                self.onLog?("录屏未授权：控制中心/弹窗里允许「屏幕录制」")
                return
            }
            self.onLog?("录屏已就绪，开始推 RTSP")
            self.begin(host: host, deviceId: deviceId, token: token)
        }
    }

    /// 先起再停一次，把系统录屏授权拿下来，避免连上 MTX 后 5s 内无帧被踢。
    private func preflightScreen(_ done: @escaping (Bool) -> Void) {
        let rec = RPScreenRecorder.shared()
        rec.isMicrophoneEnabled = false
        rec.startCapture(handler: { _, _, _ in }, completionHandler: { [weak self] err in
            if let err {
                self?.onLog?("录屏启动失败：\(err.localizedDescription)")
                DispatchQueue.main.async { done(false) }
                return
            }
            rec.stopCapture { stopErr in
                if let stopErr {
                    self?.onLog?("录屏预检 stop：\(stopErr.localizedDescription)")
                }
                DispatchQueue.main.async { done(true) }
            }
        })
    }

    private func begin(host: String, deviceId: String, token: String) {
        let trials: [(Int, Int, Int)] = [
            (720, 1280, 1_200_000),
            (540, 960, 800_000),
            (480, 640, 500_000),
        ]
        let codecs: [VideoCodec] = [.H265, .H264]

        for codec in codecs {
            let s = RtspDisplay(connectChecker: self)
            s.setVideoCodec(codec: codec)
            var vOK = false
            var used = (0, 0, 0)
            for t in trials {
                if s.prepareVideo(width: t.0, height: t.1, fps: 30, bitrate: t.2, iFrameInterval: 2, rotation: 0) {
                    vOK = true
                    used = t
                    break
                }
                onLog?("prepareVideo fail \(t.0)x\(t.1) \(codec)")
            }
            guard vOK else { continue }

            let client = s.getStreamClient()
            client.setOnlyVideo(onlyVideo: true)
            client.setRetries(reTries: 5)
            client.setLogs(enabled: true)

            let url = "rtsp://\(host):8554/\(deviceId)?token=\(token)"
            onLog?("start screen \(used.0)x\(used.1) \(codec) \(url)")
            s.startStream(endpoint: url)
            stream = s
            running = true
            return
        }
        onLog?("prepareVideo failed：编码器起不来")
    }

    func stop() {
        running = false
        if let s = stream {
            if s.isStreaming() { s.stopStream() }
        }
        stream = nil
    }
}
