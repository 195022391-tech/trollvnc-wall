import Foundation

/// Minimal RTSP TCP interleaved publish → MediaMTX (H.264 only).
final class RTSPPublisher {
    private var input: InputStream?
    private var output: OutputStream?
    private let queue = DispatchQueue(label: "cn.local.wallphone.rtsp")
    private var cseq = 1
    private var session: String = ""
    private var seq: UInt16 = 0
    private var ssrc: UInt32 = UInt32.random(in: 1...UInt32.max)
    private var rtpTimestamp: UInt32 = 0
    private var started = false
    var onLog: ((String) -> Void)?

    func connect(host: String, port: Int, path: String, token: String, width: Int, height: Int, fps: Int) throws {
        disconnect()
        var readStream: Unmanaged<CFReadStream>?
        var writeStream: Unmanaged<CFWriteStream>?
        CFStreamCreatePairWithSocketToHost(nil, host as CFString, UInt32(port), &readStream, &writeStream)
        guard let rs = readStream?.takeRetainedValue(), let ws = writeStream?.takeRetainedValue() else {
            throw NSError(domain: "rtsp", code: 1, userInfo: [NSLocalizedDescriptionKey: "stream create failed"])
        }
        let input = rs as InputStream
        let output = ws as OutputStream
        self.input = input
        self.output = output
        input.schedule(in: .current, forMode: .common)
        output.schedule(in: .current, forMode: .common)
        input.open()
        output.open()
        try Self.waitOpen(input, output, timeoutMs: 5000)

        let base = "rtsp://\(host):\(port)/\(path)?token=\(token)"
        lastBase = base
        _ = try request("OPTIONS", url: base, headers: [:])
        let sdp = """
        v=0
        o=- 0 0 IN IP4 127.0.0.1
        s=WallPhone
        c=IN IP4 0.0.0.0
        t=0 0
        m=video 0 RTP/AVP 96
        a=rtpmap:96 H264/90000
        a=fmtp:96 packetization-mode=1
        a=control:trackID=0
        """
        _ = try request("ANNOUNCE", url: base, headers: [
            "Content-Type": "application/sdp",
            "Content-Length": "\(sdp.utf8.count)",
        ], body: sdp)
        // track URL: path before ?query
        let setupURL: String = {
            if let q = base.firstIndex(of: "?") {
                return String(base[..<q]) + "/trackID=0" + String(base[q...])
            }
            return base + "/trackID=0"
        }()
        let setupResp = try request("SETUP", url: setupURL, headers: [
            "Transport": "RTP/AVP/TCP;unicast;interleaved=0-1;mode=record",
        ])
        if let sess = setupResp["Session"] {
            session = sess.split(separator: ";").first.map(String.init) ?? sess
        }
        _ = try request("RECORD", url: base, headers: [
            "Session": session,
            "Range": "npt=0.000-",
        ])
        started = true
        onLog?("rtsp: RECORD ok session=\(session)")
    }

    func disconnect() {
        started = false
        if !session.isEmpty, let hostPort = currentBase() {
            try? request("TEARDOWN", url: hostPort, headers: ["Session": session])
        }
        session = ""
        input?.close()
        output?.close()
        input = nil
        output = nil
    }

    private var lastBase: String?
    private func currentBase() -> String? { lastBase }

    func sendAnnexBNAL(_ nal: Data, pts90k: UInt32, marker: Bool) {
        guard started, let output else { return }
        queue.sync {
            self.rtpTimestamp = pts90k
            self.packetizeAndSend(nal, marker: marker, output: output)
        }
    }

    func sendAVCCFrame(_ avcc: Data, isKeyframe: Bool, ptsUs: UInt64, sps: Data?, pps: Data?) {
        guard started else { return }
        let pts90k = UInt32((ptsUs * 90) / 1000) // us → 90kHz
        queue.sync {
            self.rtpTimestamp = pts90k
            if isKeyframe, let sps, let pps {
                self.packetizeAndSend(sps, marker: false, output: self.output!)
                self.packetizeAndSend(pps, marker: false, output: self.output!)
            }
            let nals = H264Encoder.nalUnits(fromAVCC: avcc)
            for (i, nal) in nals.enumerated() {
                self.packetizeAndSend(nal, marker: i == nals.count - 1, output: self.output!)
            }
        }
    }

    private func packetizeAndSend(_ nal: Data, marker: Bool, output: OutputStream) {
        let mtu = 1200
        if nal.count <= mtu {
            var rtp = Data()
            var b0: UInt8 = 0x80
            var b1: UInt8 = 96 | (marker ? 0x80 : 0)
            rtp.append(b0)
            rtp.append(b1)
            var s = seq.bigEndian
            rtp.append(Data(bytes: &s, count: 2))
            var ts = rtpTimestamp.bigEndian
            rtp.append(Data(bytes: &ts, count: 4))
            var ss = ssrc.bigEndian
            rtp.append(Data(bytes: &ss, count: 4))
            rtp.append(nal)
            seq &+= 1
            writeInterleaved(channel: 0, payload: rtp, output: output)
            return
        }
        // FU-A
        let nalHeader = nal[nal.startIndex]
        let type = nalHeader & 0x1f
        let nri = nalHeader & 0x60
        var offset = nal.startIndex + 1
        var first = true
        while offset < nal.endIndex {
            let end = min(offset + (mtu - 2), nal.endIndex)
            let chunk = nal[offset ..< end]
            let last = end == nal.endIndex
            var rtp = Data()
            rtp.append(0x80)
            rtp.append(96 | (last && marker ? 0x80 : 0))
            var s = seq.bigEndian
            rtp.append(Data(bytes: &s, count: 2))
            var ts = rtpTimestamp.bigEndian
            rtp.append(Data(bytes: &ts, count: 4))
            var ss = ssrc.bigEndian
            rtp.append(Data(bytes: &ss, count: 4))
            rtp.append(nri | 28) // FU-A
            var fu: UInt8 = type
            if first { fu |= 0x80 }
            if last { fu |= 0x40 }
            rtp.append(fu)
            rtp.append(chunk)
            seq &+= 1
            writeInterleaved(channel: 0, payload: rtp, output: output)
            first = false
            offset = end
        }
    }

    private func writeInterleaved(channel: UInt8, payload: Data, output: OutputStream) {
        var hdr = Data([0x24, channel])
        var len = UInt16(payload.count).bigEndian
        hdr.append(Data(bytes: &len, count: 2))
        hdr.append(payload)
        _ = try? writeAll(output, hdr)
    }

    private func request(_ method: String, url: String, headers: [String: String], body: String? = nil) throws -> [String: String] {
        lastBase = url
        guard let output, let input else { throw NSError(domain: "rtsp", code: 2) }
        var lines = ["\(method) \(url) RTSP/1.0", "CSeq: \(cseq)"]
        cseq += 1
        if !session.isEmpty, headers["Session"] == nil {
            lines.append("Session: \(session)")
        }
        for (k, v) in headers {
            lines.append("\(k): \(v)")
        }
        lines.append("User-Agent: WallPhone/0.1")
        lines.append("")
        var msg = lines.joined(separator: "\r\n") + "\r\n"
        if let body { msg += body }
        try writeAll(output, Data(msg.utf8))
        return try readResponse(input)
    }

    private func readResponse(_ input: InputStream) throws -> [String: String] {
        var data = Data()
        var buf = [UInt8](repeating: 0, count: 4096)
        // read until \r\n\r\n
        while true {
            let n = input.read(&buf, maxLength: buf.count)
            if n <= 0 { throw NSError(domain: "rtsp", code: 3) }
            data.append(buf, count: n)
            if let range = data.range(of: Data("\r\n\r\n".utf8)) {
                let head = String(data: data.subdata(in: data.startIndex ..< range.lowerBound), encoding: .utf8) ?? ""
                var headers: [String: String] = [:]
                let parts = head.split(separator: "\r\n")
                guard let status = parts.first, status.contains("200") else {
                    onLog?("rtsp resp: \(head.prefix(200))")
                    throw NSError(domain: "rtsp", code: 4, userInfo: [NSLocalizedDescriptionKey: head])
                }
                for line in parts.dropFirst() {
                    if let i = line.firstIndex(of: ":") {
                        let k = String(line[..<i])
                        let v = line[line.index(after: i)...].trimmingCharacters(in: .whitespaces)
                        headers[k] = v
                    }
                }
                if let cl = headers["Content-Length"], let n = Int(cl), n > 0 {
                    var need = n - (data.count - range.upperBound)
                    while need > 0 {
                        let r = input.read(&buf, maxLength: min(buf.count, need))
                        if r <= 0 { break }
                        need -= r
                    }
                }
                return headers
            }
            if data.count > 65536 { throw NSError(domain: "rtsp", code: 5) }
        }
    }

    private func writeAll(_ stream: OutputStream, _ data: Data) throws {
        var offset = 0
        while offset < data.count {
            let wrote = data.withUnsafeBytes { raw -> Int in
                stream.write(raw.bindMemory(to: UInt8.self).baseAddress!.advanced(by: offset), maxLength: data.count - offset)
            }
            if wrote <= 0 { throw NSError(domain: "rtsp", code: 6) }
            offset += wrote
        }
    }

    private static func waitOpen(_ input: InputStream, _ output: OutputStream, timeoutMs: Int) throws {
        let deadline = Date().addingTimeInterval(Double(timeoutMs) / 1000)
        while Date() < deadline {
            let inSt = input.streamStatus
            let outSt = output.streamStatus
            if inSt == .error || outSt == .error {
                throw NSError(domain: "rtsp", code: 7, userInfo: [NSLocalizedDescriptionKey: "tcp connect failed"])
            }
            if (inSt == .open || inSt == .reading) && (outSt == .open || outSt == .writing) { return }
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        }
        throw NSError(domain: "rtsp", code: 8, userInfo: [NSLocalizedDescriptionKey: "tcp connect timeout"])
    }
}
