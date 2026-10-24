import AVFoundation
import RootEncoder

/// 绕过 RootEncoder CameraManager 错误的分辨率上下界检查（按 height 排序后用 first/last 的 width 比较会误杀 640x480）。
final class WallCameraSource: VideoSource {
    private let queue = DispatchQueue(label: "wall.cam")
    private var session: AVCaptureSession?
    private var metal: MetalInterface?
    private var ready = false
    private var running = false
    private var preset: AVCaptureSession.Preset = .vga640x480
    private var rotation = 90
    private lazy var sink = Sink(owner: self)

    func created() -> Bool { ready }

    func create(width: Int, height: Int, fps: Int, rotation: Int) -> Bool {
        _ = fps
        guard AVCaptureDevice.default(.builtInWideAngleCamera, for: .video, position: .back) != nil
            || AVCaptureDevice.default(.builtInWideAngleCamera, for: .video, position: .front) != nil
        else { return false }
        self.rotation = rotation
        preset = Self.preset(width: width, height: height)
        ready = true
        return true
    }

    func start(metalInterface: MetalInterface) {
        metal = metalInterface
        guard ready, !running else { return }
        let session = AVCaptureSession()
        session.beginConfiguration()
        if session.canSetSessionPreset(preset) {
            session.sessionPreset = preset
        } else if session.canSetSessionPreset(.vga640x480) {
            session.sessionPreset = .vga640x480
        }
        let device = AVCaptureDevice.default(.builtInWideAngleCamera, for: .video, position: .back)
            ?? AVCaptureDevice.default(.builtInWideAngleCamera, for: .video, position: .front)
        guard let device,
              let input = try? AVCaptureDeviceInput(device: device),
              session.canAddInput(input)
        else { return }
        session.addInput(input)
        let output = AVCaptureVideoDataOutput()
        output.alwaysDiscardsLateVideoFrames = true
        output.videoSettings = [kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32BGRA]
        output.setSampleBufferDelegate(sink, queue: queue)
        guard session.canAddOutput(output) else { return }
        session.addOutput(output)
        output.connections.filter(\.isVideoOrientationSupported).forEach {
            $0.videoOrientation = Self.orientation(rotation)
        }
        session.commitConfiguration()
        self.session = session
        queue.async { session.startRunning() }
        running = true
    }

    func stop() {
        session?.stopRunning()
        session = nil
        running = false
    }

    func isRunning() -> Bool { running }

    func release() {
        stop()
        metal = nil
        ready = false
    }

    fileprivate func send(_ sampleBuffer: CMSampleBuffer) {
        metal?.sendBuffer(buffer: sampleBuffer)
    }

    private static func preset(width: Int, height: Int) -> AVCaptureSession.Preset {
        switch (width, height) {
        case (352, 288): return .cif352x288
        case (1280, 720): return .hd1280x720
        case (1920, 1080): return .hd1920x1080
        default: return .vga640x480
        }
    }

    private static func orientation(_ r: Int) -> AVCaptureVideoOrientation {
        switch r {
        case 90: return .portrait
        case 270: return .portraitUpsideDown
        case 180: return .landscapeRight
        default: return .landscapeLeft
        }
    }

    /// NSObject 子类单独做 delegate，避免 `isRunning()` 与 ObjC 布尔命名冲突导致 VideoSource 不满足。
    private final class Sink: NSObject, AVCaptureVideoDataOutputSampleBufferDelegate {
        weak var owner: WallCameraSource?
        init(owner: WallCameraSource) { self.owner = owner }
        func captureOutput(_ output: AVCaptureOutput, didOutput sampleBuffer: CMSampleBuffer, from connection: AVCaptureConnection) {
            owner?.send(sampleBuffer)
        }
    }
}
