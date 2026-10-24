import CoreVideo
import UIKit

enum TestPattern {
    static func make(width: Int, height: Int, index: UInt64, label: String) -> CVPixelBuffer? {
        var pb: CVPixelBuffer?
        let attrs: [String: Any] = [
            kCVPixelBufferCGImageCompatibilityKey as String: true,
            kCVPixelBufferCGBitmapContextCompatibilityKey as String: true,
        ]
        guard CVPixelBufferCreate(
            kCFAllocatorDefault, width, height, kCVPixelFormatType_32BGRA,
            attrs as CFDictionary, &pb
        ) == kCVReturnSuccess, let pb else { return nil }
        CVPixelBufferLockBaseAddress(pb, [])
        defer { CVPixelBufferUnlockBaseAddress(pb, []) }
        guard let base = CVPixelBufferGetBaseAddress(pb) else { return nil }
        let bytesPerRow = CVPixelBufferGetBytesPerRow(pb)
        guard let ctx = CGContext(
            data: base, width: width, height: height, bitsPerComponent: 8,
            bytesPerRow: bytesPerRow, space: CGColorSpaceCreateDeviceRGB(),
            bitmapInfo: CGImageAlphaInfo.premultipliedFirst.rawValue | CGBitmapInfo.byteOrder32Little.rawValue
        ) else { return nil }
        let w = CGFloat(width), h = CGFloat(height)
        let phase = CGFloat(index % 60) / 60
        ctx.setFillColor(UIColor(hue: phase, saturation: 0.65, brightness: 0.3, alpha: 1).cgColor)
        ctx.fill(CGRect(x: 0, y: 0, width: w, height: h))
        ctx.setFillColor(UIColor.systemTeal.cgColor)
        ctx.fill(CGRect(x: CGFloat(Int(index * 10) % max(width, 1)), y: 0, width: 36, height: h))
        UIGraphicsPushContext(ctx)
        ("\(label) #\(index)" as NSString).draw(at: CGPoint(x: 32, y: h / 2 - 20), withAttributes: [
            .font: UIFont.monospacedSystemFont(ofSize: 28, weight: .bold),
            .foregroundColor: UIColor.white,
        ])
        UIGraphicsPopContext()
        return pb
    }
}
