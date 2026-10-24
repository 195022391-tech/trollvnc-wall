import Foundation
import ReplayKit
import CoreMedia
import CoreVideo
import UIKit
import RootEncoder

/// ReplayKit 采屏；失败则窗口快照。不依赖相机。
final class WallScreenSource: VideoSource {
    private var metal: MetalInterface?
    private var ready = false
    private var running = false
    private var snapshotTimer: Timer?
    private var frameCount = 0
    var onInfo: ((String) -> Void)?

    func created() -> Bool { ready }

    func create(width: Int, height: Int, fps: Int, rotation: Int) -> Bool {
        _ = (width, height, fps, rotation)
        ready = true
        return true
    }

    func start(metalInterface: MetalInterface) {
        metal = metalInterface
        guard ready, !running else { return }
        running = true
        frameCount = 0
        startReplayKit()
    }

    func stop() {
        running = false
        snapshotTimer?.invalidate()
        snapshotTimer = nil
        let rec = RPScreenRecorder.shared()
        if rec.isRecording {
            rec.stopCapture { _ in }
        }
    }

    func isRunning() -> Bool { running }

    func release() {
        stop()
        metal = nil
        ready = false
    }

    private func startReplayKit() {
        let rec = RPScreenRecorder.shared()
        guard rec.isAvailable else {
            onInfo?("ReplayKit 不可用 → 窗口快照")
            startSnapshot()
            return
        }
        rec.isMicrophoneEnabled = false
        rec.startCapture { [weak self] sample, type, error in
            guard let self, self.running else { return }
            if error != nil {
                DispatchQueue.main.async {
                    self.onInfo?("ReplayKit 中断 → 窗口快照")
                    self.startSnapshot()
                }
                return
            }
            guard type == .video else { return }
            self.emit(sample)
        } completionHandler: { [weak self] error in
            DispatchQueue.main.async {
                if let error {
                    self?.onInfo?("ReplayKit 失败：\(error.localizedDescription) → 窗口快照")
                    self?.startSnapshot()
                } else {
                    self?.onInfo?("ReplayKit 采屏中")
                }
            }
        }
    }

    private func startSnapshot() {
        guard running else { return }
        snapshotTimer?.invalidate()
        let scale = min(UIScreen.main.scale, 2)
        let bounds = UIScreen.main.bounds
        let w = max(2, Int(bounds.width * scale))
        let h = max(2, Int(bounds.height * scale))
        onInfo?("窗口快照 \(w)x\(h) @12fps（保持 App 前台）")
        let timer = Timer(timeInterval: 1.0 / 12.0, repeats: true) { [weak self] _ in
            self?.captureWindow(width: w, height: h)
        }
        RunLoop.main.add(timer, forMode: .common)
        snapshotTimer = timer
    }

    private func captureWindow(width: Int, height: Int) {
        guard running else { return }
        var pb: CVPixelBuffer?
        let attrs: [String: Any] = [
            kCVPixelBufferCGImageCompatibilityKey as String: true,
            kCVPixelBufferCGBitmapContextCompatibilityKey as String: true,
        ]
        guard CVPixelBufferCreate(
            kCFAllocatorDefault, width, height, kCVPixelFormatType_32BGRA,
            attrs as CFDictionary, &pb
        ) == kCVReturnSuccess, let pb else { return }

        CVPixelBufferLockBaseAddress(pb, [])
        defer { CVPixelBufferUnlockBaseAddress(pb, []) }
        guard let base = CVPixelBufferGetBaseAddress(pb) else { return }
        let bytesPerRow = CVPixelBufferGetBytesPerRow(pb)
        let colorSpace = CGColorSpaceCreateDeviceRGB()
        guard let ctx = CGContext(
            data: base, width: width, height: height, bitsPerComponent: 8,
            bytesPerRow: bytesPerRow, space: colorSpace,
            bitmapInfo: CGImageAlphaInfo.premultipliedFirst.rawValue | CGBitmapInfo.byteOrder32Little.rawValue
        ) else { return }

        var window: UIWindow?
        for scene in UIApplication.shared.connectedScenes {
            guard let ws = scene as? UIWindowScene else { continue }
            window = ws.windows.first(where: \.isKeyWindow) ?? ws.windows.first
            if window != nil { break }
        }
        guard let window else { return }
        UIGraphicsPushContext(ctx)
        ctx.translateBy(x: 0, y: CGFloat(height))
        ctx.scaleBy(x: CGFloat(width) / window.bounds.width, y: -(CGFloat(height) / window.bounds.height))
        window.drawHierarchy(in: window.bounds, afterScreenUpdates: false)
        UIGraphicsPopContext()

        guard let sample = Self.sampleBuffer(from: pb) else { return }
        emit(sample)
    }

    private func emit(_ sample: CMSampleBuffer) {
        metal?.sendBuffer(buffer: sample)
        frameCount += 1
        if frameCount == 1 || frameCount % 60 == 0 {
            onInfo?("已出帧 \(frameCount)")
        }
    }

    private static func sampleBuffer(from pixelBuffer: CVPixelBuffer) -> CMSampleBuffer? {
        var format: CMVideoFormatDescription?
        guard CMVideoFormatDescriptionCreateForImageBuffer(
            allocator: kCFAllocatorDefault,
            imageBuffer: pixelBuffer,
            formatDescriptionOut: &format
        ) == noErr, let format else { return nil }

        var timing = CMSampleTimingInfo(
            duration: CMTime(value: 1, timescale: 12),
            presentationTimeStamp: CMClockGetTime(CMClockGetHostTimeClock()),
            decodeTimeStamp: .invalid
        )
        var sample: CMSampleBuffer?
        guard CMSampleBufferCreateForImageBuffer(
            allocator: kCFAllocatorDefault,
            imageBuffer: pixelBuffer,
            dataReady: true,
            makeDataReadyCallback: nil,
            refcon: nil,
            formatDescription: format,
            sampleTiming: &timing,
            sampleBufferOut: &sample
        ) == noErr else { return nil }
        return sample
    }
}
