import Foundation
import Darwin

/// 墙控制通道：[u32 BE len][json] — POSIX TCP（CFStream 能连上但不写出 hello）
final class CtrlClient {
    private var fd: Int32 = -1
    private let queue = DispatchQueue(label: "cn.local.wallphone.ctrl")
    private var running = false
    private var recvBuf = Data()
    var onLog: ((String) -> Void)?
    var onSetQuality: ((_ mode: String, _ seq: UInt64, _ forceIdr: Bool, _ w: Int, _ h: Int, _ br: Int, _ fps: Int) -> Void)?

    func connect(host: String, port: Int, deviceId: String, token: String, name: String) {
        disconnect()
        queue.async { [weak self] in
            guard let self else { return }
            do {
                let sock = try Self.tcpConnect(host: host, port: port, timeoutSec: 8)
                self.fd = sock
                self.running = true
                self.onLog?("ctrl: tcp ready fd=\(sock)")

                let hello: [String: Any] = [
                    "type": "hello",
                    "body": ["deviceId": deviceId, "token": token, "name": name],
                ]
                try self.writeJSON(hello)
                self.onLog?("ctrl: hello sent")

                self.loop(deviceId: deviceId, token: token, name: name)
            } catch {
                self.onLog?("ctrl: \(error.localizedDescription)")
                self.closeFD()
            }
        }
    }

    func disconnect() {
        running = false
        queue.async { [weak self] in self?.closeFD() }
    }

    private func closeFD() {
        running = false
        if fd >= 0 {
            Darwin.close(fd)
            fd = -1
        }
        recvBuf.removeAll()
    }

    private func loop(deviceId: String, token: String, name: String) {
        var lastHb = Date.distantPast
        var buf = [UInt8](repeating: 0, count: 8192)
        while running {
            let now = Date()
            if now.timeIntervalSince(lastHb) >= 0.5 {
                try? writeJSON(["type": "heartbeat"])
                lastHb = now
            }
            // 200ms poll
            var pfd = pollfd(fd: fd, events: Int16(POLLIN), revents: 0)
            let pr = poll(&pfd, 1, 200)
            if pr < 0 {
                if errno == EINTR { continue }
                onLog?("ctrl: poll err \(errno)")
                break
            }
            if pr == 0 { continue }
            if pfd.revents & Int16(POLLERR | POLLHUP | POLLNVAL) != 0 {
                onLog?("ctrl: peer closed")
                break
            }
            if pfd.revents & Int16(POLLIN) != 0 {
                let n = Darwin.recv(fd, &buf, buf.count, 0)
                if n <= 0 {
                    onLog?("ctrl: recv end n=\(n)")
                    break
                }
                recvBuf.append(contentsOf: buf[0 ..< n])
                drainFrames()
            }
        }
        closeFD()
    }

    private func drainFrames() {
        while recvBuf.count >= 4 {
            let n = Int(recvBuf[0]) << 24 | Int(recvBuf[1]) << 16 | Int(recvBuf[2]) << 8 | Int(recvBuf[3])
            guard n > 0, n <= 65536 else {
                onLog?("ctrl: bad len \(n)")
                closeFD()
                return
            }
            guard recvBuf.count >= 4 + n else { return }
            let payload = recvBuf.subdata(in: 4 ..< (4 + n))
            recvBuf.removeSubrange(0 ..< (4 + n))
            handle(payload)
        }
    }

    private func handle(_ data: Data) {
        guard let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let type = obj["type"] as? String else { return }
        let seq = (obj["seq"] as? NSNumber)?.uint64Value ?? 0
        switch type {
        case "hello_ok":
            onLog?("ctrl: hello_ok")
        case "set_quality":
            let body = obj["body"] as? [String: Any] ?? [:]
            let mode = body["mode"] as? String ?? "preview"
            let force = body["forceIdr"] as? Bool ?? true
            let w = (body["width"] as? NSNumber)?.intValue ?? 1280
            let h = (body["height"] as? NSNumber)?.intValue ?? 720
            let br = (body["bitrateKbps"] as? NSNumber)?.intValue ?? 600
            let fps = (body["fps"] as? NSNumber)?.intValue ?? 15
            onSetQuality?(mode, seq, force, w, h, br, fps)
            let ack: [String: Any] = [
                "type": "switch_ack",
                "seq": seq,
                "body": ["ok": true, "width": w, "height": h],
            ]
            try? writeJSON(ack)
            try? writeJSON(["type": "ack", "seq": seq])
        case "error":
            onLog?("ctrl error: \(obj)")
        default:
            break
        }
    }

    private func writeJSON(_ obj: [String: Any]) throws {
        guard fd >= 0, running else {
            throw NSError(domain: "ctrl", code: 1, userInfo: [NSLocalizedDescriptionKey: "not connected"])
        }
        let payload = try JSONSerialization.data(withJSONObject: obj)
        var len = UInt32(payload.count).bigEndian
        var frame = Data(bytes: &len, count: 4)
        frame.append(payload)
        try frame.withUnsafeBytes { raw in
            var sent = 0
            let total = raw.count
            let base = raw.bindMemory(to: UInt8.self).baseAddress!
            while sent < total {
                let n = Darwin.send(fd, base.advanced(by: sent), total - sent, 0)
                if n < 0 {
                    if errno == EINTR { continue }
                    throw NSError(domain: "ctrl", code: 4, userInfo: [NSLocalizedDescriptionKey: "send errno=\(errno)"])
                }
                if n == 0 { throw NSError(domain: "ctrl", code: 4, userInfo: [NSLocalizedDescriptionKey: "send 0"]) }
                sent += n
            }
        }
    }

    private static func tcpConnect(host: String, port: Int, timeoutSec: Int) throws -> Int32 {
        var hints = addrinfo(
            ai_flags: 0,
            ai_family: AF_INET,
            ai_socktype: SOCK_STREAM,
            ai_protocol: IPPROTO_TCP,
            ai_addrlen: 0,
            ai_canonname: nil,
            ai_addr: nil,
            ai_next: nil
        )
        var info: UnsafeMutablePointer<addrinfo>?
        let portStr = String(port)
        let err = host.withCString { h in
            portStr.withCString { p in
                getaddrinfo(h, p, &hints, &info)
            }
        }
        guard err == 0, let info else {
            throw NSError(domain: "ctrl", code: 7, userInfo: [NSLocalizedDescriptionKey: "getaddrinfo \(err)"])
        }
        defer { freeaddrinfo(info) }

        var lastErr = 0
        var ptr: UnsafeMutablePointer<addrinfo>? = info
        while let ai = ptr {
            let sock = socket(ai.pointee.ai_family, ai.pointee.ai_socktype, ai.pointee.ai_protocol)
            if sock < 0 {
                lastErr = errno
                ptr = ai.pointee.ai_next
                continue
            }
            // nonblock connect + poll
            let flags = fcntl(sock, F_GETFL, 0)
            _ = fcntl(sock, F_SETFL, flags | O_NONBLOCK)
            let cr = Darwin.connect(sock, ai.pointee.ai_addr, ai.pointee.ai_addrlen)
            if cr == 0 {
                _ = fcntl(sock, F_SETFL, flags)
                return sock
            }
            if errno != EINPROGRESS {
                lastErr = errno
                Darwin.close(sock)
                ptr = ai.pointee.ai_next
                continue
            }
            var pfd = pollfd(fd: sock, events: Int16(POLLOUT), revents: 0)
            let pr = poll(&pfd, 1, Int32(timeoutSec * 1000))
            if pr > 0, pfd.revents & Int16(POLLOUT) != 0 {
                var soErr: Int32 = 0
                var len = socklen_t(MemoryLayout<Int32>.size)
                getsockopt(sock, SOL_SOCKET, SO_ERROR, &soErr, &len)
                if soErr == 0 {
                    _ = fcntl(sock, F_SETFL, flags) // back to blocking
                    return sock
                }
                lastErr = soErr
            } else {
                lastErr = errno == 0 ? ETIMEDOUT : errno
            }
            Darwin.close(sock)
            ptr = ai.pointee.ai_next
        }
        throw NSError(domain: "ctrl", code: 8, userInfo: [NSLocalizedDescriptionKey: "connect errno=\(lastErr)"])
    }
}
